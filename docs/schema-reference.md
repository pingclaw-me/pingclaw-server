# Schema Reference

This document is the canonical reference for PingClaw's data layout
across PostgreSQL and Redis, and for the identifier/token prefix
conventions used throughout the system. Any future service that needs
to read PingClaw data (e.g. a VendorClaw server reading user
locations from Redis) should treat this file as the contract.

---

## Token and identifier prefixes

Every user-facing identifier and credential is prefixed so you can
tell what it is at a glance — in logs, config files, database rows,
and bug reports.

| Prefix | What | Format | Example |
|---|---|---|---|
| `usr_` | User ID | `usr_` + 12 chars of UUIDv4 | `usr_6dc2d4a2-d9b` |
| `ak_` | API key | `ak_` + 32 hex chars | `ak_8288e02f0533e90d5aa4ae5cc1b0f714` |
| `pt_` | Pairing token | `pt_` + 32 hex chars | `pt_a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6` |
| `ws_` | Web session token | `ws_` + 32 hex chars | `ws_f0e1d2c3b4a5f6e7d8c9b0a1f2e3d4c5` |
| `whsec_` | Webhook secret | `whsec_` + 32 hex chars | `whsec_f96354c23af4b473cb03cb00ae7d77ae` |

### How tokens are stored

The server never stores token plaintext in the database. On
generation, the plaintext is shown to the user once (in the dashboard
or the API response), then immediately SHA-256 hashed. The hash is
stored in the `user_tokens` table and used for all subsequent lookups.

The webhook secret (`whsec_`) is the exception — it is stored in
plaintext in `user_webhooks.secret` because the server must replay
it on every outbound POST to the webhook receiver.

### How tokens are generated

```go
func generateToken(prefix string) string {
    b := make([]byte, 16)
    crypto/rand.Read(b)
    return prefix + hex.EncodeToString(b)
}
```

16 bytes of `crypto/rand` → 32 hex characters → prefixed. This gives
128 bits of entropy per token.

---

## PostgreSQL schema

Database: `pingclaw`. All tables use `TEXT` primary keys (not serial
integers).

### `users`

One row per signed-up user. Identity info lives in `user_identities`.

| Column | Type | Notes |
|---|---|---|
| `user_id` | `TEXT` PK | `usr_` + 12 chars |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |
| `updated_at` | `TIMESTAMPTZ` | Default `now()`, bumped on sign-in and token rotation |

### `user_identities`

Federated identity: one row per (provider, sub). A user can have
multiple identities (e.g. Apple + Google), linked automatically by
email when possible.

| Column | Type | Notes |
|---|---|---|
| `provider` | `TEXT` PK (composite) | `'apple'` or `'google'` |
| `provider_sub` | `TEXT` PK (composite) | The `sub` claim from the provider's JWT |
| `user_id` | `TEXT` FK → `users` | CASCADE on delete |
| `email` | `TEXT` | Cached from the token. Nil if Apple user hides email. |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |

Index: `idx_user_identities_user` on `(user_id)`.

### `user_tokens`

One row per active credential. A user can have at most one of each
kind at a time (rotation deletes the previous).

| Column | Type | Notes |
|---|---|---|
| `token_hash` | `TEXT` PK | SHA-256 of the plaintext token |
| `user_id` | `TEXT` FK → `users` | CASCADE on delete |
| `kind` | `TEXT` | `'web_session'`, `'api_key'`, or `'pairing_token'` |
| `label` | `TEXT` | How the token was issued (e.g. `'rotate'`, `'sign-in'`) |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |
| `last_used_at` | `TIMESTAMPTZ` | Updated on every authenticated request (best-effort) |

Index: `idx_user_tokens_user` on `(user_id, kind)`.

### `user_webhooks`

One row per user, if they've registered an outbound webhook.

| Column | Type | Notes |
|---|---|---|
| `user_id` | `TEXT` PK, FK → `users` | CASCADE on delete |
| `url` | `TEXT` | The receiver URL. Must be `http://` or `https://`. |
| `secret` | `TEXT` | Bearer token sent on every outbound POST. Stored plaintext (see note above). |
| `created_at` | `TIMESTAMPTZ` | Default `now()` |
| `updated_at` | `TIMESTAMPTZ` | Default `now()`, bumped on URL/secret update |

---

## Redis schema

Database: `0` (default). All keys are namespaced with a prefix and
a colon.

### `loc:<user_id>` — cached location

The most recent GPS location for a user. Written on every
`POST /pingclaw/location` from the iOS app.

| Field | Key pattern | TTL |
|---|---|---|
| Location | `loc:usr_6dc2d4a2-d9b` | **24 hours** |

Value is a JSON string:

```json
{
  "lat": 39.9312,
  "lng": -75.3610,
  "accuracy_metres": 5.69,
  "activity": "Stationary",
  "timestamp": "2026-04-16T20:27:59Z",
  "received_at": "2026-04-16T20:28:14Z"
}
```

| JSON field | Type | Notes |
|---|---|---|
| `lat` | `float64` | WGS 84 latitude |
| `lng` | `float64` | WGS 84 longitude |
| `accuracy_metres` | `*float64` | Horizontal accuracy from Core Location. Omitted if zero/unknown. |
| `activity` | `string` | Inferred from speed: `"Stationary"`, `"Walking"`, `"Cycling"`, `"In vehicle"`. May be empty. |
| `timestamp` | `string` | RFC 3339 UTC. When the phone took the GPS fix. |
| `received_at` | `string` | RFC 3339 UTC. When the server received the POST. |

**Privacy**: this is the only location data stored anywhere.
PostgreSQL has no location table. When the TTL expires (or the user
deletes their account), the key is gone and no trace remains.

**Account deletion**: `DeleteAccount` explicitly calls
`DEL loc:<user_id>` before removing the Postgres rows, so the
location doesn't linger for up to 24 hours.

### `webcode:<code>` — web login code

A short-lived code generated by the phone (`POST /pingclaw/auth/web-code`)
that the user types into the web dashboard to sign in.

| Field | Key pattern | TTL |
|---|---|---|
| Code → user_id | `webcode:ABCD1234` | **5 minutes** |

Value is the `user_id` string. Consumed atomically via `GETDEL` on
successful web login — a code can only be used once.

### `rl:ip:<ip_address>` — per-IP rate limit counter

Tracks how many sign-in attempts have been made from a given
IP address in the current 1-hour window.

| Field | Key pattern | TTL |
|---|---|---|
| Counter | `rl:ip:203.0.113.42` | **1 hour** |

Same mechanics as the phone counter. Default limit: 10 per hour.

---

## Reserved key namespaces

The following prefixes are reserved and must not be used by other
services sharing the same Redis instance:

| Prefix | Owner | Purpose |
|---|---|---|
| `loc:` | PingClaw | User location cache |
| `webcode:` | PingClaw | Web login codes (phone → web) |
| `rl:` | PingClaw | Rate limit counters |

Future services (e.g. VendorClaw) should pick their own namespace
(e.g. `vendor:`, `market:`, `sub:`) and document it here.
