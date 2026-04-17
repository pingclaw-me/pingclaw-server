// Package pingclaw implements the PingClaw web dashboard and iOS app
// authentication and location endpoints. All routes are mounted under
// /pingclaw/ — see cmd/server/main.go.
package pingclaw

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/pingclaw-me/pingclaw-server/internal/ratelimit"
	"github.com/pingclaw-me/pingclaw-server/internal/sms"
)

type Handler struct {
	db      *sql.DB
	rdb     *redis.Client
	sms     *sms.Client       // nil in dev → log code instead of sending SMS
	limiter *ratelimit.Limiter
	cfg     RateLimitConfig
}

// RateLimitConfig sets the per-window event caps for sign-in.
type RateLimitConfig struct {
	PerPhonePerHour int
	PerIPPerHour    int
}

func NewHandler(db *sql.DB, rdb *redis.Client, smsClient *sms.Client, limiter *ratelimit.Limiter, cfg RateLimitConfig) *Handler {
	if cfg.PerPhonePerHour <= 0 {
		cfg.PerPhonePerHour = 3
	}
	if cfg.PerIPPerHour <= 0 {
		cfg.PerIPPerHour = 10
	}
	return &Handler{db: db, rdb: rdb, sms: smsClient, limiter: limiter, cfg: cfg}
}

// --- Verification code store (Redis, 10-minute TTL) ---

const codeTTL = 10 * time.Minute

// codeKey is the Redis key for a pending verification code, keyed by
// the SHA-256 hash of the phone number (no plaintext phone in Redis).
func codeKey(phoneHash string) string { return "code:" + phoneHash }

// locationTTL is how long a location data point survives in Redis after
// it's written. Per the privacy policy, locations expire automatically
// after 24 hours.
const locationTTL = 24 * time.Hour

// locationKey is the Redis key namespace used to store the most recent
// location for a given user.
func locationKey(userID string) string { return "loc:" + userID }

// cachedLocation is the JSON shape persisted to Redis under loc:<user_id>.
// All readers (GetLocation, TestWebhook, GetMyData, MCP get_my_location)
// pull this struct.
type cachedLocation struct {
	Lat            float64  `json:"lat"`
	Lng            float64  `json:"lng"`
	AccuracyMetres *float64 `json:"accuracy_metres,omitempty"`
	Activity       string   `json:"activity,omitempty"`
	Timestamp      string   `json:"timestamp"`   // RFC3339, sent by client
	ReceivedAt     string   `json:"received_at"` // RFC3339, set by server
}

// readLocation pulls the cached location for a user. Returns (nil, nil)
// if there's nothing cached (either never set or expired).
func (h *Handler) readLocation(ctx context.Context, userID string) (*cachedLocation, error) {
	raw, err := h.rdb.Get(ctx, locationKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var loc cachedLocation
	if err := json.Unmarshal([]byte(raw), &loc); err != nil {
		return nil, err
	}
	return &loc, nil
}

// writeLocation stores the location in Redis with a 24-hour TTL.
func (h *Handler) writeLocation(ctx context.Context, userID string, loc cachedLocation) error {
	body, err := json.Marshal(loc)
	if err != nil {
		return err
	}
	return h.rdb.Set(ctx, locationKey(userID), body, locationTTL).Err()
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func generateCode() string {
	return fmt.Sprintf("%06d", mathrand.Intn(1000000))
}

func generateToken(prefix string) string {
	b := make([]byte, 16)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// hashToken uses SHA-256 for fast lookup. Tokens are random, so SHA-256
// is safe here (no need for bcrypt's slow password hashing).
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func normalizePhone(p string) string {
	// Strip spaces, dashes, parens — keep + and digits.
	var sb strings.Builder
	for _, c := range p {
		if c == '+' || (c >= '0' && c <= '9') {
			sb.WriteRune(c)
		}
	}
	return sb.String()
}

// --- Auth endpoints ---

func (h *Handler) SendCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PhoneNumber string `json:"phone_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	phone := normalizePhone(req.PhoneNumber)
	if len(phone) < 8 {
		writeError(w, 400, "valid phone number required")
		return
	}
	// US/CA only at launch — both share the +1 country code (NANP).
	// We're not unpacking the area code beyond that; the wider NANP
	// (Caribbean) is out of scope but acceptable to allow at this stage.
	if !strings.HasPrefix(phone, "+1") {
		writeError(w, 400, "only US and Canada phone numbers are supported")
		return
	}

	phoneHash := hashToken(phone)

	// Per-phone rate limit. We rate-limit on the hash so the limiter
	// keys are themselves anonymous.
	if ok, retryAfter, _ := h.limiter.Allow(r.Context(), "rl:phone:"+phoneHash, h.cfg.PerPhonePerHour); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, 429, "too many code requests for this phone number — try again later")
		return
	}
	// Per-IP rate limit. Catches an attacker spraying many phone numbers
	// from one source.
	ip := clientIP(r)
	if ok, retryAfter, _ := h.limiter.Allow(r.Context(), "rl:ip:"+ip, h.cfg.PerIPPerHour); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, 429, "too many code requests from this network — try again later")
		return
	}

	code := generateCode()
	if err := h.rdb.Set(r.Context(), codeKey(phoneHash), code, codeTTL).Err(); err != nil {
		slog.Error("code store failed", "error", err)
		writeError(w, 500, "internal error")
		return
	}

	body := fmt.Sprintf("Your PingClaw verification code is %s. It expires in 10 minutes.", code)

	if h.sms != nil {
		if err := h.sms.Send(r.Context(), phone, body); err != nil {
			slog.Error("[PINGCLAW SMS] delivery failed", "error", err)
			writeError(w, 502, "could not send verification code")
			return
		}
		// Log the suffix so the operator can correlate without seeing
		// the full number. The code is NOT logged when SMS is on.
		suffix := "..." + phone[len(phone)-4:]
		slog.Info("[PINGCLAW SMS] delivered", "phone_suffix", suffix)
		writeJSON(w, 200, map[string]string{
			"status":  "sent",
			"message": "Verification code sent.",
		})
		return
	}

	// Dev fallback — no SMS provider configured. Log the code so the
	// operator can hand it to the user out-of-band. Phone is redacted.
	suffix := "..." + phone[len(phone)-4:]
	slog.Info("[PINGCLAW SMS] dev-mode (no provider) — code in log",
		"phone_suffix", suffix, "code", code)
	writeJSON(w, 200, map[string]string{
		"status":  "sent",
		"message": "Verification code sent — check the server log (no SMS provider configured).",
	})
}

func (h *Handler) VerifyCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PhoneNumber string `json:"phone_number"`
		Code        string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	phone := normalizePhone(req.PhoneNumber)
	code := strings.TrimSpace(req.Code)
	if phone == "" || code == "" {
		writeError(w, 401, "invalid or expired code")
		return
	}
	phoneHash := hashToken(phone)

	// GETDEL is atomic single-use: a successful verify burns the code so
	// it can't be replayed. If the key is missing (expired or already
	// used) we treat it identically to a wrong code.
	stored, err := h.rdb.GetDel(r.Context(), codeKey(phoneHash)).Result()
	if errors.Is(err, redis.Nil) || err != nil || stored != code {
		writeError(w, 401, "invalid or expired code")
		return
	}

	// The phone number is hashed before storage. We never persist the
	// plaintext number — only the SHA-256 hash, used as a stable lookup
	// key. This matches the privacy policy.
	var userID string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT user_id FROM users WHERE phone_number_hash = $1`, phoneHash).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		userID = "usr_" + uuid.New().String()[:12]
		if _, err = h.db.ExecContext(r.Context(),
			`INSERT INTO users (user_id, phone_number_hash) VALUES ($1, $2)`,
			userID, phoneHash); err != nil {
			slog.Error("user insert failed", "error", err)
			writeError(w, 500, "internal error")
			return
		}
	} else if err != nil {
		slog.Error("user lookup failed", "error", err)
		writeError(w, 500, "internal error")
		return
	} else {
		if _, err = h.db.ExecContext(r.Context(),
			`UPDATE users SET updated_at = now() WHERE user_id = $1`,
			userID); err != nil {
			slog.Error("user update failed", "error", err)
			writeError(w, 500, "internal error")
			return
		}
	}

	// Rotate the web session: delete any previous web_session tokens for
	// this user and issue a fresh one. Result: at most one active dashboard
	// session per user. The api_key and pairing_token are untouched.
	session, err := h.rotateToken(r.Context(), userID, "web_session", "ws_")
	if err != nil {
		slog.Error("issue web_session failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}

	writeJSON(w, 200, map[string]string{
		"web_session": session,
		"user_id":     userID,
	})
}

// GetMe is called by the dashboard on load to decide whether to show
// "**** • Rotate" or a "Generate" CTA for each token kind.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	var apiKeyCount, pairingCount int
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM user_tokens WHERE user_id = $1 AND kind = 'api_key'`,
		userID).Scan(&apiKeyCount)
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM user_tokens WHERE user_id = $1 AND kind = 'pairing_token'`,
		userID).Scan(&pairingCount)
	writeJSON(w, 200, map[string]any{
		"user_id":           userID,
		"has_api_key":       apiKeyCount > 0,
		"has_pairing_token": pairingCount > 0,
	})
}

// --- Auth middleware (Bearer api_key) ---

type ctxKey string

const ctxUserID ctxKey = "user_id"

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			slog.Warn("[PINGCLAW AUTH] missing bearer token", "path", r.URL.Path)
			writeError(w, 401, "missing token")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		hash := hashToken(token)

		var userID string
		err := h.db.QueryRowContext(r.Context(),
			`SELECT user_id FROM user_tokens WHERE token_hash = $1`, hash).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			tokenPreview := token
			if len(tokenPreview) > 12 {
				tokenPreview = tokenPreview[:12] + "..."
			}
			slog.Warn("[PINGCLAW AUTH] token not in db",
				"path", r.URL.Path, "token_prefix", tokenPreview)
			writeError(w, 401, "invalid token")
			return
		}
		if err != nil {
			slog.Error("auth lookup failed", "error", err)
			writeError(w, 500, "internal error")
			return
		}

		// Best-effort last-used tracking; ignore errors.
		_, _ = h.db.ExecContext(r.Context(),
			`UPDATE user_tokens SET last_used_at = now() WHERE token_hash = $1`, hash)

		r = r.WithContext(context.WithValue(r.Context(), ctxUserID, userID))
		next(w, r)
	}
}

// --- Location endpoints ---

func (h *Handler) GetLocation(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	loc, err := h.readLocation(r.Context(), userID)
	if err != nil {
		slog.Error("location lookup failed", "error", err)
		writeError(w, 500, "internal error")
		return
	}
	if loc == nil {
		writeJSON(w, 200, map[string]any{
			"status":      "no_location",
			"server_time": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	locField := map[string]any{
		"lat": loc.Lat,
		"lng": loc.Lng,
	}
	if loc.AccuracyMetres != nil {
		locField["accuracy_metres"] = *loc.AccuracyMetres
	} else {
		locField["accuracy_metres"] = nil
	}
	var activity any
	if loc.Activity != "" {
		activity = loc.Activity
	}
	writeJSON(w, 200, map[string]any{
		"status":      "ok",
		"server_time": time.Now().UTC().Format(time.RFC3339),
		"timestamp":   loc.Timestamp,
		"location":    locField,
		"activity":    activity,
	})
}

func (h *Handler) PostLocation(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	var req struct {
		Timestamp string `json:"timestamp"`
		Location  struct {
			Lat            float64 `json:"lat"`
			Lng            float64 `json:"lng"`
			AccuracyMetres float64 `json:"accuracy_metres"`
		} `json:"location"`
		Activity string `json:"activity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("[PINGCLAW LOCATION] invalid body", "user_id", userID, "error", err)
		writeError(w, 400, "invalid request body")
		return
	}
	slog.Info("[PINGCLAW LOCATION]",
		"user_id", userID, "lat", req.Location.Lat, "lng", req.Location.Lng,
		"accuracy_m", req.Location.AccuracyMetres, "activity", req.Activity)
	if req.Timestamp == "" {
		req.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	loc := cachedLocation{
		Lat:        req.Location.Lat,
		Lng:        req.Location.Lng,
		Activity:   req.Activity,
		Timestamp:  req.Timestamp,
		ReceivedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if req.Location.AccuracyMetres > 0 {
		v := req.Location.AccuracyMetres
		loc.AccuracyMetres = &v
	}
	if err := h.writeLocation(r.Context(), userID, loc); err != nil {
		slog.Error("location write failed", "error", err)
		writeError(w, 500, "failed to save location")
		return
	}

	// If the user has a webhook configured (e.g. an OpenClaw home agent),
	// forward the location update asynchronously.
	if hookURL, secret, ok := h.lookupWebhook(r.Context(), userID); ok {
		payload := map[string]any{
			"event":       "location_update",
			"user_id":     userID,
			"timestamp":   req.Timestamp,
			"server_time": time.Now().UTC().Format(time.RFC3339),
			"location": map[string]any{
				"lat":             req.Location.Lat,
				"lng":             req.Location.Lng,
				"accuracy_metres": req.Location.AccuracyMetres,
			},
			"activity": req.Activity,
		}
		go fireUserWebhook(hookURL, secret, userID, payload)
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// --- Webhook endpoints ---

func (h *Handler) lookupWebhook(ctx context.Context, userID string) (hookURL, secret string, ok bool) {
	err := h.db.QueryRowContext(ctx,
		`SELECT url, secret FROM user_webhooks WHERE user_id = $1`, userID).Scan(&hookURL, &secret)
	if err != nil || hookURL == "" {
		return "", "", false
	}
	return hookURL, secret, true
}

// fireUserWebhook POSTs the location payload to the user's configured webhook.
// Runs in a goroutine — failures are logged but never affect the inbound request.
// The receiver should verify the Authorization: Bearer header matches the
// secret it was given when the webhook was registered.
func fireUserWebhook(hookURL, secret, userID string, payload map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("[PINGCLAW WEBHOOK] marshal failed", "user_id", userID, "error", err)
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("POST", hookURL, bytes.NewReader(body))
	if err != nil {
		slog.Error("[PINGCLAW WEBHOOK] request build failed", "user_id", userID, "url", hookURL, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("[PINGCLAW WEBHOOK] POST failed", "user_id", userID, "url", hookURL, "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("[PINGCLAW WEBHOOK] delivered", "user_id", userID, "url", hookURL, "status", resp.StatusCode)
}

// PutWebhook sets (or replaces) the per-user outgoing webhook URL and
// secret. The secret is supplied by the caller (the receiver picks/generates
// it). PingClaw stores both verbatim and replays the secret as
// Authorization: Bearer on every fire.
//
//	PUT /pingclaw/webhook  { "url": "https://...", "secret": "whatever" }
func (h *Handler) PutWebhook(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	var req struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	hookURL := strings.TrimSpace(req.URL)
	secret := strings.TrimSpace(req.Secret)
	if hookURL == "" {
		writeError(w, 400, "url is required")
		return
	}
	if secret == "" {
		writeError(w, 400, "secret is required")
		return
	}
	parsed, err := url.Parse(hookURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, 400, "url must be a valid http(s) URL")
		return
	}

	if _, err = h.db.ExecContext(r.Context(),
		`INSERT INTO user_webhooks (user_id, url, secret) VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO UPDATE SET
		   url = EXCLUDED.url,
		   secret = EXCLUDED.secret,
		   updated_at = now()`,
		userID, hookURL, secret); err != nil {
		slog.Error("webhook upsert failed", "user_id", userID, "error", err)
		writeError(w, 500, "failed to save webhook")
		return
	}
	slog.Info("[PINGCLAW WEBHOOK] registered", "user_id", userID, "url", hookURL)
	writeJSON(w, 200, map[string]string{
		"status": "ok",
		"url":    hookURL,
	})
}

// GetWebhook returns the user's currently configured webhook (URL + secret).
// Secret returned plaintext because the user supplied it.
//
//	GET /pingclaw/webhook
func (h *Handler) GetWebhook(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	hookURL, secret, ok := h.lookupWebhook(r.Context(), userID)
	if !ok {
		writeJSON(w, 200, map[string]any{"url": nil, "webhook_secret": nil})
		return
	}
	writeJSON(w, 200, map[string]string{
		"url":            hookURL,
		"webhook_secret": secret,
	})
}

// TestWebhook fires a one-shot synthetic POST to the user's configured
// webhook so they can verify the receiver is reachable / authenticated.
// Always fires regardless of mode (proximity rules are skipped). Uses the
// user's last known location if there is one, otherwise the North Pole.
//
//	POST /pingclaw/webhook/test
func (h *Handler) TestWebhook(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	hookURL, secret, ok := h.lookupWebhook(r.Context(), userID)
	if !ok {
		writeError(w, 404, "no webhook configured")
		return
	}

	// Pull last known location; fall back to the North Pole.
	lat, lng := 90.0, 0.0
	if loc, err := h.readLocation(r.Context(), userID); err == nil && loc != nil {
		lat = loc.Lat
		lng = loc.Lng
	}

	payload := map[string]any{
		"event":    "webhook_test",
		"test":     true,
		"user_id":  userID,
		"location": map[string]any{"lat": lat, "lng": lng},
		"fired_at": time.Now().UTC().Format(time.RFC3339),
		"note":     "Triggered from the PingClaw dashboard.",
	}

	status, err := fireUserWebhookSync(hookURL, secret, payload)
	if err != nil {
		slog.Warn("[PINGCLAW WEBHOOK] test delivery failed", "user_id", userID, "url", hookURL, "error", err)
		writeError(w, 502, "delivery failed: "+err.Error())
		return
	}
	slog.Info("[PINGCLAW WEBHOOK] test delivered", "user_id", userID, "url", hookURL, "status", status)
	writeJSON(w, 200, map[string]any{
		"status":           "ok",
		"delivered_status": status,
		"location":         map[string]float64{"lat": lat, "lng": lng},
	})
}

// fireUserWebhookSync is the synchronous version of fireUserWebhook used
// by the test endpoint so we can return delivery status to the caller.
func fireUserWebhookSync(hookURL, secret string, payload map[string]any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest("POST", hookURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// DeleteWebhook removes the per-user outgoing webhook (URL and secret).
//
//	DELETE /pingclaw/webhook
func (h *Handler) DeleteWebhook(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM user_webhooks WHERE user_id = $1`, userID); err != nil {
		writeError(w, 500, "failed to delete webhook")
		return
	}
	slog.Info("[PINGCLAW WEBHOOK] removed", "user_id", userID)
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Token rotation ---

// rotateToken revokes ALL of the user's tokens of the given kind and
// issues a new one. Other kinds are unaffected.
func (h *Handler) rotateToken(ctx context.Context, userID, kind, prefix string) (string, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM user_tokens WHERE user_id = $1 AND kind = $2`, userID, kind); err != nil {
		return "", err
	}
	tok := generateToken(prefix)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO user_tokens (token_hash, user_id, kind, label) VALUES ($1, $2, $3, 'rotate')`,
		hashToken(tok), userID, kind); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET updated_at = now() WHERE user_id = $1`, userID); err != nil {
		return "", err
	}
	return tok, tx.Commit()
}

func (h *Handler) RotatePairingToken(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	pt, err := h.rotateToken(r.Context(), userID, "pairing_token", "pt_")
	if err != nil {
		slog.Error("rotate pairing_token failed", "user_id", userID, "error", err)
		writeError(w, 500, "rotate failed")
		return
	}
	writeJSON(w, 200, map[string]string{"pairing_token": pt})
}

func (h *Handler) RotateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	ak, err := h.rotateToken(r.Context(), userID, "api_key", "ak_")
	if err != nil {
		slog.Error("rotate api_key failed", "user_id", userID, "error", err)
		writeError(w, 500, "rotate failed")
		return
	}
	writeJSON(w, 200, map[string]string{"api_key": ak})
}

// --- Account ---

// GetMyData returns every record the server stores about the calling
// user, for transparency. Tokens are returned as hashes (which is what's
// actually on disk) — plaintext is never reconstructible.
//
//	GET /pingclaw/auth/data
func (h *Handler) GetMyData(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	type tokenRow struct {
		TokenHash  string  `json:"token_hash"`
		Kind       string  `json:"kind"`
		Label      *string `json:"label"`
		CreatedAt  string  `json:"created_at"`
		LastUsedAt *string `json:"last_used_at"`
	}
	type locationRow struct {
		Lat            float64  `json:"lat"`
		Lng            float64  `json:"lng"`
		AccuracyMetres *float64 `json:"accuracy_metres"`
		Activity       *string  `json:"activity"`
		Timestamp      string   `json:"timestamp"`
		ReceivedAt     string   `json:"received_at"`
	}
	type webhookRow struct {
		URL       string `json:"url"`
		Secret    string `json:"secret"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	type response struct {
		UserID          string       `json:"user_id"`
		PhoneNumberHash string       `json:"phone_number_hash"`
		CreatedAt       string       `json:"created_at"`
		UpdatedAt       string       `json:"updated_at"`
		Tokens          []tokenRow   `json:"tokens"`
		Location        *locationRow `json:"location"`
		Webhook         *webhookRow  `json:"webhook"`
	}

	resp := response{UserID: userID, Tokens: []tokenRow{}}

	// User row
	var userCreated, userUpdated time.Time
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT phone_number_hash, created_at, updated_at FROM users WHERE user_id = $1`,
		userID).Scan(&resp.PhoneNumberHash, &userCreated, &userUpdated); err != nil {
		slog.Error("data export: user lookup failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}
	resp.CreatedAt = userCreated.UTC().Format(time.RFC3339)
	resp.UpdatedAt = userUpdated.UTC().Format(time.RFC3339)

	// Tokens
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT token_hash, kind, label, created_at, last_used_at
		   FROM user_tokens WHERE user_id = $1 ORDER BY created_at`, userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t tokenRow
			var label sql.NullString
			var created time.Time
			var lastUsed sql.NullTime
			if err := rows.Scan(&t.TokenHash, &t.Kind, &label, &created, &lastUsed); err == nil {
				t.CreatedAt = created.UTC().Format(time.RFC3339)
				if label.Valid {
					t.Label = &label.String
				}
				if lastUsed.Valid {
					s := lastUsed.Time.UTC().Format(time.RFC3339)
					t.LastUsedAt = &s
				}
				resp.Tokens = append(resp.Tokens, t)
			}
		}
	}

	// Location — pulled from Redis (Postgres has no locations table). May
	// be nil if the user hasn't sent a location in the last 24 hours.
	if cached, err := h.readLocation(r.Context(), userID); err == nil && cached != nil {
		loc := locationRow{
			Lat:            cached.Lat,
			Lng:            cached.Lng,
			AccuracyMetres: cached.AccuracyMetres,
			Timestamp:      cached.Timestamp,
			ReceivedAt:     cached.ReceivedAt,
		}
		if cached.Activity != "" {
			a := cached.Activity
			loc.Activity = &a
		}
		resp.Location = &loc
	}

	// Webhook (optional)
	{
		var wh webhookRow
		var created, updated time.Time
		err := h.db.QueryRowContext(r.Context(),
			`SELECT url, secret, created_at, updated_at FROM user_webhooks WHERE user_id = $1`,
			userID).Scan(&wh.URL, &wh.Secret, &created, &updated)
		if err == nil {
			wh.CreatedAt = created.UTC().Format(time.RFC3339)
			wh.UpdatedAt = updated.UTC().Format(time.RFC3339)
			resp.Webhook = &wh
		}
	}

	writeJSON(w, 200, resp)
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	// Drop the Redis location cache first. Postgres delete cascades take
	// care of user_tokens + user_webhooks, but the cached location is in
	// a separate store and would otherwise linger until its 24h TTL.
	// Best-effort: log but don't fail the delete if Redis is unavailable.
	if err := h.rdb.Del(r.Context(), locationKey(userID)).Err(); err != nil {
		slog.Warn("delete: redis cache delete failed", "user_id", userID, "error", err)
	}

	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM users WHERE user_id = $1`, userID); err != nil {
		writeError(w, 500, "delete failed")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Route registration helper ---

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public auth endpoints
	mux.HandleFunc("POST /pingclaw/auth/send-code", h.SendCode)
	mux.HandleFunc("POST /pingclaw/auth/verify-code", h.VerifyCode)

	// Authenticated endpoints
	mux.HandleFunc("GET /pingclaw/auth/me", h.requireAuth(h.GetMe))
	mux.HandleFunc("GET /pingclaw/location", h.requireAuth(h.GetLocation))
	mux.HandleFunc("POST /pingclaw/location", h.requireAuth(h.PostLocation))
	mux.HandleFunc("POST /pingclaw/auth/rotate-pairing-token", h.requireAuth(h.RotatePairingToken))
	mux.HandleFunc("POST /pingclaw/auth/rotate-api-key", h.requireAuth(h.RotateAPIKey))
	mux.HandleFunc("DELETE /pingclaw/auth/account", h.requireAuth(h.DeleteAccount))
	mux.HandleFunc("GET /pingclaw/auth/data", h.requireAuth(h.GetMyData))

	// Outgoing webhook configuration (e.g. OpenClaw home agent)
	mux.HandleFunc("GET /pingclaw/webhook", h.requireAuth(h.GetWebhook))
	mux.HandleFunc("PUT /pingclaw/webhook", h.requireAuth(h.PutWebhook))
	mux.HandleFunc("DELETE /pingclaw/webhook", h.requireAuth(h.DeleteWebhook))
	mux.HandleFunc("POST /pingclaw/webhook/test", h.requireAuth(h.TestWebhook))
}

// clientIP returns the best-guess client IP. Trusts X-Forwarded-For
// when present (Digital Ocean's load balancer is on our request path
// and sets it). Strips port from r.RemoteAddr otherwise.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// XFF can be a comma-separated chain; the first entry is the
		// original client.
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

