# pingclaw-server

A self-hostable Go server that gives your AI agent your current location — one binary, no external dependencies in local mode.

Your phone runs the PingClaw app and sends its position to this server. Your AI agent reads it via MCP, or the server pushes it to your OpenClaw gateway. Nothing is stored permanently. Nothing phones home.

## Quick start (self-hosted)

```bash
go install github.com/pingclaw-me/pingclaw-server/cmd/pingclaw-server@latest
pingclaw-server --local
```

That's it. The server starts with a SQLite database, prints a pairing token, and listens on port 8080:

```
=== PingClaw Local Mode ===
Server URL:    http://localhost:8080
Pairing Token: pt_a1b2c3d4e5f6...

Enter these in the PingClaw app:
  1. Tap "Self-Hosted" on the sign-in screen
  2. Enter the server URL and pairing token
  3. Tap "Connect"
===============================
```

Open the PingClaw app, tap **Self-Hosted Server**, enter the URL and token, and your location starts flowing. No Apple or Google account required. No Redis. No Postgres. One file (`pingclaw.db`) holds everything.

### Reaching the server from your phone

Your phone needs to reach the server over the network. Pick whichever fits your setup:

**Tailscale** (recommended) — if both your machine and phone are on the same tailnet, use the Tailscale hostname:

```bash
pingclaw-server --local
# Use http://my-machine.tail1234.ts.net:8080 in the app
```

To expose it outside your tailnet (e.g. from cellular):

```bash
tailscale funnel 8080
# Use the https://... URL Tailscale prints
```

**ngrok** — punch through NAT without any network config:

```bash
pingclaw-server --local
ngrok http 8080
# Use the https://xxxx.ngrok-free.app URL in the app
```

**Same WiFi** — if your phone and machine are on the same network, use the LAN IP directly:

```bash
pingclaw-server --local
# Use http://192.168.1.x:8080 in the app
```

## What the server never does

- Never stores location history — only the single most recent position exists
- Never writes location to a permanent database — it lives in memory (or SQLite) with a 24-hour TTL, then disappears
- Never phones home — no telemetry, no analytics, no update checks
- Never sends data to third parties — your location goes only where you configure it
- Never stores plaintext API keys — only irreversible SHA-256 hashes

## Data architecture

**Local mode** (`--local`): SQLite for user accounts and tokens. Location is held in memory with a 24-hour TTL — if the server restarts, the cached position is gone. This is intentional: location is ephemeral.

**Hosted mode** (default): PostgreSQL for user accounts and tokens. Redis for the ephemeral location cache (same 24-hour TTL). This mode supports multiple users and Apple/Google Sign-In.

In both modes, location never touches persistent storage. The privacy guarantee is structural, not policy.

| Store | What it holds |
|---|---|
| SQLite or PostgreSQL | Users, identity links, token hashes, webhook configs |
| Memory or Redis | Most recent location per user (24h TTL) |
| Nothing | Location history, movement patterns, historical positions |

## MCP server

The server includes a built-in [MCP](https://modelcontextprotocol.io/) server at `/pingclaw/mcp`. It exposes a single tool — `get_my_location` — that returns the user's current position. No separate process, no sidecar, no plugin install.

Paste this into your MCP client config. Use `localhost` if the agent runs on the same machine, or your Tailscale/ngrok URL if it's remote:

**Claude Code** (`~/.claude.json`):
```json
{
  "mcpServers": {
    "pingclaw": {
      "type": "http",
      "url": "http://localhost:8080/pingclaw/mcp",
      "headers": { "Authorization": "Bearer YOUR_API_KEY" }
    }
  }
}
```

**Cursor** (`~/.cursor/mcp.json`), **VS Code** (`.vscode/mcp.json`) — same shape, same keys.

**Claude Desktop** (`~/Library/Application Support/Claude/claude_desktop_config.json`):
```json
{
  "mcpServers": {
    "pingclaw": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:8080/pingclaw/mcp",
               "--header", "Authorization:Bearer YOUR_API_KEY"]
    }
  }
}
```

Generate an API key by running the server, pairing the app, and calling:

```bash
curl -X POST http://localhost:8080/pingclaw/auth/rotate-api-key \
  -H "Authorization: Bearer YOUR_PAIRING_TOKEN"
```

## OpenClaw webhook

If you use [OpenClaw](https://openclaw.ai), the server can push each location update directly to your gateway. Every time the phone sends a position, the server POSTs:

```json
POST https://your-gateway:18789/hooks/pingclaw
Authorization: Bearer your-hook-token

{
  "text": "Location update: 40.1031, -75.3610 ±8m (gps)",
  "mode": "now"
}
```

The PingClaw server must be able to reach your gateway over the network. If both run on the same machine, `localhost` works. If they're on different machines, use a Tailscale hostname or public URL.

Configure it from the web dashboard or via the API:

```bash
curl -X POST http://localhost:8080/pingclaw/webhook/openclaw \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "gateway_url": "https://my-gateway.tail1234.ts.net:18789",
    "hook_token": "your-hook-token",
    "hook_path": "pingclaw",
    "action": "wake"
  }'
```

For testing without a real OpenClaw setup, use [pingclaw-listen](https://github.com/pingclaw-me/pingclaw-tools) from the tools repo:

```bash
go run github.com/pingclaw-me/pingclaw-tools/cmd/pingclaw-listen@latest
```

## Standard webhooks

For non-OpenClaw setups, the server can POST location updates to any URL. The receiver must be reachable from the PingClaw server — use a Tailscale hostname, ngrok tunnel, or public URL:

```bash
curl -X PUT http://localhost:8080/pingclaw/webhook \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"url": "https://your-receiver.example.com/location", "secret": "your-secret"}'
```

Each location update fires a POST with `Authorization: Bearer your-secret` and a JSON body containing lat, lng, and accuracy.

## Full deployment (Docker)

For running as a persistent service with multiple users and social sign-in:

```bash
cp .env.example .env    # edit with your Postgres/Redis URLs and OAuth credentials
docker compose up --build
```

The docker-compose stack includes PostgreSQL and Redis for local development. For production, use managed services (see [deployment guide](docs/deployment-instructions.md)).

The server requires `DATABASE_URL` and `REDIS_URL` in hosted mode. Schema is applied automatically on startup.

## Configuration

All settings are environment variables. In `--local` mode, none are required.

| Variable | Default | Required | Description |
|---|---|---|---|
| `PORT` | `8080` | No | Listen port |
| `DATABASE_URL` | `pingclaw.db` (local) | Hosted only | PostgreSQL DSN |
| `REDIS_URL` | — | Hosted only | Redis DSN |
| `APPLE_BUNDLE_ID` | `me.pingclaw.app` | No | JWT audience for Apple Sign-In (comma-separated for multiple) |
| `GOOGLE_CLIENT_ID` | — | Hosted only | Google OAuth client ID |
| `GOOGLE_IOS_CLIENT_ID` | — | No | Separate iOS Google client ID |
| `RATE_LIMIT_IP_PER_HOUR` | `10` | No | Sign-in attempts per IP per hour |
| `RATE_LIMIT_LOC_POST_PER_MIN` | `30` | No | Location updates per user per minute |
| `RATE_LIMIT_LOC_GET_PER_MIN` | `60` | No | Location reads per user per minute |
| `OAUTH_CLIENT_ID` | — | No | ChatGPT GPT Action client ID |
| `OAUTH_CLIENT_SECRET` | — | No | ChatGPT GPT Action client secret |
| `OAUTH_REDIRECT_URI` | — | No | Allowed OAuth redirect URI (if set, enforced on authorize) |

Flags:
- `--local` — SQLite + in-memory cache, no external dependencies, no social sign-in
- `--port 9090` — listen port (overrides `PORT` env var, default 8080)
- `--token` — generate a new pairing token on startup (requires `--local`)
- `--debug` — verbose JSON logging

## Security model

**Token storage**: API keys, pairing tokens, and web sessions are stored as SHA-256 hashes. The plaintext is shown once at creation and cannot be retrieved from the server.

**What a self-host operator can see**: The SQLite database contains user IDs, token hashes (not plaintext), webhook URLs, and webhook secrets (plaintext, because the server must replay them). Location data is in memory only and not visible in the database file.

**What a self-host operator cannot see**: Plaintext API keys or pairing tokens (only hashes are stored). Location history (only the current position exists, and only in memory).

**SSRF protection**: Webhook and OpenClaw gateway URLs are validated against private/reserved address ranges. In `--local` mode this check is relaxed to allow `localhost` receivers.

**Auth in local mode**: No Apple or Google credentials are needed. The server generates a pairing token on first run. Social sign-in endpoints return 404.

## Related repos

| Repo | Description |
|---|---|
| [pingclaw-ios](https://github.com/pingclaw-me/pingclaw-ios) | iOS app (SwiftUI). Background location, Sign in with Apple + Google. |
| [pingclaw-android](https://github.com/pingclaw-me/pingclaw-android) | Android app (Kotlin, Jetpack Compose). Same thing, different platform. |
| [openclaw-skill](https://github.com/pingclaw-me/openclaw-skill) | OpenClaw skill — teaches the agent to fetch location from PingClaw on demand. |
| [pingclaw-tools](https://github.com/pingclaw-me/pingclaw-tools) | Development and testing tools: E2E test suites, webhook listener. |

## License

MIT — see [LICENSE](./LICENSE).

## Security

To report a vulnerability, see [SECURITY.md](./SECURITY.md). Please report privately rather than via a public issue.
