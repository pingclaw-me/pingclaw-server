## Push your location to OpenClaw

PingClaw can push your GPS position directly to your OpenClaw gateway every time your phone reports a new location. Your agent gets your position as context automatically — no need to ask it to fetch.

### Step 1 — Enable hooks on your gateway

Add this to your `~/.openclaw/openclaw.json` and restart the gateway:

```json
{
  "hooks": {
    "enabled": true,
    "token": "pick-a-strong-random-secret",
    "path": "/hooks"
  }
}
```

Generate a good token with `openssl rand -hex 24` or any password manager.

### Step 2 — Make your gateway reachable

PingClaw's server (`pingclaw.me`) needs to POST to your gateway over the internet. If your gateway is on a local machine, expose it with one of:

- **Tailscale Funnel** — `tailscale funnel 18789`
- **ngrok** — `ngrok http 18789`
- **Cloudflare Tunnel**
- **VPS with a public IP** — no tunnel needed

Your gateway URL will look something like `https://my-machine.tail1234.ts.net:18789` or `https://abc123.ngrok-free.app`.

### Step 3 — Connect PingClaw

Enter your gateway URL and the hook token you chose in Step 1 in the form below, then click **Connect**. PingClaw will send a test message to verify the connection before saving.

### Step 4 — Verify

Open your OpenClaw session and check that the test message appeared. Then move around with your phone — your agent now has your location as context whenever you ask a question.

### How it works

Each time your phone sends a GPS fix to PingClaw, the server pushes a one-line location update to your gateway:

```
Location update: -27.1396, -109.4270 ±8m (gps)
```

In **wake** mode (the default), this is injected as context into your current session — no tokens consumed, no agent reply triggered. The next time you ask a location-sensitive question, the agent already knows where you are.

In **agent** mode, each update triggers a full agent run. This is useful for standing instructions like "remind me when I'm near a pharmacy" but costs tokens on every GPS update. Most users should use wake mode.
