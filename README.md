# pingclaw-server

The Go server that powers [PingClaw](https://pingclaw.me) — a quiet
location utility that lets your AI assistant know where you are when it
needs to.

One Go binary backed by PostgreSQL (identity + tokens + webhooks) and
Redis (ephemeral 24h location cache). Hosts:

- **iOS / web auth + location API** under `/pingclaw/*` — used by the
  PingClaw iOS app and the web dashboard.
- **Per-user MCP server** at `/pingclaw/mcp` — exposes `get_my_location`
  to MCP clients (Claude Code, Claude Desktop via `mcp-remote`, VS Code,
  Cursor, Windsurf, Zed). Each call is scoped to the user whose API key
  is in the `Authorization` header.
- **Outbound webhook firing** — when the phone reports a new location,
  POSTs to a per-user URL (e.g. an OpenClaw home agent) with a bearer
  secret the receiver verifies.
- **Marketing site + dashboard** at `/` — landing page, sign-in flow,
  credential management, MCP config snippets, privacy policy, terms.

## Run it

The simplest path is the docker-compose stack (server + Postgres + Redis):

```bash
cp .env.example .env             # defaults are fine for local
docker compose up --build
```

Then open `http://localhost:8090/` (the docker-compose stack maps the
server to host port 8090 to avoid conflicting with anything you might
already have on 8080). Sign in with any phone number — the verification
code is printed to the server log, there is no SMS provider configured.
Generate a Pairing Token from the dashboard and paste it into the iOS
app.

To run the binary directly against a local Postgres:

```bash
export DATABASE_URL=postgres://localhost/pingclaw
go run ./cmd/server
```

## Layout

```
cmd/
  server/         the HTTP server entry point
  openclaw-mock/  small CLI that pretends to be an OpenClaw receiver,
                  for testing webhook delivery end-to-end
internal/
  pingclaw/       all PingClaw HTTP handlers + MCP tools
  mdpage/         markdown→HTML rendering for prose pages and fragments
db/migrations/    snapshot of the schema (the server applies equivalent
                  CREATE TABLE statements at startup)
web/              static website + dashboard (HTML/CSS/JS, prose markdown)
Dockerfile, docker-compose.yml — local + production deploy
```

## Endpoints

PingClaw API (used by the iOS app and the dashboard JS):

| Method | Path | Notes |
|---|---|---|
| POST | `/pingclaw/auth/social` | verify Apple/Google identity token, issue `pairing_token` (iOS) or `web_session` (web) |
| POST | `/pingclaw/auth/web-login` | exchange a phone-generated code for a `web_session` |
| POST | `/pingclaw/auth/web-code` | (auth'd) generate an 8-char code the user types into the web dashboard |
| GET  | `/pingclaw/auth/me` | which token kinds the user has |
| GET  | `/pingclaw/auth/data` | full data export (transparency view) |
| POST | `/pingclaw/auth/rotate-api-key` | mint/rotate the `api_key` |
| POST | `/pingclaw/auth/rotate-pairing-token` | mint/rotate the `pairing_token` |
| DELETE | `/pingclaw/auth/account` | delete the user and all their data |
| GET / POST | `/pingclaw/location` | read or write the user's last known location |
| GET / PUT / DELETE | `/pingclaw/webhook` | manage outbound webhook URL + secret |
| POST | `/pingclaw/webhook/test` | fire a synthetic POST to the configured webhook |
| POST / GET | `/pingclaw/mcp` | MCP server (Streamable HTTP) — Bearer auth via the user's API key |

## Configuration

Everything runs from environment variables (see `.env.example`):

- `PORT` — listen port (default `8080`)
- `DATABASE_URL` — PostgreSQL DSN (required, no default)
- `REDIS_URL` — Redis DSN (required; stores location data with a 24-hour TTL)
- `LOG_FILE` — server log (default `logs/server.log`)
- `APPLE_BUNDLE_ID` — the iOS bundle ID used as the JWT audience for Apple Sign-In (default `me.pingclaw.app`)
- `GOOGLE_CLIENT_ID` — the OAuth client ID from Google Cloud Console (required for Google Sign-In)
- `RATE_LIMIT_IP_PER_HOUR` — max sign-in attempts per IP per hour (default `10`)

Sign-in uses Apple and Google identity tokens — no phone number or SMS.

Run with `--debug` to bump log level to debug.

## Deploying to Digital Ocean

Target architecture: server droplet (or App Platform), DO managed
PostgreSQL, DO managed Redis.

1. Create a managed Postgres database; copy the connection string into
   `DATABASE_URL`. Use `?sslmode=require`.
2. Create a managed Redis; copy into `REDIS_URL`. Redis holds the
   ephemeral location cache (24h TTL) — the server requires it to be
   reachable on startup.
3. Deploy the container built from this `Dockerfile`. The schema is
   applied automatically on first boot (idempotent `CREATE TABLE IF NOT
   EXISTS`).

## Data layout

| Store | Holds |
|---|---|
| **PostgreSQL** | `users` (phone hash + identity), `user_tokens` (api_key/pairing_token/web_session hashes), `user_webhooks` (URL + secret) |
| **Redis** | `loc:<user_id>` — most recent location with a **24-hour TTL**. Nothing about location is ever written to Postgres. |

## Development

```bash
# Build
go build ./cmd/server

# End-to-end webhook test against the docker-compose stack
docker compose up -d postgres redis
DATABASE_URL=postgres://pingclaw:pingclaw@localhost:5433/pingclaw?sslmode=disable \
REDIS_URL=redis://localhost:6380 \
go run ./cmd/server &
go run ./cmd/openclaw-mock --register --token ak_xxx
```

See `webhook-test.md` for the full end-to-end webhook walkthrough.

## Related repos

- **iOS app** — [pingclaw-ios](https://github.com/pingclaw-me/pingclaw-ios) (the
  PingClaw app users install on their phone; pairs to this server with
  the pairing token).

## License

MIT — see [LICENSE](./LICENSE).

## Security

To report a vulnerability, see [SECURITY.md](./SECURITY.md). Please
report privately rather than via a public issue.
