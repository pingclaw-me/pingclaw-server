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
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" driver
	"github.com/redis/go-redis/v9"

	"github.com/pingclaw-me/pingclaw-server/internal/mdpage"
	"github.com/pingclaw-me/pingclaw-server/internal/pingclaw"
	"github.com/joho/godotenv"
)

func main() {
	debug := flag.Bool("debug", false, "Enable debug-level logging")
	flag.Parse()

	godotenv.Load()

	port := envOrDefault("PORT", "8080")
	logPath := envOrDefault("LOG_FILE", "logs/server.log")
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

	setupLogging(logPath, *debug)

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

	mux := http.NewServeMux()

	// PingClaw web dashboard + iOS app endpoints (under /pingclaw/)
	pc := pingclaw.NewHandler(db, rdb)
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

	// Static website (landing page, dashboard, css, js, icons).
	mux.Handle("/", http.FileServer(http.Dir("web")))

	slog.Info("listening", "port", port)
	if err := http.ListenAndServe(":"+port, withCORS(mux)); err != nil {
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

func setupLogging(logPath string, debug bool) {
	os.MkdirAll("logs", 0755)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Error("failed to open log file, using stdout only", "path", logPath, "error", err)
		return
	}

	multi := io.MultiWriter(os.Stdout, logFile)

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(multi, &slog.HandlerOptions{Level: level})
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
	// users — phone number stored as SHA-256 hash so the server can do
	// "is this number an existing user?" lookups without retaining the
	// plaintext number. Auth credentials live in user_tokens.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		user_id           TEXT PRIMARY KEY,
		phone_number_hash TEXT NOT NULL UNIQUE,
		created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	// (Locations are stored in Redis with a 24-hour TTL — never persisted
	// to Postgres. See internal/pingclaw/handlers.go and the LocationStore.)

	// Drop the legacy locations table if it's still around from earlier
	// builds. Idempotent; harmless on a fresh install.
	if _, err := db.Exec(`DROP TABLE IF EXISTS locations`); err != nil {
		return err
	}

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
