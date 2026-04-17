package pingclaw

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// mcpUserKey is the context key that mcpAuthMiddleware uses to attach the
// authenticated user_id to the request, so MCP tool callbacks can read it.
type mcpUserKey struct{}

// NewMCPHandler returns an HTTP handler implementing the PingClaw MCP
// server. It exposes per-user tools that read from the same data the
// dashboard does, authenticated via the user's API key (or pairing token)
// presented as a Bearer header — the same auth scheme as the rest of the
// /pingclaw/* surface.
func (h *Handler) NewMCPHandler() http.Handler {
	s := server.NewMCPServer(
		"PingClaw",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	s.AddTool(
		mcplib.NewTool("get_my_location",
			mcplib.WithDescription("Returns the user's most recent location: latitude, longitude, accuracy in metres, and when it was last updated. Use this whenever the agent needs to know where the user is."),
		),
		func(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			userID, _ := ctx.Value(mcpUserKey{}).(string)
			if userID == "" {
				return mcplib.NewToolResultError("not authenticated"), nil
			}
			return h.mcpGetMyLocation(ctx, userID), nil
		},
	)

	slog.Info("registered PingClaw MCP tools", "count", 1)

	httpHandler := server.NewStreamableHTTPServer(s)
	return h.mcpAuthMiddleware(httpHandler)
}

// mcpAuthMiddleware mirrors requireAuth — looks up the bearer in
// user_tokens (api_key, pairing_token, or web_session all work) and
// attaches the resolved user_id to the request context.
func (h *Handler) mcpAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			slog.Warn("[PINGCLAW MCP] missing bearer token", "path", r.URL.Path)
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		hash := hashToken(token)

		var userID string
		err := h.db.QueryRowContext(r.Context(),
			`SELECT user_id FROM user_tokens WHERE token_hash = $1`, hash).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			slog.Warn("[PINGCLAW MCP] token not in db")
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if err != nil {
			slog.Error("[PINGCLAW MCP] auth lookup failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Best-effort last-used tracking.
		_, _ = h.db.ExecContext(r.Context(),
			`UPDATE user_tokens SET last_used_at = now() WHERE token_hash = $1`, hash)

		ctx := context.WithValue(r.Context(), mcpUserKey{}, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// mcpGetMyLocation reads the latest location for the user from Redis and
// formats it as a small JSON blob the agent can present to the user.
// Returns no_location once the cached location expires (24h after the
// last write).
func (h *Handler) mcpGetMyLocation(ctx context.Context, userID string) *mcplib.CallToolResult {
	loc, err := h.readLocation(ctx, userID)
	if err != nil {
		slog.Error("[PINGCLAW MCP] location lookup failed", "user_id", userID, "error", err)
		return mcplib.NewToolResultError("failed to fetch location")
	}
	if loc == nil {
		return mcplib.NewToolResultText(
			`{"status":"no_location","message":"No location data yet — open PingClaw on your phone to start sharing your location."}`)
	}

	accuracyM := 0.0
	if loc.AccuracyMetres != nil {
		accuracyM = *loc.AccuracyMetres
	}

	ageStr := "unknown"
	if t, err := time.Parse(time.RFC3339, loc.Timestamp); err == nil {
		age := time.Since(t)
		switch {
		case age < 10*time.Second:
			ageStr = "just now"
		case age < time.Minute:
			ageStr = fmt.Sprintf("%ds ago", int(age.Seconds()))
		case age < time.Hour:
			ageStr = fmt.Sprintf("%dm ago", int(age.Minutes()))
		case age < 24*time.Hour:
			ageStr = fmt.Sprintf("%dh ago", int(age.Hours()))
		default:
			ageStr = fmt.Sprintf("%dd ago", int(age.Hours()/24))
		}
	}

	body, _ := json.Marshal(map[string]any{
		"status":           "ok",
		"lat":              loc.Lat,
		"lng":              loc.Lng,
		"accuracy_metres":  accuracyM,
		"last_updated":     loc.Timestamp,
		"last_updated_ago": ageStr,
	})
	return mcplib.NewToolResultText(string(body))
}
