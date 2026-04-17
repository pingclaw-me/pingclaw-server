-- Migration 001: PingClaw schema (PostgreSQL).
--
-- This file is the snapshot record of the database schema. The server
-- applies the equivalent CREATE TABLE statements inline at startup
-- (cmd/server/main.go), so this file is documentation rather than an
-- input — but it must stay in sync.

-- users — phone number stored as SHA-256 hash so the server can look up
-- "is this number an existing user?" without retaining the plaintext.
CREATE TABLE IF NOT EXISTS users (
    user_id           TEXT PRIMARY KEY,
    phone_number_hash TEXT NOT NULL UNIQUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- locations — DELIBERATELY NOT IN POSTGRES.
-- Location data points are stored only in Redis under the key
-- `loc:<user_id>` with a 24-hour TTL, per the privacy policy. Nothing
-- about a user's location is ever written to permanent storage.

-- user_webhooks — per-user outgoing webhook (e.g. OpenClaw home agent).
-- The secret is stored plaintext because the server replays it on every
-- outbound POST.
CREATE TABLE IF NOT EXISTS user_webhooks (
    user_id    TEXT PRIMARY KEY REFERENCES users(user_id) ON DELETE CASCADE,
    url        TEXT NOT NULL,
    secret     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- user_tokens — auth credentials. Only hashes are stored; plaintext is
-- shown to the user once at creation/rotation.
--
--   web_session   issued on sign-in, one per browser, used by the
--                 dashboard. Adding another doesn't kick existing ones.
--   api_key       one per user, created/rotated explicitly. Used by
--                 MCP agents.
--   pairing_token one per user, created/rotated explicitly. Used by
--                 the iOS app.
CREATE TABLE IF NOT EXISTS user_tokens (
    token_hash    TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    kind          TEXT NOT NULL CHECK(kind IN ('web_session','api_key','pairing_token')),
    label         TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_user_tokens_user ON user_tokens(user_id, kind);
