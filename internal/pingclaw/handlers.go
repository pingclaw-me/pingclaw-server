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
	"math"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/pingclaw-me/pingclaw-server/internal/ratelimit"
	"github.com/pingclaw-me/pingclaw-server/internal/socialauth"
)

type Handler struct {
	db          *sql.DB
	rdb         *redis.Client
	verifier    *socialauth.Verifier
	limiter     *ratelimit.Limiter // 1-hour window (sign-in)
	limiterFast *ratelimit.Limiter // 1-minute window (location)
	cfg         RateLimitConfig
	oauth       OAuthConfig
}

// RateLimitConfig sets the per-window event caps and app config.
type RateLimitConfig struct {
	PerIPPerHour          int
	LocationPostPerMinute int    // per-user POST /location cap (default 30)
	LocationGetPerMinute  int    // per-user GET /location cap (default 60)
	ChatGPTURL            string // deep link to the PingClaw custom GPT
}

func NewHandler(db *sql.DB, rdb *redis.Client, verifier *socialauth.Verifier, limiter *ratelimit.Limiter, limiterFast *ratelimit.Limiter, cfg RateLimitConfig, oauth OAuthConfig) *Handler {
	if cfg.PerIPPerHour <= 0 {
		cfg.PerIPPerHour = 10
	}
	if cfg.LocationPostPerMinute <= 0 {
		cfg.LocationPostPerMinute = 30
	}
	if cfg.LocationGetPerMinute <= 0 {
		cfg.LocationGetPerMinute = 60
	}
	return &Handler{db: db, rdb: rdb, verifier: verifier, limiter: limiter, limiterFast: limiterFast, cfg: cfg, oauth: oauth}
}

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

// webCodeTTL is how long a web-login code survives in Redis.
const webCodeTTL = 5 * time.Minute

// webCodeKey is the Redis key for a pending web-login code.
func webCodeKey(code string) string { return "webcode:" + code }

// generateWebCode returns an 8-char uppercase alphanumeric code (letters
// + digits, no ambiguous chars like 0/O/I/1).
func generateWebCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // skip 0,O,I,1
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// --- Auth endpoints ---

// SocialAuth handles sign-in via Apple or Google. The client SDKs
// (iOS AuthenticationServices, Google Sign-In, or web JS SDKs) do the
// interactive authentication and give us a JWT id_token. We verify it,
// find-or-create the user, and issue a token.
//
//	POST /pingclaw/auth/social
//	{ "provider": "apple"|"google", "id_token": "<JWT>", "client": "ios"|"web" }
func (h *Handler) SocialAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
		IDToken  string `json:"id_token"`
		Client   string `json:"client"` // "ios" → pairing_token, "web" → web_session
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	provider := socialauth.Provider(req.Provider)
	if provider != socialauth.ProviderApple && provider != socialauth.ProviderGoogle {
		writeError(w, 400, "provider must be 'apple' or 'google'")
		return
	}
	if req.IDToken == "" {
		writeError(w, 400, "id_token is required")
		return
	}

	// Per-IP rate limit.
	ip := clientIP(r)
	if ok, retryAfter, _ := h.limiter.Allow(r.Context(), "rl:ip:"+ip, h.cfg.PerIPPerHour); !ok {
		slog.Warn("[PINGCLAW RATE] sign-in rate limited", "ip", ip, "retry_after", retryAfter)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, 429, "too many sign-in attempts — try again later")
		return
	}

	// Verify the JWT with the provider's public keys.
	identity, err := h.verifier.Verify(r.Context(), provider, req.IDToken)
	if err != nil {
		slog.Warn("[PINGCLAW AUTH] social token rejected", "provider", provider, "error", err)
		writeError(w, 401, "identity verification failed")
		return
	}

	// Find or create the user.
	userID, err := h.findOrCreateSocialUser(r.Context(), identity)
	if err != nil {
		slog.Error("[PINGCLAW AUTH] user upsert failed", "error", err)
		writeError(w, 500, "internal error")
		return
	}

	// Issue the right token kind based on the calling client.
	if req.Client == "ios" || req.Client == "android" {
		pt, err := h.rotateToken(r.Context(), userID, "pairing_token", "pt_")
		if err != nil {
			slog.Error("issue pairing_token failed", "user_id", userID, "error", err)
			writeError(w, 500, "internal error")
			return
		}
		writeJSON(w, 200, map[string]string{
			"pairing_token": pt,
			"user_id":       userID,
		})
		return
	}

	// Default: web client → issue web_session.
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

// findOrCreateSocialUser looks up user_identities by (provider, sub).
// If not found, creates a new user.
func (h *Handler) findOrCreateSocialUser(ctx context.Context, id *socialauth.Identity) (string, error) {
	// Check if this exact provider+sub is already known.
	var userID string
	err := h.db.QueryRowContext(ctx,
		`SELECT user_id FROM user_identities WHERE provider = $1 AND provider_sub = $2`,
		string(id.Provider), id.Sub).Scan(&userID)
	if err == nil {
		_, _ = h.db.ExecContext(ctx, `UPDATE users SET updated_at = now() WHERE user_id = $1`, userID)
		return userID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Brand-new user.
	userID = "usr_" + uuid.New().String()[:12]
	if _, err = h.db.ExecContext(ctx,
		`INSERT INTO users (user_id) VALUES ($1)`, userID); err != nil {
		return "", err
	}
	if _, err = h.db.ExecContext(ctx,
		`INSERT INTO user_identities (provider, provider_sub, user_id) VALUES ($1, $2, $3)`,
		string(id.Provider), id.Sub, userID); err != nil {
		return "", err
	}
	slog.Info("[PINGCLAW AUTH] new user created",
		"provider", id.Provider, "user_id", userID)
	return userID, nil
}

// WebCode generates a short-lived code the user can type into the web
// dashboard to sign in there without needing social auth on the browser.
// The phone must already be authenticated.
//
//	POST /pingclaw/auth/web-code   (requireAuth)
func (h *Handler) WebCode(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	code := generateWebCode()
	if err := h.rdb.Set(r.Context(), webCodeKey(code), userID, webCodeTTL).Err(); err != nil {
		slog.Error("web code store failed", "error", err)
		writeError(w, 500, "internal error")
		return
	}

	writeJSON(w, 200, map[string]any{
		"code":       code,
		"expires_in": int(webCodeTTL.Seconds()),
	})
}

// WebLogin lets a web browser sign in by submitting a code generated
// on the phone (via WebCode).
//
//	POST /pingclaw/auth/web-login   (public)
func (h *Handler) WebLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}
	code := strings.TrimSpace(strings.ToUpper(req.Code))
	if code == "" {
		writeError(w, 401, "invalid or expired code")
		return
	}

	// Per-IP rate limit.
	ip := clientIP(r)
	if ok, retryAfter, _ := h.limiter.Allow(r.Context(), "rl:ip:"+ip, h.cfg.PerIPPerHour); !ok {
		slog.Warn("[PINGCLAW RATE] web-login rate limited", "ip", ip, "retry_after", retryAfter)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, 429, "too many attempts — try again later")
		return
	}

	// GETDEL: single-use. A used or expired code returns 401.
	userID, err := h.rdb.GetDel(r.Context(), webCodeKey(code)).Result()
	if errors.Is(err, redis.Nil) || err != nil || userID == "" {
		writeError(w, 401, "invalid or expired code")
		return
	}

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
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM user_tokens WHERE user_id = $1 AND kind = 'api_key'`,
		userID).Scan(&apiKeyCount); err != nil {
		slog.Warn("[PINGCLAW AUTH] GetMe: api_key count query failed", "user_id", userID, "error", err)
	}
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM user_tokens WHERE user_id = $1 AND kind = 'pairing_token'`,
		userID).Scan(&pairingCount); err != nil {
		slog.Warn("[PINGCLAW AUTH] GetMe: pairing_token count query failed", "user_id", userID, "error", err)
	}
	writeJSON(w, 200, map[string]any{
		"user_id":           userID,
		"has_api_key":       apiKeyCount > 0,
		"has_pairing_token": pairingCount > 0,
	})
}

// --- Auth middleware (Bearer api_key) ---

type ctxKey string

const ctxUserID ctxKey = "user_id"

const authCacheTTL = 5 * time.Minute

func authCacheKey(hash string) string { return "auth:" + hash }

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

		// Try Redis cache first.
		userID, err := h.rdb.Get(r.Context(), authCacheKey(hash)).Result()
		if err == nil && userID != "" {
			r = r.WithContext(context.WithValue(r.Context(), ctxUserID, userID))
			next(w, r)
			return
		}

		// Cache miss — fall back to Postgres.
		err = h.db.QueryRowContext(r.Context(),
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

		// Cache the token→user_id mapping in Redis.
		h.rdb.Set(r.Context(), authCacheKey(hash), userID, authCacheTTL)

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

	if ok, retryAfter, _ := h.limiterFast.Allow(r.Context(), "rl:loc:get:"+userID, h.cfg.LocationGetPerMinute); !ok {
		slog.Warn("[PINGCLAW RATE] location GET rate limited", "user_id", userID, "retry_after", retryAfter)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, 429, "too many requests — try again later")
		return
	}

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

	if ok, retryAfter, _ := h.limiterFast.Allow(r.Context(), "rl:loc:post:"+userID, h.cfg.LocationPostPerMinute); !ok {
		slog.Warn("[PINGCLAW RATE] location POST rate limited", "user_id", userID, "retry_after", retryAfter)
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, 429, "too many location updates — try again later")
		return
	}

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

	// If the user has an OpenClaw gateway destination, deliver concurrently.
	if dest, err := h.lookupOpenClawDest(r.Context(), userID); err == nil && dest != nil {
		go deliverToOpenClaw(dest, req.Location.Lat, req.Location.Lng, req.Location.AccuracyMetres, "gps")
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// --- Webhook endpoints ---

const webhookCacheTTL = 5 * time.Minute
const webhookCacheNone = "__none__" // sentinel: user has no webhook

func webhookCacheKey(userID string) string { return "wh:" + userID }

func (h *Handler) lookupWebhook(ctx context.Context, userID string) (hookURL, secret string, ok bool) {
	// Try Redis cache first.
	cached, err := h.rdb.Get(ctx, webhookCacheKey(userID)).Result()
	if err == nil {
		if cached == webhookCacheNone {
			return "", "", false
		}
		// Cached as "url\nsecret"
		parts := strings.SplitN(cached, "\n", 2)
		if len(parts) == 2 && parts[0] != "" {
			return parts[0], parts[1], true
		}
	}

	// Cache miss — fall back to Postgres.
	err = h.db.QueryRowContext(ctx,
		`SELECT url, secret FROM user_webhooks WHERE user_id = $1`, userID).Scan(&hookURL, &secret)
	if err != nil || hookURL == "" {
		h.rdb.Set(ctx, webhookCacheKey(userID), webhookCacheNone, webhookCacheTTL)
		return "", "", false
	}

	h.rdb.Set(ctx, webhookCacheKey(userID), hookURL+"\n"+secret, webhookCacheTTL)
	return hookURL, secret, true
}

func (h *Handler) invalidateWebhookCache(ctx context.Context, userID string) {
	h.rdb.Del(ctx, webhookCacheKey(userID))
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
	if isPrivateHost(parsed.Hostname()) {
		writeError(w, 400, "webhook URL must not point to a private or reserved address")
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
	h.invalidateWebhookCache(r.Context(), userID)
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
		writeError(w, 502, "webhook delivery failed")
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
	h.invalidateWebhookCache(r.Context(), userID)
	slog.Info("[PINGCLAW WEBHOOK] removed", "user_id", userID)
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// --- Token rotation ---

// rotateToken revokes ALL of the user's tokens of the given kind and
// issues a new one. Other kinds are unaffected.
func (h *Handler) rotateToken(ctx context.Context, userID, kind, prefix string) (string, error) {
	// Collect old token hashes so we can invalidate their auth cache.
	var oldHashes []string
	rows, err := h.db.QueryContext(ctx,
		`SELECT token_hash FROM user_tokens WHERE user_id = $1 AND kind = $2`, userID, kind)
	if err == nil {
		for rows.Next() {
			var hash string
			if rows.Scan(&hash) == nil {
				oldHashes = append(oldHashes, hash)
			}
		}
		rows.Close()
	}

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
	if err := tx.Commit(); err != nil {
		return "", err
	}

	// Invalidate old token cache entries.
	for _, hash := range oldHashes {
		h.rdb.Del(ctx, authCacheKey(hash))
	}
	return tok, nil
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
	type identityRow struct {
		Provider    string `json:"provider"`
		ProviderSub string `json:"provider_sub"`
		CreatedAt   string `json:"created_at"`
	}
	type openclawDestRow struct {
		DestinationID string `json:"destination_id"`
		GatewayURL    string `json:"gateway_url"`
		HookPath      string `json:"hook_path"`
		Action        string `json:"action"`
		CreatedAt     string `json:"created_at"`
		UpdatedAt     string `json:"updated_at"`
	}
	type response struct {
		UserID          string           `json:"user_id"`
		CreatedAt       string           `json:"created_at"`
		UpdatedAt       string           `json:"updated_at"`
		Identities      []identityRow    `json:"identities"`
		Tokens          []tokenRow       `json:"tokens"`
		Location        *locationRow     `json:"location"`
		Webhook         *webhookRow      `json:"webhook"`
		OpenClawGateway *openclawDestRow `json:"openclaw_gateway"`
	}

	resp := response{UserID: userID, Tokens: []tokenRow{}, Identities: []identityRow{}}

	// User row
	var userCreated, userUpdated time.Time
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT created_at, updated_at FROM users WHERE user_id = $1`,
		userID).Scan(&userCreated, &userUpdated); err != nil {
		slog.Error("data export: user lookup failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}
	resp.CreatedAt = userCreated.UTC().Format(time.RFC3339)
	resp.UpdatedAt = userUpdated.UTC().Format(time.RFC3339)

	// Identities
	idRows, idErr := h.db.QueryContext(r.Context(),
		`SELECT provider, provider_sub, created_at
		   FROM user_identities WHERE user_id = $1 ORDER BY created_at`, userID)
	if idErr == nil {
		defer idRows.Close()
		for idRows.Next() {
			var row identityRow
			var created time.Time
			if err := idRows.Scan(&row.Provider, &row.ProviderSub, &created); err == nil {
				row.CreatedAt = created.UTC().Format(time.RFC3339)
				resp.Identities = append(resp.Identities, row)
			}
		}
	}

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

	// OpenClaw gateway destination (optional)
	{
		var oc openclawDestRow
		var created, updated time.Time
		err := h.db.QueryRowContext(r.Context(),
			`SELECT destination_id, gateway_url, hook_path, action, created_at, updated_at
			   FROM user_openclaw_destinations WHERE user_id = $1`, userID).Scan(
			&oc.DestinationID, &oc.GatewayURL, &oc.HookPath, &oc.Action, &created, &updated)
		if err == nil {
			oc.CreatedAt = created.UTC().Format(time.RFC3339)
			oc.UpdatedAt = updated.UTC().Format(time.RFC3339)
			resp.OpenClawGateway = &oc
		}
	}

	writeJSON(w, 200, resp)
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	// Invalidate all auth cache entries for the user before deleting.
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT token_hash FROM user_tokens WHERE user_id = $1`, userID)
	if err == nil {
		for rows.Next() {
			var hash string
			if rows.Scan(&hash) == nil {
				h.rdb.Del(r.Context(), authCacheKey(hash))
			}
		}
		rows.Close()
	}

	// Drop Redis caches. Postgres delete cascades take care of
	// user_tokens + user_webhooks + user_openclaw_destinations,
	// but cached data would linger.
	h.invalidateWebhookCache(r.Context(), userID)
	h.invalidateOpenClawDestCache(r.Context(), userID)
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

// --- App config ---

// GetConfig returns client-facing configuration that apps fetch at
// startup. Served from env vars so links can be updated without
// redeploying the apps.
//
//	GET /pingclaw/config   (public)
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	config := map[string]any{
		"integrations": map[string]any{
			"chatgpt": map[string]any{
				"name": "ChatGPT",
				"url":  h.cfg.ChatGPTURL,
			},
		},
	}
	writeJSON(w, 200, config)
}

// --- Route registration helper ---

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public endpoints
	mux.HandleFunc("GET /pingclaw/config", h.GetConfig)
	mux.HandleFunc("POST /pingclaw/auth/social", h.SocialAuth)
	mux.HandleFunc("POST /pingclaw/auth/web-login", h.WebLogin)

	// Authenticated endpoints
	mux.HandleFunc("GET /pingclaw/auth/me", h.requireAuth(h.GetMe))
	mux.HandleFunc("POST /pingclaw/auth/web-code", h.requireAuth(h.WebCode))
	mux.HandleFunc("GET /pingclaw/location", h.requireAuth(h.GetLocation))
	mux.HandleFunc("POST /pingclaw/location", h.requireAuth(h.PostLocation))
	mux.HandleFunc("POST /pingclaw/auth/rotate-pairing-token", h.requireAuth(h.RotatePairingToken))
	mux.HandleFunc("POST /pingclaw/auth/rotate-api-key", h.requireAuth(h.RotateAPIKey))
	mux.HandleFunc("DELETE /pingclaw/auth/account", h.requireAuth(h.DeleteAccount))
	mux.HandleFunc("GET /pingclaw/auth/data", h.requireAuth(h.GetMyData))

	// OAuth 2.0 (ChatGPT GPT Actions and other OAuth consumers)
	mux.HandleFunc("GET /pingclaw/oauth/authorize", h.OAuthAuthorize)
	mux.HandleFunc("POST /pingclaw/oauth/authorize", h.OAuthAuthorize)
	mux.HandleFunc("POST /pingclaw/oauth/token", h.OAuthToken)

	// Outgoing webhook configuration (e.g. OpenClaw home agent)
	mux.HandleFunc("GET /pingclaw/webhook", h.requireAuth(h.GetWebhook))
	mux.HandleFunc("PUT /pingclaw/webhook", h.requireAuth(h.PutWebhook))
	mux.HandleFunc("DELETE /pingclaw/webhook", h.requireAuth(h.DeleteWebhook))
	mux.HandleFunc("POST /pingclaw/webhook/test", h.requireAuth(h.TestWebhook))

	// OpenClaw gateway push delivery
	mux.HandleFunc("POST /pingclaw/webhook/openclaw", h.requireAuth(h.RegisterOpenClawDest))
	mux.HandleFunc("GET /pingclaw/webhook/openclaw", h.requireAuth(h.GetOpenClawDest))
	mux.HandleFunc("DELETE /pingclaw/webhook/openclaw", h.requireAuth(h.DeleteOpenClawDest))
	mux.HandleFunc("POST /pingclaw/webhook/openclaw/test", h.requireAuth(h.TestOpenClawDest))
	mux.HandleFunc("POST /pingclaw/webhook/openclaw/send", h.requireAuth(h.SendOpenClawLocation))
}

// --- OpenClaw gateway push delivery ---

// openclawDestCacheTTL caches the per-user list of OpenClaw gateway
// destinations in Redis so every PostLocation doesn't hit Postgres.
const openclawDestCacheTTL = 5 * time.Minute
const openclawDestCacheNone = "__none__"

func openclawDestCacheKey(userID string) string { return "oc:" + userID }

// openclawGatewayDest is the stored configuration for an OpenClaw gateway
// push destination. One user can have one destination (keyed by user_id).
type openclawGatewayDest struct {
	DestinationID string `json:"destination_id"`
	GatewayURL    string `json:"gateway_url"`
	HookToken     string `json:"hook_token"`
	HookPath      string `json:"hook_path"`
	Action        string `json:"action"`
	SessionKey    string `json:"session_key,omitempty"`
}

var hookPathRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*$`)

// lookupOpenClawDest returns the user's OpenClaw gateway destination, if any.
func (h *Handler) lookupOpenClawDest(ctx context.Context, userID string) (*openclawGatewayDest, error) {
	cached, err := h.rdb.Get(ctx, openclawDestCacheKey(userID)).Result()
	if err == nil {
		if cached == openclawDestCacheNone {
			return nil, nil
		}
		var dest openclawGatewayDest
		if json.Unmarshal([]byte(cached), &dest) == nil {
			return &dest, nil
		}
	}

	var dest openclawGatewayDest
	err = h.db.QueryRowContext(ctx,
		`SELECT destination_id, gateway_url, hook_token, hook_path, action, session_key
		   FROM user_openclaw_destinations WHERE user_id = $1`, userID).Scan(
		&dest.DestinationID, &dest.GatewayURL, &dest.HookToken,
		&dest.HookPath, &dest.Action, &dest.SessionKey)
	if errors.Is(err, sql.ErrNoRows) {
		h.rdb.Set(ctx, openclawDestCacheKey(userID), openclawDestCacheNone, openclawDestCacheTTL)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	body, _ := json.Marshal(dest)
	h.rdb.Set(ctx, openclawDestCacheKey(userID), string(body), openclawDestCacheTTL)
	return &dest, nil
}

func (h *Handler) invalidateOpenClawDestCache(ctx context.Context, userID string) {
	h.rdb.Del(ctx, openclawDestCacheKey(userID))
}

// formatLocationText builds the human-readable one-line location string
// used in OpenClaw gateway hook payloads. Uses signed decimal degrees
// (the format Google Maps expects).
func formatLocationText(lat, lon, accuracyMeters float64, source string) string {
	if source == "" {
		source = "gps"
	}
	return fmt.Sprintf("Location update: %.4f, %.4f ±%dm (%s)",
		lat, lon,
		int(math.Round(accuracyMeters)),
		source)
}

// openclawDeliveryTimeout and retry delay can be tuned via env vars,
// but default to sensible values.
var (
	openclawDeliveryTimeout    = 10 * time.Second
	openclawDeliveryRetryDelay = 2 * time.Second
)

// SetOpenClawDeliveryTimeout overrides the default 10s HTTP timeout for
// OpenClaw gateway delivery.
func SetOpenClawDeliveryTimeout(d time.Duration) { openclawDeliveryTimeout = d }

// SetOpenClawDeliveryRetryDelay overrides the default 2s retry delay.
func SetOpenClawDeliveryRetryDelay(d time.Duration) { openclawDeliveryRetryDelay = d }

// deliverToOpenClaw POSTs a location update to the user's OpenClaw gateway.
// Runs in a goroutine — failures are logged but never affect the inbound request.
func deliverToOpenClaw(dest *openclawGatewayDest, lat, lon, accuracyMeters float64, source string) {
	hookURL := strings.TrimRight(dest.GatewayURL, "/") + "/hooks/" + dest.HookPath
	text := formatLocationText(lat, lon, accuracyMeters, source)

	var payload []byte
	if dest.Action == "agent" {
		payload, _ = json.Marshal(map[string]any{
			"message": text,
			"name":    "PingClaw",
			"deliver": false,
		})
	} else {
		payload, _ = json.Marshal(map[string]any{
			"text": text,
			"mode": "now",
		})
	}

	client := &http.Client{Timeout: openclawDeliveryTimeout}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(openclawDeliveryRetryDelay)
		}

		req, err := http.NewRequest("POST", hookURL, bytes.NewReader(payload))
		if err != nil {
			slog.Error("[PINGCLAW OPENCLAW] request build failed", "url", hookURL, "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+dest.HookToken)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			slog.Warn("[PINGCLAW OPENCLAW] POST failed", "url", hookURL, "attempt", attempt+1, "error", err)
			continue
		}
		resp.Body.Close()

		// Don't retry on auth failure or not found.
		if resp.StatusCode == 401 || resp.StatusCode == 404 {
			slog.Warn("[PINGCLAW OPENCLAW] delivery rejected", "url", hookURL, "status", resp.StatusCode)
			return
		}

		if resp.StatusCode < 300 {
			slog.Info("[PINGCLAW OPENCLAW] delivered", "url", hookURL, "status", resp.StatusCode)
			return
		}

		lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		slog.Warn("[PINGCLAW OPENCLAW] delivery error", "url", hookURL, "status", resp.StatusCode, "attempt", attempt+1)
	}
	if lastErr != nil {
		slog.Warn("[PINGCLAW OPENCLAW] delivery failed after retries", "url", hookURL, "error", lastErr)
	}
}

// testOpenClawDelivery sends a verification POST to the gateway and returns
// the HTTP status code. Used during registration and by the test endpoint.
func testOpenClawDelivery(gatewayURL, hookToken, hookPath string) (int, error) {
	hookURL := strings.TrimRight(gatewayURL, "/") + "/hooks/" + hookPath
	payload, _ := json.Marshal(map[string]any{
		"text": "PingClaw connected. Location updates will appear here.",
		"mode": "now",
	})

	req, err := http.NewRequest("POST", hookURL, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+hookToken)

	client := &http.Client{Timeout: openclawDeliveryTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// RegisterOpenClawDest registers (or replaces) an OpenClaw gateway
// destination for the authenticated user. Verifies the gateway is
// reachable before saving.
//
//	POST /pingclaw/webhook/openclaw
func (h *Handler) RegisterOpenClawDest(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)

	var req struct {
		GatewayURL string `json:"gateway_url"`
		HookToken  string `json:"hook_token"`
		HookPath   string `json:"hook_path"`
		Action     string `json:"action"`
		SessionKey string `json:"session_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid request body")
		return
	}

	gatewayURL := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(req.GatewayURL), "/"))
	hookToken := strings.TrimSpace(req.HookToken)
	hookPath := strings.TrimSpace(req.HookPath)
	action := strings.TrimSpace(req.Action)
	sessionKey := strings.TrimSpace(req.SessionKey)

	if gatewayURL == "" {
		writeError(w, 400, "gateway_url is required")
		return
	}
	parsed, err := url.Parse(gatewayURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeError(w, 400, "gateway_url must be a valid http(s) URL")
		return
	}
	if hookToken == "" {
		writeError(w, 400, "hook_token is required")
		return
	}
	if hookPath == "" {
		hookPath = "pingclaw"
	}
	if !hookPathRe.MatchString(hookPath) {
		writeError(w, 400, "hook_path must be alphanumeric with hyphens only, no slashes")
		return
	}
	if action == "" {
		action = "wake"
	}
	if action != "wake" && action != "agent" {
		writeError(w, 400, "action must be 'wake' or 'agent'")
		return
	}

	// Verify the gateway is reachable and the token is valid.
	status, err := testOpenClawDelivery(gatewayURL, hookToken, hookPath)
	if err != nil {
		slog.Warn("[PINGCLAW OPENCLAW] verification failed", "user_id", userID, "url", gatewayURL, "error", err)
		writeJSON(w, 422, map[string]string{
			"error":   "gateway_unreachable",
			"message": "Could not reach the OpenClaw gateway. Check the URL and ensure hooks are enabled.",
		})
		return
	}
	if status == 401 {
		writeJSON(w, 422, map[string]string{
			"error":   "gateway_auth_failed",
			"message": "The gateway rejected the hook token. Check hooks.token in your openclaw.json.",
		})
		return
	}
	if status >= 300 {
		slog.Warn("[PINGCLAW OPENCLAW] verification returned error", "user_id", userID, "status", status)
		writeJSON(w, 422, map[string]string{
			"error":   "gateway_unreachable",
			"message": fmt.Sprintf("The gateway returned HTTP %d. Check your gateway configuration.", status),
		})
		return
	}

	destID := "dest_" + uuid.New().String()[:12]

	if _, err = h.db.ExecContext(r.Context(),
		`INSERT INTO user_openclaw_destinations (destination_id, user_id, gateway_url, hook_token, hook_path, action, session_key)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (user_id) DO UPDATE SET
		   destination_id = EXCLUDED.destination_id,
		   gateway_url = EXCLUDED.gateway_url,
		   hook_token = EXCLUDED.hook_token,
		   hook_path = EXCLUDED.hook_path,
		   action = EXCLUDED.action,
		   session_key = EXCLUDED.session_key,
		   updated_at = now()`,
		destID, userID, gatewayURL, hookToken, hookPath, action, sessionKey); err != nil {
		slog.Error("[PINGCLAW OPENCLAW] upsert failed", "user_id", userID, "error", err)
		writeError(w, 500, "failed to save destination")
		return
	}
	h.invalidateOpenClawDestCache(r.Context(), userID)

	slog.Info("[PINGCLAW OPENCLAW] registered", "user_id", userID, "url", gatewayURL, "path", hookPath, "action", action)
	writeJSON(w, 201, map[string]any{
		"destination_id": destID,
		"type":           "openclaw_gateway",
		"gateway_url":    gatewayURL,
		"hook_path":      hookPath,
		"action":         action,
		"verified":       true,
	})
}

// GetOpenClawDest returns the user's configured OpenClaw gateway destination.
//
//	GET /pingclaw/webhook/openclaw
func (h *Handler) GetOpenClawDest(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	dest, err := h.lookupOpenClawDest(r.Context(), userID)
	if err != nil {
		slog.Error("[PINGCLAW OPENCLAW] lookup failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}
	if dest == nil {
		writeJSON(w, 200, map[string]any{"destination": nil})
		return
	}
	writeJSON(w, 200, map[string]any{
		"destination": map[string]any{
			"destination_id": dest.DestinationID,
			"type":           "openclaw_gateway",
			"gateway_url":    dest.GatewayURL,
			"hook_path":      dest.HookPath,
			"action":         dest.Action,
			"session_key":    dest.SessionKey,
		},
	})
}

// DeleteOpenClawDest removes the user's OpenClaw gateway destination.
//
//	DELETE /pingclaw/webhook/openclaw
func (h *Handler) DeleteOpenClawDest(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM user_openclaw_destinations WHERE user_id = $1`, userID); err != nil {
		writeError(w, 500, "failed to delete destination")
		return
	}
	h.invalidateOpenClawDestCache(r.Context(), userID)
	slog.Info("[PINGCLAW OPENCLAW] removed", "user_id", userID)
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}

// TestOpenClawDest sends a test POST to the user's OpenClaw gateway destination.
//
//	POST /pingclaw/webhook/openclaw/test
func (h *Handler) TestOpenClawDest(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	dest, err := h.lookupOpenClawDest(r.Context(), userID)
	if err != nil {
		slog.Error("[PINGCLAW OPENCLAW] lookup failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}
	if dest == nil {
		writeError(w, 404, "no OpenClaw gateway destination configured")
		return
	}

	status, err := testOpenClawDelivery(dest.GatewayURL, dest.HookToken, dest.HookPath)
	if err != nil {
		slog.Warn("[PINGCLAW OPENCLAW] test delivery failed", "user_id", userID, "error", err)
		writeJSON(w, 200, map[string]any{
			"verified":       false,
			"destination_id": dest.DestinationID,
			"error":          "gateway_unreachable",
			"message":        "Could not reach the OpenClaw gateway.",
		})
		return
	}
	if status == 401 {
		writeJSON(w, 200, map[string]any{
			"verified":       false,
			"destination_id": dest.DestinationID,
			"error":          "gateway_auth_failed",
			"message":        "The gateway rejected the hook token.",
		})
		return
	}

	verified := status < 300
	slog.Info("[PINGCLAW OPENCLAW] test delivered", "user_id", userID, "status", status, "verified", verified)
	writeJSON(w, 200, map[string]any{
		"verified":       verified,
		"destination_id": dest.DestinationID,
		"type":           "openclaw_gateway",
		"gateway_url":    dest.GatewayURL,
		"hook_path":      dest.HookPath,
		"action":         dest.Action,
	})
}

// SendOpenClawLocation reads the user's last known location from Redis
// and delivers it to the configured OpenClaw gateway synchronously,
// returning the result to the caller.
//
//	POST /pingclaw/webhook/openclaw/send
func (h *Handler) SendOpenClawLocation(w http.ResponseWriter, r *http.Request) {
	userID, _ := r.Context().Value(ctxUserID).(string)
	dest, err := h.lookupOpenClawDest(r.Context(), userID)
	if err != nil {
		slog.Error("[PINGCLAW OPENCLAW] lookup failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}
	if dest == nil {
		writeError(w, 404, "no OpenClaw gateway destination configured")
		return
	}

	loc, err := h.readLocation(r.Context(), userID)
	if err != nil {
		slog.Error("[PINGCLAW OPENCLAW] location read failed", "user_id", userID, "error", err)
		writeError(w, 500, "internal error")
		return
	}
	if loc == nil {
		writeError(w, 404, "no location data — open PingClaw on your phone first")
		return
	}

	acc := 0.0
	if loc.AccuracyMetres != nil {
		acc = *loc.AccuracyMetres
	}

	hookURL := strings.TrimRight(dest.GatewayURL, "/") + "/hooks/" + dest.HookPath
	text := formatLocationText(loc.Lat, loc.Lng, acc, "gps")

	var payload []byte
	if dest.Action == "agent" {
		payload, _ = json.Marshal(map[string]any{
			"message": text,
			"name":    "PingClaw",
			"deliver": false,
		})
	} else {
		payload, _ = json.Marshal(map[string]any{
			"text": text,
			"mode": "now",
		})
	}

	req2, err := http.NewRequest("POST", hookURL, bytes.NewReader(payload))
	if err != nil {
		writeError(w, 500, "failed to build request")
		return
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+dest.HookToken)

	client := &http.Client{Timeout: openclawDeliveryTimeout}
	resp, err := client.Do(req2)
	if err != nil {
		slog.Warn("[PINGCLAW OPENCLAW] send location failed", "user_id", userID, "error", err)
		writeJSON(w, 200, map[string]any{
			"delivered": false,
			"error":     "gateway_unreachable",
			"message":   "Could not reach the OpenClaw gateway.",
		})
		return
	}
	resp.Body.Close()

	delivered := resp.StatusCode < 300
	slog.Info("[PINGCLAW OPENCLAW] location sent", "user_id", userID, "status", resp.StatusCode, "delivered", delivered)
	writeJSON(w, 200, map[string]any{
		"delivered": delivered,
		"status":    resp.StatusCode,
		"location":  map[string]any{"lat": loc.Lat, "lng": loc.Lng},
		"text":      text,
	})
}

// isPrivateHost returns true if the hostname resolves to a private,
// loopback, or link-local address. Used to prevent SSRF via webhook
// URLs pointing at internal services.
func isPrivateHost(hostname string) bool {
	// Reject obvious local names.
	lower := strings.ToLower(hostname)
	if lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return true
	}

	ips, err := net.LookupHost(hostname)
	if err != nil {
		// If DNS fails, reject — fail closed.
		return true
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return true
		}
		// Block metadata endpoints (169.254.0.0/16 covered by IsLinkLocalUnicast)
	}
	return false
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

