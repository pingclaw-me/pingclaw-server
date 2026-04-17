# Testing the OpenClaw webhook end-to-end

Step-by-step from a working paired phone (PingClaw is installed and
already sending location updates to PingClaw Server).

## 1. Get your PingClaw bearer token

Both `pairing_token` and `api_key` work for `requireAuth`. The dashboard
only shows them at the moment of creation/rotation (the server stores
only hashes), so you have two options:

**Option A — reuse the token already on the phone.** It's in the iOS
keychain; you'd need to re-mint via the dashboard to extract it, which
defeats the purpose. Skip to Option B.

**Option B — mint a fresh token from the dashboard** (this will replace
whatever the phone is using, so you'll need to re-pair the phone after).

1. Open `https://<your-pingclaw-host>/pingclaw/` in a browser.
2. Sign in (the SMS code lands in the server log).
3. In the Pairing Token card, click **Rotate** (or **Generate Pairing
   Token** if it's the first time). The plaintext token appears in the
   field — copy it. Refreshing the page hides it again behind `••••`.
4. Re-paste the new token into the iOS app's Settings → Pairing Token,
   so the phone keeps working.

```bash
export PINGCLAW_TOKEN=pt_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

## 2. Start `openclaw-mock`

Pick the path that matches your setup.

### A) PingClaw server is on `localhost:8080` and your phone hits it via ngrok / same machine

```bash
go run ./cmd/openclaw-mock --register
```

This registers `http://localhost:9999/location` as the webhook. PingClaw
(running on the same box) can reach it directly.

### B) PingClaw is remote (ngrok-hosted) — phone hits a public URL

You need a second public tunnel for the mock, because PingClaw can't
reach your laptop's localhost.

```bash
# Terminal A — expose the mock to the internet
ngrok http 9999
# copy the https URL it prints, e.g. https://abc-mock.ngrok-free.dev

# Terminal B — start the mock and tell PingClaw about the public URL
go run ./cmd/openclaw-mock \
  --register \
  --pingclaw-url https://your-pingclaw.ngrok-free.dev \
  --webhook-url https://abc-mock.ngrok-free.dev/location
```

Either way, on startup you should see:

```
INFO openclaw-mock listening addr=:9999 webhook_path=/location
INFO webhook registered webhook_url=...
```

and on the PingClaw server:

```
INFO [PINGCLAW WEBHOOK] registered user_id=usr_... url=...
```

## 3. Trigger a location push from the phone

Easiest: open PingClaw on the phone → Settings → "Send test update"
(debug builds only). Otherwise unlock the phone and walk a few metres /
wait for the next adaptive ping.

## 4. Watch the mock

The `openclaw-mock` terminal should print, within a second or two:

```
─────────────────────────────────────────────
  LOCATION EVENT #1  (2026-04-16T...)
─────────────────────────────────────────────
  {
    "event": "location_update",
    "user_id": "usr_...",
    "location": { "lat": 37.77, "lng": -122.42, "accuracy_metres": 8.0 },
    "activity": "walking",
    ...
  }
─────────────────────────────────────────────
INFO location event received event_num=1 ...
```

And in the PingClaw server log:

```
INFO [PINGCLAW LOCATION] user_id=usr_... lat=... lng=...
INFO [PINGCLAW WEBHOOK] delivered user_id=usr_... url=... status=200
```

## If nothing fires, in order

1. Did the phone push reach PingClaw? Look for `[PINGCLAW LOCATION]` in
   the server log.
2. Did the webhook lookup find a row?
   `docker compose exec postgres psql -U pingclaw -d pingclaw -c "SELECT user_id, url FROM user_webhooks;"`
   should show your user + the mock URL.
3. Did the POST out fail? Look for `[PINGCLAW WEBHOOK] POST failed` in
   the server log (firewall / unreachable URL is the usual cause in
   scenario B).

## How the outbound POST is authenticated

The OpenClaw operator owns the webhook secret end-to-end. Pick or
generate any string and pass it via `--secret` (or
`OPENCLAW_WEBHOOK_SECRET`). The mock then:

1. Sends `{url, secret}` to `PUT /pingclaw/webhook` on registration.
2. PingClaw stores both verbatim and replays the secret as
   `Authorization: Bearer <secret>` on every outbound POST.
3. The mock rejects any incoming POST whose bearer doesn't match
   (constant-time compare → 401).

If you don't pass a secret, the mock generates one for you at startup
and prints it to the log — fine for testing, use a real secret in
production.

```bash
# Generate your own secret (any string works; this just gives a tidy random one)
export OPENCLAW_WEBHOOK_SECRET=whsec_$(openssl rand -hex 16)
go run ./cmd/openclaw-mock --register
```

To inspect the current registration on the server:

```bash
curl https://your-pingclaw/pingclaw/webhook \
  -H "Authorization: Bearer $PINGCLAW_TOKEN"
# {"url":"https://...","webhook_secret":"<the secret you supplied>"}
```

To rotate, just `PUT` again with a new secret — the previous one is
overwritten in place.

## To stop forwarding later

```bash
curl -X DELETE https://your-pingclaw/pingclaw/webhook \
  -H "Authorization: Bearer $PINGCLAW_TOKEN"
```

This deletes both the URL and the stored secret.
