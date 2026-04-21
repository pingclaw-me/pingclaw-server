// pingclaw-server hosts everything PingClaw needs:
//   - REST/JSON API used by the iOS PingClaw App and the web dashboard
//     (under /pingclaw/*)
//   - Per-user MCP server at /pingclaw/mcp authenticated with the user's
//     API key
//   - Outbound webhook firing whenever a phone reports a new location
//   - Static website at pingclaw.me (landing page, dashboard, privacy,
//     terms, setup help)
//
// One Go binary, talks to PostgreSQL.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver
	"github.com/redis/go-redis/v9"

	"github.com/pingclaw-me/pingclaw-server/internal/mdpage"
	"github.com/pingclaw-me/pingclaw-server/internal/pingclaw"
	"github.com/pingclaw-me/pingclaw-server/internal/ratelimit"
	"github.com/pingclaw-me/pingclaw-server/internal/socialauth"
	"github.com/joho/godotenv"
)

func main() {
	debug := flag.Bool("debug", false, "Enable debug-level logging")
	flag.Parse()

	godotenv.Load()

	port := envOrDefault("PORT", "8080")
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL not set — supply a PostgreSQL DSN (e.g. postgres://user:pass@host:5432/dbname)")
		os.Exit(1)
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		slog.Error("REDIS_URL not set — supply a Redis DSN (e.g. redis://localhost:6379)")
		os.Exit(1)
	}

	setupLogging(*debug)

	db, err := initDB(dsn)
	if err != nil {
		slog.Error("database init failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	slog.Info("database initialised")

	rdb, err := initRedis(redisURL)
	if err != nil {
		slog.Error("redis init failed", "error", err)
		os.Exit(1)
	}
	defer rdb.Close()
	slog.Info("redis initialised")

	// Social auth (Apple + Google). Tokens can arrive from iOS or web,
	// each with a different audience (client ID), so we accept both.
	appleBundleID := envOrDefault("APPLE_BUNDLE_ID", "me.pingclaw.app")
	appleAudiences := strings.Split(appleBundleID, ",")
	for i := range appleAudiences {
		appleAudiences[i] = strings.TrimSpace(appleAudiences[i])
	}
	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	googleIOSClientID := os.Getenv("GOOGLE_IOS_CLIENT_ID")
	verifier := socialauth.New(
		appleAudiences,
		[]string{googleClientID, googleIOSClientID},
	)
	slog.Info("social auth initialised",
		"apple_bundle_id", appleBundleID,
		"google_client_id_set", googleClientID != "",
		"google_ios_client_id_set", googleIOSClientID != "")

	limiter := ratelimit.New(rdb, time.Hour)
	limiterFast := ratelimit.New(rdb, time.Minute)
	rlConfig := pingclaw.RateLimitConfig{
		PerIPPerHour:          envInt("RATE_LIMIT_IP_PER_HOUR", 10),
		LocationPostPerMinute: envInt("RATE_LIMIT_LOC_POST_PER_MIN", 30),
		LocationGetPerMinute:  envInt("RATE_LIMIT_LOC_GET_PER_MIN", 60),
		ChatGPTURL:            envOrDefault("CHATGPT_GPT_URL", ""),
	}
	oauthConfig := pingclaw.OAuthConfig{
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
	}
	if oauthConfig.ClientID != "" {
		slog.Info("oauth enabled", "client_id", oauthConfig.ClientID)
	}

	// OpenClaw delivery tuning (optional env vars).
	if v := envInt("OPENCLAW_DELIVERY_TIMEOUT_SECONDS", 0); v > 0 {
		pingclaw.SetOpenClawDeliveryTimeout(time.Duration(v) * time.Second)
	}
	if v := envInt("OPENCLAW_DELIVERY_RETRY_DELAY_SECONDS", 0); v > 0 {
		pingclaw.SetOpenClawDeliveryRetryDelay(time.Duration(v) * time.Second)
	}

	mux := http.NewServeMux()

	// PingClaw web dashboard + iOS app endpoints (under /pingclaw/)
	pc := pingclaw.NewHandler(db, rdb, verifier, limiter, limiterFast, rlConfig, oauthConfig)
	pc.RegisterRoutes(mux)

	// PingClaw MCP server — per-user, authenticated by the user's API key
	// (or pairing token). Tools return data scoped to the calling user.
	pcMCP := pc.NewMCPHandler()
	mux.Handle("/pingclaw/mcp", pcMCP)
	mux.Handle("/pingclaw/mcp/", pcMCP)

	// Markdown-rendered prose pages: privacy policy + terms of service.
	// Each combines a content.md with an index.html shell. Mounted before
	// the static fallback so the bare directory URLs don't hit http.FileServer.
	privacyHandler := mdpage.NewHandler(
		"web/privacypolicy/index.html",
		"web/privacypolicy/content.md",
	)
	mux.Handle("/privacypolicy", privacyHandler)
	mux.Handle("/privacypolicy/", privacyHandler)

	termsHandler := mdpage.NewHandler(
		"web/termsofservice/index.html",
		"web/termsofservice/content.md",
	)
	mux.Handle("/termsofservice", termsHandler)
	mux.Handle("/termsofservice/", termsHandler)

	// Setup sub-sections — each rendered as a markdown fragment and
	// injected into the dashboard's Setup card on demand.
	mux.Handle("/setup/ios.html", mdpage.NewFragmentHandler("web/setup/ios.md"))
	mux.Handle("/setup/chatgpt.html", mdpage.NewFragmentHandler("web/setup/chatgpt.md"))
	mux.Handle("/setup/mcp.html", mdpage.NewFragmentHandler("web/setup/mcp.md"))
	mux.Handle("/setup/openclaw.html", mdpage.NewFragmentHandler("web/setup/openclaw.md"))

	// Static website (landing page, dashboard, css, js, icons).
	mux.Handle("/", http.FileServer(http.Dir("web")))

	slog.Info("listening", "port", port)
	if err := http.ListenAndServe(":"+port, withMaxBody(withCORS(mux))); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

// withCORS adds permissive CORS headers and short-circuits OPTIONS
// pre-flights. The dashboard JS and MCP clients both need this.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id")
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withMaxBody limits request body size to prevent DoS via oversized payloads.
func withMaxBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1 MB limit — generous for JSON payloads, blocks abuse.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r)
	})
}

func setupLogging(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
}

// initDB connects to PostgreSQL and applies the PingClaw schema. Tables
// are created with IF NOT EXISTS so this is idempotent.
//
// We retry the initial Connect() loop because in a docker-compose
// world the Postgres container may not have finished starting up by
// the time the server boots.
func initDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		if err := db.PingContext(ctx); err == nil {
			break
		} else if ctx.Err() != nil {
			return nil, err
		}
		slog.Info("database not ready yet — retrying in 1s")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}

	if err := applySchema(db); err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	return db, nil
}

func applySchema(db *sql.DB) error {
	// users — the core identity table. No phone number, no email — just
	// the user_id. Social identities live in user_identities.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		user_id    TEXT PRIMARY KEY,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	// user_identities — federated identity: one row per (provider, sub).
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_identities (
		provider      TEXT NOT NULL,
		provider_sub  TEXT NOT NULL,
		user_id       TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (provider, provider_sub)
	)`); err != nil {
		return err
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities(user_id)`)

	// Clean up legacy columns/tables from earlier schema versions.
	db.Exec(`DROP TABLE IF EXISTS locations`)
	db.Exec(`ALTER TABLE users DROP COLUMN IF EXISTS phone_number_hash`)
	db.Exec(`ALTER TABLE user_identities DROP COLUMN IF EXISTS email`)

	// user_webhooks — per-user outgoing webhook (e.g. OpenClaw home agent).
	// `secret` is the bearer PingClaw replays on outbound POSTs. Stored
	// plaintext because the server itself uses it on every fire.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_webhooks (
		user_id    TEXT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
		url        TEXT NOT NULL,
		secret     TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	// user_openclaw_destinations — per-user OpenClaw gateway push destination.
	// One destination per user. The hook_token is stored plaintext because the
	// server replays it as Authorization: Bearer on every push.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_openclaw_destinations (
		destination_id TEXT NOT NULL,
		user_id        TEXT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
		gateway_url    TEXT NOT NULL,
		hook_token     TEXT NOT NULL,
		hook_path      TEXT NOT NULL DEFAULT 'pingclaw',
		action         TEXT NOT NULL DEFAULT 'wake' CHECK(action IN ('wake','agent')),
		session_key    TEXT NOT NULL DEFAULT '',
		created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	// user_tokens — auth credentials. Only hashes are stored; plaintext is
	// shown to the user once at creation/rotation.
	//
	//   web_session   issued on sign-in, one per browser, used by the
	//                 dashboard. Adding another doesn't kick existing ones.
	//   api_key       one per user, created/rotated explicitly. Used by
	//                 MCP agents.
	//   pairing_token one per user, created/rotated explicitly. Used by
	//                 the iOS app.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS user_tokens (
		token_hash    TEXT PRIMARY KEY,
		user_id       TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		kind          TEXT NOT NULL CHECK(kind IN ('web_session','api_key','pairing_token')),
		label         TEXT,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_used_at  TIMESTAMPTZ
	)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_user_tokens_user ON user_tokens(user_id, kind)`); err != nil {
		return err
	}

	return nil
}

// initRedis parses the REDIS_URL and connects, retrying for a short
// window so docker-compose can start the server before Redis is fully
// up.
func initRedis(redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for {
		if err := client.Ping(ctx).Err(); err == nil {
			return client, nil
		} else if ctx.Err() != nil {
			return nil, err
		}
		slog.Info("redis not ready yet — retrying in 1s")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
