# Deployment Instructions — Digital Ocean

This guide walks through deploying pingclaw-server to Digital Ocean
using managed PostgreSQL, managed Redis, and App Platform with
automated deploys via GitHub Actions.

## Prerequisites

- A [Digital Ocean](https://www.digitalocean.com) account
- An Apple Developer account (for Sign in with Apple)
- A Google Cloud Console project (for Sign in with Google)
- The [doctl](https://docs.digitalocean.com/reference/doctl/how-to/install/) CLI installed locally
- The `pingclaw-server` repo pushed to GitHub at
  `github.com/pingclaw-me/pingclaw-server`

## 1. Create the infrastructure

### Container Registry

1. DO dashboard → Container Registry → Create Registry.
2. Pick the **Starter** plan (free, 1 repo, 500 MB — plenty for this
   image).
3. Name it `pingclaw` (or whatever you like — update
   `.github/workflows/deploy.yml` `env.REGISTRY` to match).

### Managed PostgreSQL

1. DO dashboard → Databases → Create Database Cluster.
2. Engine: **PostgreSQL 16**.
3. Plan: **Basic $15/mo** (1 GB RAM, 10 GB disk, single node).
4. Region: pick the same region you'll use for the app (e.g.
   `nyc1`).
5. Database name: `pingclaw`.
6. Once provisioned, copy the **Connection String** from the
   dashboard. It looks like:
   ```
   postgresql://doadmin:PASSWORD@db-xxx.ondigitalocean.com:25060/pingclaw?sslmode=require
   ```

### Managed Redis

1. DO dashboard → Databases → Create Database Cluster.
2. Engine: **Redis 7**.
3. Plan: **Basic $15/mo** (1 GB RAM, single node).
4. Same region as Postgres.
5. Once provisioned, copy the **Connection String**. It looks like:
   ```
   rediss://default:PASSWORD@redis-xxx.ondigitalocean.com:25061
   ```
   Note the `rediss://` (double-s) — this means TLS is enabled,
   which go-redis handles automatically.

## 2. Create the App Platform app

1. DO dashboard → Apps → Create App.
2. Source: **Container Registry** → select the `pingclaw` registry
   and the `pingclaw-server` image. (The image won't exist yet on
   first setup — that's fine; the first GitHub Actions deploy will
   push it.)
3. Plan: **Basic $5/mo** (512 MB RAM, shared CPU).
4. HTTP port: `8080`.
5. Set the following **environment variables** in the app settings:

| Variable | Value |
|---|---|
| `PORT` | `8080` |
| `DATABASE_URL` | the Postgres connection string from step 1 |
| `REDIS_URL` | the Redis connection string from step 1 |
| `APPLE_BUNDLE_ID` | `me.pingclaw.app` |
| `GOOGLE_CLIENT_ID` | your OAuth client ID from Google Cloud Console |
| `RATE_LIMIT_IP_PER_HOUR` | `10` (default) |

6. Note the **App ID** — find it in the app settings page or run:
   ```bash
   doctl apps list
   ```

## 3. Configure GitHub Actions

Add these **repository secrets** in GitHub → Settings → Secrets and
variables → Actions → New repository secret:

| Secret | Value |
|---|---|
| `DIGITALOCEAN_ACCESS_TOKEN` | a DO personal access token with read + write scope |
| `DIGITALOCEAN_APP_ID` | the App Platform app ID from step 2 |

The workflow file `.github/workflows/deploy.yml` is already in the
repo and triggers on every GitHub release (or `v*` tag push).

## 4. Deploy

### First deploy

```bash
git tag v1.0.0
git push origin v1.0.0
```

Or create a release in the GitHub UI (Releases → Draft a new
release → tag `v1.0.0` → Publish).

GitHub Actions will:
1. Build the Docker image from the repo's `Dockerfile`.
2. Tag it as `v1.0.0` and `latest`.
3. Push both tags to the DO Container Registry.
4. Trigger a new deployment on App Platform (`doctl apps
   create-deployment`).

The action takes ~2 minutes. Watch progress in GitHub → Actions tab.

### Subsequent deploys

Same pattern — push a new tag or create a new release:

```bash
git tag v1.1.0
git push origin v1.1.0
```

App Platform picks up the new image and does a zero-downtime rolling
deploy.

## 5. DNS

Point `pingclaw.me` at the App Platform app:

1. In the App Platform app settings → Domains → Add Domain.
2. Enter `pingclaw.me`.
3. DO will give you a CNAME target (e.g.
   `xxx.ondigitalocean.app`).
4. At your DNS provider, create a CNAME record:
   ```
   pingclaw.me  CNAME  xxx.ondigitalocean.app
   ```
5. SSL is provisioned automatically via Let's Encrypt once DNS
   propagates.

## 6. Verify

```bash
# Health check
curl https://pingclaw.me/pingclaw/auth/me
# → 401 (expected — no token, but proves the server is running)

# Open https://pingclaw.me in a browser
# → sign in with Apple or Google → dashboard loads

# MCP
curl -X POST https://pingclaw.me/pingclaw/mcp \
  -H "Authorization: Bearer ak_..." \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
# → 200 with MCP handshake
```

## Cost summary

| Component | Plan | Monthly |
|---|---|---|
| App Platform | Basic (512 MB) | $5 |
| Managed PostgreSQL | Basic (1 GB, single) | $15 |
| Managed Redis | Basic (1 GB, single) | $15 |
| Container Registry | Starter (free) | $0 |
| SSL | Let's Encrypt (free) | $0 |
| **Total** | | **~$35/mo** |

## Troubleshooting

**App won't start / crashes on boot**

Check App Platform logs (DO dashboard → Apps → your app → Runtime
Logs). Common causes:

- `DATABASE_URL not set` — env var missing or misspelled.
- `REDIS_URL not set` — same.
- `database not ready yet — retrying` looping forever — the Postgres
  connection string is wrong, or the database cluster hasn't finished
  provisioning.

**Social sign-in not working**

- Verify `GOOGLE_CLIENT_ID` matches the OAuth client ID in Google
  Cloud Console.
- Verify `APPLE_BUNDLE_ID` matches the iOS bundle ID registered for
  Sign in with Apple in the Apple Developer Portal.
- Check server logs for `[PINGCLAW AUTH] social token rejected` —
  the error message will say whether the issue is the issuer,
  audience, or expiry.

**GitHub Actions deploy fails**

- `unauthorized` on registry push — check that
  `DIGITALOCEAN_ACCESS_TOKEN` has write scope and hasn't expired.
- `app not found` on create-deployment — check
  `DIGITALOCEAN_APP_ID` matches. Run `doctl apps list` to verify.

**Redis connection refused**

- Managed Redis uses TLS. The connection string must start with
  `rediss://` (double-s), not `redis://`.

## Scaling

Start with the $35/mo setup above. When you need more:

1. **Add app replicas**: App Platform dashboard → scale to 2–3
   instances. Zero code changes (the server is stateless).
2. **Upgrade Redis**: bump to the $30/mo plan for more connections
   and memory.
3. **Upgrade Postgres**: add a standby node ($30/mo) for HA, or a
   read replica for query offloading.
4. **Connection pooling**: enable DO's built-in PgBouncer ($7/mo)
   once you run multiple app replicas.
