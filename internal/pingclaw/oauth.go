package pingclaw

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// OAuthConfig holds the client credentials for the ChatGPT GPT Action
// (or any other OAuth consumer). Set via env vars.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
}

const oauthCodeTTL = 5 * time.Minute

func oauthCodeKey(code string) string { return "oauth:code:" + code }

type oauthCodeData struct {
	UserID      string `json:"user_id"`
	RedirectURI string `json:"redirect_uri"`
	ClientID    string `json:"client_id"`
}

// OAuthAuthorize is the OAuth 2.0 authorization endpoint. ChatGPT
// redirects the user here; if they're signed in (web_session cookie),
// they see an approval page. On approve, PingClaw generates an auth
// code and redirects back to ChatGPT's callback URL.
//
//	GET /pingclaw/oauth/authorize?client_id=...&redirect_uri=...&response_type=code&state=...
func (h *Handler) OAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	redirectURI := r.URL.Query().Get("redirect_uri")
	state := r.URL.Query().Get("state")
	responseType := r.URL.Query().Get("response_type")

	if h.oauth.ClientID == "" {
		http.Error(w, "OAuth not configured on this server", http.StatusServiceUnavailable)
		return
	}
	if clientID != h.oauth.ClientID {
		http.Error(w, "invalid client_id", http.StatusBadRequest)
		return
	}
	if responseType != "code" {
		http.Error(w, "unsupported response_type (must be 'code')", http.StatusBadRequest)
		return
	}
	if redirectURI == "" {
		http.Error(w, "redirect_uri is required", http.StatusBadRequest)
		return
	}

	// Check for a signed-in user via the web_session cookie.
	userID := h.getUserFromCookie(r)
	if userID == "" {
		// Not signed in — show a page telling them to sign in first.
		h.serveOAuthPage(w, map[string]any{
			"SignedIn":    false,
			"ClientID":   clientID,
			"RedirectURI": redirectURI,
			"State":       state,
			"ServerURL":   serverURL(r),
		})
		return
	}

	// POST = user clicked Approve.
	if r.Method == http.MethodPost {
		code := generateWebCode()
		data, _ := json.Marshal(oauthCodeData{
			UserID:      userID,
			RedirectURI: redirectURI,
			ClientID:    clientID,
		})
		if err := h.rdb.Set(r.Context(), oauthCodeKey(code), data, oauthCodeTTL).Err(); err != nil {
			slog.Error("[OAUTH] code store failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		slog.Info("[OAUTH] auth code issued", "user_id", userID)

		sep := "?"
		if strings.Contains(redirectURI, "?") {
			sep = "&"
		}
		target := fmt.Sprintf("%s%scode=%s&state=%s", redirectURI, sep, code, state)
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	// GET = show the approval page.
	h.serveOAuthPage(w, map[string]any{
		"SignedIn":    true,
		"ClientID":   clientID,
		"RedirectURI": redirectURI,
		"State":       state,
	})
}

// OAuthToken is the OAuth 2.0 token endpoint. ChatGPT calls this
// server-to-server to exchange an auth code for an access token.
//
//	POST /pingclaw/oauth/token
//	grant_type=authorization_code&code=...&client_id=...&client_secret=...&redirect_uri=...
func (h *Handler) OAuthToken(w http.ResponseWriter, r *http.Request) {
	// ChatGPT may send form-encoded or JSON. Handle both.
	contentType := r.Header.Get("Content-Type")
	var grantType, code, clientID, clientSecret, redirectURI string

	if strings.Contains(contentType, "application/json") {
		var body struct {
			GrantType    string `json:"grant_type"`
			Code         string `json:"code"`
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
			RedirectURI  string `json:"redirect_uri"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, 400, "invalid request body")
			return
		}
		grantType, code, clientID, clientSecret, redirectURI =
			body.GrantType, body.Code, body.ClientID, body.ClientSecret, body.RedirectURI
	} else {
		r.ParseForm()
		grantType = r.FormValue("grant_type")
		code = r.FormValue("code")
		clientID = r.FormValue("client_id")
		clientSecret = r.FormValue("client_secret")
		redirectURI = r.FormValue("redirect_uri")
	}

	if grantType != "authorization_code" {
		writeError(w, 400, "unsupported grant_type")
		return
	}
	if clientID != h.oauth.ClientID || clientSecret != h.oauth.ClientSecret {
		writeError(w, 401, "invalid client credentials")
		return
	}

	// Look up and consume the auth code (single-use).
	raw, err := h.rdb.GetDel(r.Context(), oauthCodeKey(code)).Result()
	if err != nil || raw == "" {
		writeError(w, 400, "invalid or expired code")
		return
	}
	var stored oauthCodeData
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		writeError(w, 500, "internal error")
		return
	}
	if stored.ClientID != clientID || stored.RedirectURI != redirectURI {
		writeError(w, 400, "code was issued for a different client or redirect_uri")
		return
	}

	// Issue an api_key for this user.
	apiKey, err := h.rotateToken(r.Context(), stored.UserID, "api_key", "ak_")
	if err != nil {
		slog.Error("[OAUTH] token issue failed", "user_id", stored.UserID, "error", err)
		writeError(w, 500, "failed to issue token")
		return
	}
	slog.Info("[OAUTH] access token issued", "user_id", stored.UserID)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token": apiKey,
		"token_type":   "Bearer",
	})
}

// getUserFromCookie reads the web_session cookie and returns the
// user_id if the session is valid. Returns "" if not signed in.
func (h *Handler) getUserFromCookie(r *http.Request) string {
	cookie, err := r.Cookie("web_session")
	if err != nil || cookie.Value == "" {
		return ""
	}
	hash := hashToken(cookie.Value)
	var userID string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT user_id FROM user_tokens WHERE token_hash = $1`, hash).Scan(&userID)
	if err != nil {
		return ""
	}
	return userID
}

// serveOAuthPage renders the OAuth approval page.
func (h *Handler) serveOAuthPage(w http.ResponseWriter, data map[string]any) {
	tmplBytes, err := os.ReadFile("web/oauth/authorize.html")
	if err != nil {
		slog.Error("[OAUTH] template read failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpl, err := template.New("oauth").Parse(string(tmplBytes))
	if err != nil {
		slog.Error("[OAUTH] template parse failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl.Execute(w, data)
}

func serverURL(r *http.Request) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}
