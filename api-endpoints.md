# PingClaw API Endpoints

All endpoints are mounted under `/pingclaw/` on the server.

---

## Authentication

### `POST /pingclaw/auth/social` (Public)

Sign in via Apple or Google. Rate-limited per IP.

**Request:**
```json
{ "provider": "apple|google", "id_token": "<JWT>", "client": "ios|android|web" }
```

**Response (client = ios/android):**
```json
{ "pairing_token": "pt_...", "user_id": "usr_..." }
```

**Response (client = web):**
```json
{ "web_session": "ws_...", "user_id": "usr_..." }
```

| Layer | Operation |
|-------|-----------|
| Redis | `INCR rl:ip:<ip>` — rate limit check (10/hour default) |
| Server | Verify JWT via JWKS (Apple: `appleid.apple.com/auth/keys`, Google: `googleapis.com/oauth2/v3/certs`) |
| Postgres | `SELECT user_id FROM user_identities WHERE provider = $1 AND provider_sub = $2` — lookup existing identity |
| Postgres | `SELECT user_id FROM user_identities WHERE email = $1` — auto-link by email if identity not found |
| Postgres | `INSERT INTO users` + `INSERT INTO user_identities` — create new user if no match |
| Postgres | `UPDATE users SET updated_at = now()` — bump timestamp for existing users |
| Postgres | `DELETE FROM user_tokens WHERE user_id = $1 AND kind = $2` — revoke old token of this kind |
| Postgres | `INSERT INTO user_tokens (token_hash, user_id, kind)` — store SHA-256 hash of new token |

---

### `POST /pingclaw/auth/web-code` (Authenticated)

Generate a short-lived code for web dashboard sign-in from the phone.

**Request:** empty body

**Response:**
```json
{ "code": "ABCD1234", "expires_in": 300 }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache (via `requireAuth`) |
| Postgres | (cache miss only) `SELECT user_id FROM user_tokens WHERE token_hash = $1` — verify bearer token |
| Redis | (cache miss only) `SET auth:<token_hash> <user_id> EX 300` — cache token for 5 min |
| Postgres | (cache miss only) `UPDATE user_tokens SET last_used_at = now()` — track last use |
| Server | Generate 8-char alphanumeric code (excludes 0/O/I/1) |
| Redis | `SET webcode:<code> <user_id> EX 300` — store code with 5-minute TTL |

---

### `POST /pingclaw/auth/web-login` (Public)

Exchange a web code for a web session. Rate-limited per IP.

**Request:**
```json
{ "code": "ABCD1234" }
```

**Response:**
```json
{ "web_session": "ws_...", "user_id": "usr_..." }
```

| Layer | Operation |
|-------|-----------|
| Redis | `INCR rl:ip:<ip>` — rate limit check |
| Redis | `GETDEL webcode:<code>` — consume code (single-use) |
| Postgres | `DELETE FROM user_tokens WHERE user_id = $1 AND kind = 'web_session'` — revoke old web sessions |
| Postgres | `INSERT INTO user_tokens (token_hash, user_id, kind)` — store new web session hash |

---

### `GET /pingclaw/auth/me` (Authenticated)

Check which token types exist for the calling user.

**Response:**
```json
{ "user_id": "usr_...", "has_api_key": true, "has_pairing_token": false }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Postgres | `SELECT COUNT(*) FROM user_tokens WHERE user_id = $1 AND kind = 'api_key'` |
| Postgres | `SELECT COUNT(*) FROM user_tokens WHERE user_id = $1 AND kind = 'pairing_token'` |

---

### `POST /pingclaw/auth/rotate-pairing-token` (Authenticated)

Revoke and reissue the pairing token.

**Response:**
```json
{ "pairing_token": "pt_..." }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Postgres | `SELECT token_hash FROM user_tokens WHERE user_id = $1 AND kind = 'pairing_token'` — collect old hashes |
| Postgres | `BEGIN` transaction |
| Postgres | `DELETE FROM user_tokens WHERE user_id = $1 AND kind = 'pairing_token'` |
| Postgres | `INSERT INTO user_tokens (token_hash, user_id, kind)` — new token |
| Postgres | `UPDATE users SET updated_at = now()` |
| Postgres | `COMMIT` |
| Redis | `DEL auth:<old_hash>` — invalidate old token cache entries |

---

### `POST /pingclaw/auth/rotate-api-key` (Authenticated)

Revoke and reissue the API key.

**Response:**
```json
{ "api_key": "ak_..." }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Postgres | `SELECT token_hash FROM user_tokens WHERE user_id = $1 AND kind = 'api_key'` — collect old hashes |
| Postgres | `BEGIN` transaction |
| Postgres | `DELETE FROM user_tokens WHERE user_id = $1 AND kind = 'api_key'` |
| Postgres | `INSERT INTO user_tokens (token_hash, user_id, kind)` — new token |
| Postgres | `UPDATE users SET updated_at = now()` |
| Postgres | `COMMIT` |
| Redis | `DEL auth:<old_hash>` — invalidate old token cache entries |

---

## Location

### `GET /pingclaw/location` (Authenticated)

Get the user's most recent cached location. **Redis-only on cache hit (zero Postgres queries).**

**Response (location exists):**
```json
{
  "status": "ok",
  "server_time": "2026-04-18T20:00:00Z",
  "timestamp": "2026-04-18T19:59:50Z",
  "location": { "lat": 39.93, "lng": -75.36, "accuracy_metres": 5.69 },
  "activity": "Stationary"
}
```

**Response (no location):**
```json
{ "status": "no_location", "server_time": "2026-04-18T20:00:00Z" }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Redis | `GET loc:<user_id>` — read cached location JSON |

---

### `POST /pingclaw/location` (Authenticated)

Push a location update from a mobile app. **Redis-only on cache hit (zero Postgres queries).**

**Request:**
```json
{
  "timestamp": "2026-04-18T19:59:50Z",
  "location": { "lat": 39.93, "lng": -75.36, "accuracy_metres": 5.69 },
  "activity": "Stationary"
}
```

**Response:**
```json
{ "status": "ok" }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Redis | `SET loc:<user_id> <JSON> EX 86400` — cache location with 24-hour TTL |
| Redis | `GET wh:<user_id>` — check webhook cache |
| Postgres | (cache miss only) `SELECT url, secret FROM user_webhooks WHERE user_id = $1` — lookup webhook + cache result |
| Redis | (cache miss only) `SET wh:<user_id> <url\nsecret> EX 300` — cache webhook (or `__none__` sentinel) |
| Server | If webhook exists: fire async `POST` to webhook URL with `Authorization: Bearer <secret>` (goroutine, does not block response) |

---

## Webhook

### `GET /pingclaw/webhook` (Authenticated)

Get current webhook configuration.

**Response (configured):**
```json
{ "url": "https://...", "webhook_secret": "whsec_..." }
```

**Response (none):**
```json
{ "url": null, "webhook_secret": null }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Redis | `GET wh:<user_id>` — check webhook cache |
| Postgres | (cache miss only) `SELECT url, secret FROM user_webhooks WHERE user_id = $1` + cache result |

---

### `PUT /pingclaw/webhook` (Authenticated)

Register or update the outgoing webhook.

**Request:**
```json
{ "url": "https://...", "secret": "whsec_..." }
```

**Response:**
```json
{ "status": "ok", "url": "https://..." }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Server | Validate URL (must be http/https with host) |
| Postgres | `INSERT INTO user_webhooks ... ON CONFLICT (user_id) DO UPDATE SET url, secret, updated_at` — upsert |
| Redis | `DEL wh:<user_id>` — invalidate webhook cache |

---

### `DELETE /pingclaw/webhook` (Authenticated)

Remove the webhook.

**Response:**
```json
{ "status": "deleted" }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Postgres | `DELETE FROM user_webhooks WHERE user_id = $1` |
| Redis | `DEL wh:<user_id>` — invalidate webhook cache |

---

### `POST /pingclaw/webhook/test` (Authenticated)

Fire a test POST to the configured webhook.

**Response (success):**
```json
{ "status": "ok", "delivered_status": 200, "location": { "lat": 39.93, "lng": -75.36 } }
```

**Response (delivery failed):**
```json
{ "error": "delivery failed: ..." }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Redis | `GET wh:<user_id>` — check webhook cache |
| Postgres | (cache miss only) `SELECT url, secret FROM user_webhooks WHERE user_id = $1` + cache result |
| Redis | `GET loc:<user_id>` — read last known location (falls back to 90.0, 0.0) |
| Server | Synchronous `POST` to webhook URL with 5-second timeout, `Authorization: Bearer <secret>` |

---

## Account & Data

### `GET /pingclaw/auth/data` (Authenticated)

Full data export (GDPR-style transparency).

**Response:**
```json
{
  "user_id": "usr_...",
  "created_at": "...",
  "updated_at": "...",
  "identities": [{ "provider": "google", "provider_sub": "...", "email": "...", "created_at": "..." }],
  "tokens": [{ "token_hash": "sha256...", "kind": "pairing_token", "label": "rotate", "created_at": "...", "last_used_at": "..." }],
  "location": { "lat": 39.93, "lng": -75.36, "accuracy_metres": 5.69, "activity": "Stationary", "timestamp": "...", "received_at": "..." },
  "webhook": { "url": "...", "secret": "...", "created_at": "...", "updated_at": "..." }
}
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Postgres | `SELECT created_at, updated_at FROM users WHERE user_id = $1` |
| Postgres | `SELECT provider, provider_sub, email, created_at FROM user_identities WHERE user_id = $1` |
| Postgres | `SELECT token_hash, kind, label, created_at, last_used_at FROM user_tokens WHERE user_id = $1` |
| Redis | `GET loc:<user_id>` — cached location (may be null) |
| Postgres | `SELECT url, secret, created_at, updated_at FROM user_webhooks WHERE user_id = $1` |

---

### `DELETE /pingclaw/auth/account` (Authenticated)

Permanently delete the user's account and all data.

**Response:**
```json
{ "status": "deleted" }
```

| Layer | Operation |
|-------|-----------|
| Redis | `GET auth:<token_hash>` — check auth cache |
| Postgres | (cache miss only) Verify bearer token + cache result |
| Postgres | `SELECT token_hash FROM user_tokens WHERE user_id = $1` — collect all token hashes |
| Redis | `DEL auth:<hash>` — invalidate auth cache for each token |
| Redis | `DEL wh:<user_id>` — invalidate webhook cache |
| Redis | `DEL loc:<user_id>` — remove cached location |
| Postgres | `DELETE FROM users WHERE user_id = $1` — cascading delete removes `user_identities`, `user_tokens`, `user_webhooks` |

---

## OAuth 2.0 (ChatGPT GPT Actions)

### `GET /pingclaw/oauth/authorize` (Public, requires web_session cookie)

OAuth 2.0 authorization endpoint. ChatGPT redirects the user here.

**Query params:** `client_id`, `redirect_uri`, `response_type=code`, `state`

**Behavior:**
- If user has `web_session` cookie: renders approval page
- If not signed in: renders "sign in first" page

| Layer | Operation |
|-------|-----------|
| Server | Validate `client_id` matches `OAUTH_CLIENT_ID` env var |
| Postgres | `SELECT user_id FROM user_tokens WHERE token_hash = $1` — verify web_session cookie |
| Server | Render HTML template from `web/oauth/authorize.html` |

---

### `POST /pingclaw/oauth/authorize` (Public, requires web_session cookie)

User clicks "Approve" on the authorization page.

**Behavior:** Generates auth code, redirects to `redirect_uri?code=...&state=...`

| Layer | Operation |
|-------|-----------|
| Postgres | Verify web_session cookie |
| Server | Generate 8-char auth code |
| Redis | `SET oauth:code:<code> <JSON{user_id, redirect_uri, client_id}> EX 300` — 5-minute TTL |
| Server | HTTP 302 redirect to `redirect_uri?code=<code>&state=<state>` |

---

### `POST /pingclaw/oauth/token` (Public, server-to-server)

Exchange auth code for access token. Called by ChatGPT.

**Request (form-encoded or JSON):**
```
grant_type=authorization_code&code=...&client_id=...&client_secret=...&redirect_uri=...
```

**Response:**
```json
{ "access_token": "ak_...", "token_type": "Bearer" }
```

| Layer | Operation |
|-------|-----------|
| Server | Validate `client_id` and `client_secret` against env vars |
| Redis | `GETDEL oauth:code:<code>` — consume auth code (single-use) |
| Server | Verify `client_id` and `redirect_uri` match what was stored with the code |
| Postgres | `DELETE FROM user_tokens WHERE user_id = $1 AND kind = 'api_key'` — revoke old API key |
| Postgres | `INSERT INTO user_tokens (token_hash, user_id, kind)` — issue new API key |

---

## Auth Middleware: `requireAuth`

All authenticated endpoints pass through this middleware. **Redis-cached — Postgres is only hit on cache miss.**

| Layer | Operation |
|-------|-----------|
| Server | Extract `Bearer <token>` from `Authorization` header |
| Server | Compute `SHA-256(token)` |
| Redis | `GET auth:<token_hash>` — check cache |
| | **Cache hit:** inject `user_id` into request context, skip Postgres |
| Postgres | (cache miss) `SELECT user_id FROM user_tokens WHERE token_hash = $1` — lookup by hash |
| Redis | (cache miss) `SET auth:<token_hash> <user_id> EX 300` — cache for 5 min |
| Postgres | (cache miss) `UPDATE user_tokens SET last_used_at = now()` — best-effort tracking |
| Server | Inject `user_id` into request context |

**Token types accepted:** `pairing_token` (pt_), `web_session` (ws_), `api_key` (ak_) — all stored as SHA-256 hashes in the same `user_tokens` table.

**Cache invalidation:** `auth:<hash>` entries are explicitly deleted on token rotation and account deletion. Stale entries also expire via 5-minute TTL as a safety net.

---

## Redis Key Reference

| Key Pattern | Value | TTL | Used By |
|-------------|-------|-----|---------|
| `auth:<token_hash>` | user_id | 5 minutes | requireAuth (all authenticated endpoints) |
| `loc:<user_id>` | JSON (lat, lng, accuracy, activity, timestamp, received_at) | 24 hours | PostLocation, GetLocation, GetMyData, TestWebhook |
| `wh:<user_id>` | `url\nsecret` or `__none__` sentinel | 5 minutes | lookupWebhook (PostLocation, GetWebhook, TestWebhook) |
| `webcode:<code>` | user_id | 5 minutes | WebCode, WebLogin |
| `oauth:code:<code>` | JSON (user_id, redirect_uri, client_id) | 5 minutes | OAuthAuthorize, OAuthToken |
| `rl:ip:<ip>` | request count | 1 hour | SocialAuth, WebLogin (rate limiting) |

### Hot Path Performance

After initial cache warm-up, `POST /pingclaw/location` and `GET /pingclaw/location` hit **only Redis** — zero Postgres queries. This is the most frequently called path (every location update from every active device).

## Postgres Table Reference

| Table | Primary Key | Cascades From |
|-------|-------------|---------------|
| `users` | `user_id` | — |
| `user_identities` | `(provider, provider_sub)` | `users.user_id` ON DELETE CASCADE |
| `user_tokens` | `token_hash` | `users.user_id` ON DELETE CASCADE |
| `user_webhooks` | `user_id` | `users.user_id` ON DELETE CASCADE |
