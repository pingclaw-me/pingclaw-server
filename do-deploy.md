# PingClaw — DigitalOcean Deployment Guide

Deploy PingClaw on DigitalOcean using App Platform (server), Managed PostgreSQL (database), and Managed Valkey (Redis-compatible cache).

## Prerequisites

- A DigitalOcean account
- `doctl` CLI installed and authenticated: `brew install doctl && doctl auth init`
- The PingClaw server repo pushed to GitHub

---

## Step 1: Create the Managed PostgreSQL Database

1. Go to **DigitalOcean Control Panel → Databases → Create Database Cluster**
2. Choose **PostgreSQL** (version 17 recommended)
3. Configuration:
   - **Plan**: Basic (1 vCPU, 1 GB RAM, 10 GB disk) — $15/month is fine to start
   - **Region**: Choose the same region you'll use for App Platform (e.g., `nyc1` or `sfo1`)
   - **Name**: `pingclaw-db`
4. Click **Create Database Cluster** — provisioning takes ~5 minutes

### After creation:

5. Go to the cluster's **Overview** tab → copy the **Connection String** (public). It looks like:
   ```
   postgresql://doadmin:PASSWORD@pingclaw-db-do-user-XXXXX-0.c.db.ondigitalocean.com:25060/defaultdb?sslmode=require
   ```
6. Go to **Users & Databases** tab → click **Add a Database** → name it `pingclaw`
7. Update the connection string to use the new database name:
   ```
   postgresql://doadmin:PASSWORD@pingclaw-db-do-user-XXXXX-0.c.db.ondigitalocean.com:25060/pingclaw?sslmode=require
   ```
   Save this — you'll need it as `DATABASE_URL` in Step 3.

### Restrict access (after App Platform is set up):

8. Go to **Settings** tab → **Trusted Sources** → add your App Platform app (see Step 4)

---

## Step 2: Create the Managed Valkey (Redis) Cluster

DigitalOcean discontinued Managed Redis on June 30, 2025. The replacement is **Managed Valkey**, which is fully Redis-compatible. PingClaw's Go Redis client works with it unchanged.

1. Go to **DigitalOcean Control Panel → Databases → Create Database Cluster**
2. Choose **Valkey** (version 8)
3. Configuration:
   - **Plan**: Basic (1 vCPU, 1 GB RAM) — $15/month
   - **Region**: Same region as PostgreSQL and App Platform
   - **Name**: `pingclaw-cache`
4. Click **Create Database Cluster**

### After creation:

5. Go to the cluster's **Overview** tab → copy the **Connection String** (public). It looks like:
   ```
   rediss://default:PASSWORD@pingclaw-cache-do-user-XXXXX-0.c.db.ondigitalocean.com:25061
   ```
   Note: `rediss://` (with double 's') indicates TLS. The Go Redis client supports this automatically.

   Save this — you'll need it as `REDIS_URL` in Step 3.

---

## Step 3: Deploy the Server on App Platform

### Option A: Deploy from GitHub (recommended)

1. Push the PingClaw server repo to GitHub if not already there
2. Go to **DigitalOcean Control Panel → Create → App Platform**
3. Select **GitHub** as the source
4. Authorize DigitalOcean to access your repo, select the `pingclaw-server` repository
5. Branch: `main`
6. App Platform detects the `Dockerfile` automatically
7. Configure the component:
   - **Type**: Web Service
   - **Name**: `pingclaw-server`
   - **HTTP Port**: `8080`
   - **Instance Size**: Basic ($5/month, 512 MB RAM) — upgrade later if needed
   - **Instance Count**: 1

### Option B: Deploy via app spec YAML

Create `.do/app.yaml` in the repo root:

```yaml
name: pingclaw
region: nyc
services:
  - name: pingclaw-server
    github:
      repo: your-github-user/pingclaw-server
      branch: main
      deploy_on_push: true
    dockerfile_path: Dockerfile
    http_port: 8080
    instance_count: 1
    instance_size_slug: apps-s-1vcpu-0.5gb
    envs:
      - key: DATABASE_URL
        value: "postgresql://doadmin:PASSWORD@pingclaw-db-do-user-XXXXX-0.c.db.ondigitalocean.com:25060/pingclaw?sslmode=require"
        type: SECRET
      - key: REDIS_URL
        value: "rediss://default:PASSWORD@pingclaw-cache-do-user-XXXXX-0.c.db.ondigitalocean.com:25061"
        type: SECRET
      - key: APPLE_BUNDLE_ID
        value: "XTL-74JC6TTWQ8.me.pingclaw.app"
      - key: APPLE_WEB_SERVICE_ID
        value: "me.pingclaw.web"
      - key: GOOGLE_CLIENT_ID
        value: "829482463629-rn53ogdupf2nnjlt8ooi5unqquvn1vuu.apps.googleusercontent.com"
      - key: GOOGLE_IOS_CLIENT_ID
        value: "829482463629-bns6vt57man8o94sf11i1b8nk9g70lb5.apps.googleusercontent.com"
      - key: OAUTH_CLIENT_ID
        value: "pingclaw-gpt"
      - key: OAUTH_CLIENT_SECRET
        value: "your-oauth-secret-here"
        type: SECRET
```

Deploy with:
```bash
doctl apps create --spec .do/app.yaml
```

### Set environment variables (if using Option A):

8. In the App Platform settings, go to **Environment Variables** and add:

| Variable | Value | Encrypt? |
|----------|-------|----------|
| `DATABASE_URL` | PostgreSQL connection string from Step 1 | Yes |
| `REDIS_URL` | Valkey connection string from Step 2 | Yes |
| `APPLE_BUNDLE_ID` | `XTL-74JC6TTWQ8.me.pingclaw.app` | No |
| `APPLE_WEB_SERVICE_ID` | `me.pingclaw.web` | No |
| `GOOGLE_CLIENT_ID` | Web client ID | No |
| `GOOGLE_IOS_CLIENT_ID` | iOS client ID | No |
| `OAUTH_CLIENT_ID` | `pingclaw-gpt` | No |
| `OAUTH_CLIENT_SECRET` | Your OAuth secret | Yes |

9. Click **Save** and deploy

---

## Step 4: Restrict Database Access

Once the App Platform app is deployed:

1. Go to your **PostgreSQL cluster → Settings → Trusted Sources**
2. Click **Edit** → select your App Platform app → **Save**
3. Repeat for the **Valkey cluster → Settings → Trusted Sources**

This blocks all connections except from your App Platform app.

---

## Step 5: Configure the Custom Domain

1. In the App Platform app, go to **Settings → Domains**
2. Click **Add Domain**
3. Enter `pingclaw.me`
4. Choose **You manage your domain**
5. Copy the CNAME target (e.g., `pingclaw-xxxxx.ondigitalocean.app`)
6. In your DNS provider, add:
   - **CNAME** record: `@` → `pingclaw-xxxxx.ondigitalocean.app` (or use an ALIAS/ANAME if your DNS supports it for the root domain)
   - If your DNS doesn't support CNAME at the root, use a `www` subdomain and set up a redirect, or use DigitalOcean's DNS:
     - Transfer your domain's nameservers to DigitalOcean: `ns1.digitalocean.com`, `ns2.digitalocean.com`, `ns3.digitalocean.com`
7. App Platform automatically provisions an SSL certificate via Let's Encrypt

### Verify:
```bash
curl -I https://pingclaw.me
# Should return 200 OK with valid HTTPS
```

---

## Step 6: Verify the Deployment

### Health check:
```bash
curl https://pingclaw.me/pingclaw/location
# Should return 401 (no token) — means the server is running
```

### Sign in from the iOS app:
1. Update the iOS app's server URL from the ngrok dev URL to `https://pingclaw.me`
2. Sign in with Apple or Google
3. Enable location sharing
4. Verify location appears on the web dashboard

### Check logs:
```bash
doctl apps logs YOUR_APP_ID --type run
```

To get your app ID:
```bash
doctl apps list
```

---

## Step 7: Enable Auto-Deploy

If you deployed from GitHub with `deploy_on_push: true` (or checked the box in the UI), every push to `main` triggers a rebuild and deploy automatically.

Verify:
```bash
git push origin main
doctl apps list-deployments YOUR_APP_ID
```

---

## Costs Summary

| Service | Plan | Monthly Cost |
|---------|------|-------------|
| App Platform | Basic (1 vCPU, 512 MB) | $5 |
| Managed PostgreSQL | Basic (1 vCPU, 1 GB, 10 GB disk) | $15 |
| Managed Valkey | Basic (1 vCPU, 1 GB) | $15 |
| **Total** | | **$35/month** |

Scale up as needed. App Platform supports horizontal scaling (increase instance count) and vertical scaling (increase instance size).

---

## Troubleshooting

### "connection refused" on database

- Check that the App Platform app is added to the database's Trusted Sources
- Verify the connection string uses `sslmode=require`
- Make sure you're using the `pingclaw` database, not `defaultdb`

### "certificate verify failed" on Valkey

- The Go Redis client supports `rediss://` (TLS) URLs natively
- Make sure the URL starts with `rediss://` (two s's), not `redis://`

### Build fails on App Platform

- Check that the Dockerfile works locally: `docker build -t pingclaw .`
- App Platform uses BuildKit by default; the multi-stage Dockerfile should work as-is
- Check build logs: `doctl apps logs YOUR_APP_ID --type build`

### Domain not working

- DNS propagation can take up to 48 hours (usually minutes)
- Verify DNS: `dig pingclaw.me CNAME`
- Check that CAA records allow `letsencrypt.org` and `pki.goog` if you have any CAA records set

### App keeps restarting

- Check runtime logs: `doctl apps logs YOUR_APP_ID --type run`
- Common cause: `DATABASE_URL` or `REDIS_URL` not set or incorrect
- The server retries database/Redis connections for 30 seconds on startup, then exits

---

## Updating Environment Variables

```bash
# List current env vars
doctl apps spec get YOUR_APP_ID

# Update via spec
doctl apps update YOUR_APP_ID --spec .do/app.yaml
```

Or use the Control Panel: **Apps → pingclaw → Settings → Environment Variables**.

---

## Backup & Recovery

- **PostgreSQL**: DigitalOcean automatically takes daily backups (retained for 7 days). Restore from the Control Panel under **Backups**.
- **Valkey**: In-memory cache only — no backup needed. Data rebuilds from the apps (location cache has 24h TTL, rate limit counters have 1h TTL).
- **App Platform**: Stateless — redeploy from GitHub at any time.
