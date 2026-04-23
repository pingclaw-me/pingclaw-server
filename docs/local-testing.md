# Local Testing Guide

How to test the PingClaw server locally against the docker-compose
Postgres and Redis instances.

## Prerequisites

- Docker running with the PingClaw compose stack
- Go toolchain installed
- `jq` for readable curl output (optional)

## 1. Start Postgres and Redis

```bash
cd pingclaw-server
docker compose up -d postgres redis
```

This starts Postgres on port 5433 and Redis on port 6380 (offset
from defaults to avoid conflicts).

## 2. Start the local server

```bash
cd pingclaw-server
PORT=8090 \
DATABASE_URL='postgres://pingclaw:pingclaw@localhost:5433/pingclaw?sslmode=disable' \
REDIS_URL='redis://localhost:6380' \
go run ./cmd/pingclaw-server --debug
```

The server listens on `http://localhost:8090`. Port 8090 avoids
conflicts with anything already on 8080.

## 3. Create a test user

psql is not installed locally — run it via the docker container:

```bash
docker exec -i pingclaw-postgres psql -U pingclaw -d pingclaw -c "
  INSERT INTO users (user_id) VALUES ('usr_test123') ON CONFLICT DO NOTHING;
  INSERT INTO user_tokens (token_hash, user_id, kind, label)
  VALUES (encode(sha256('ak_localtest'::bytea), 'hex'), 'usr_test123', 'api_key', 'test')
  ON CONFLICT DO NOTHING;
"
```

If the container name has changed, find it with:

```bash
docker ps --format '{{.Names}}' | grep -i postgres
```

Then set the token for all subsequent requests:

```bash
export API_KEY=ak_localtest
```

## 4. Simulate phone location updates

Your real phone sends to `pingclaw.me`, not localhost. Simulate
the phone with curl:

```bash
curl -s -X POST http://localhost:8090/pingclaw/location \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "timestamp": "2026-04-19T20:00:00Z",
    "location": {"lat": -27.1396, "lng": -109.427, "accuracy_metres": 8},
    "activity": "Walking"
  }' | jq .
```

## 5. Test OpenClaw gateway push delivery

### Start a fake OpenClaw gateway

```bash
python3 -c "
from http.server import HTTPServer, BaseHTTPRequestHandler
import json

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(length)
        print(f'\n--- POST {self.path} ---')
        print(f'Auth: {self.headers.get(\"Authorization\", \"\")}')
        print(json.dumps(json.loads(body), indent=2))
        self.send_response(200)
        self.end_headers()

HTTPServer(('', 9999), Handler).serve_forever()
"
```

### Register the gateway destination

```bash
curl -s -X POST http://localhost:8090/pingclaw/webhook/openclaw \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "gateway_url": "http://localhost:9999",
    "hook_token": "test-token",
    "hook_path": "pingclaw",
    "action": "wake"
  }' | jq .
```

The fake gateway should print the verification POST. Response:

```json
{
  "destination_id": "dest_...",
  "type": "openclaw_gateway",
  "gateway_url": "http://localhost:9999",
  "hook_path": "pingclaw",
  "action": "wake",
  "verified": true
}
```

### Send a location and see the push

```bash
curl -s -X POST http://localhost:8090/pingclaw/location \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "timestamp": "2026-04-19T20:00:00Z",
    "location": {"lat": -27.1396, "lng": -109.427, "accuracy_metres": 8},
    "activity": "Walking"
  }' | jq .
```

The fake gateway should print:

```json
{
  "text": "Location update: -27.1396, -109.4270 ±8m (gps)",
  "mode": "now"
}
```

### Test the test endpoint

```bash
curl -s -X POST http://localhost:8090/pingclaw/webhook/openclaw/test \
  -H "Authorization: Bearer $API_KEY" | jq .
```

### Test agent mode

```bash
curl -s -X POST http://localhost:8090/pingclaw/webhook/openclaw \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "gateway_url": "http://localhost:9999",
    "hook_token": "test-token",
    "hook_path": "pingclaw",
    "action": "agent"
  }' | jq .
```

Then send a location — the fake gateway should print:

```json
{
  "message": "Location update: ...",
  "name": "PingClaw",
  "deliver": false
}
```

### Delete the destination

```bash
curl -s -X DELETE http://localhost:8090/pingclaw/webhook/openclaw \
  -H "Authorization: Bearer $API_KEY" | jq .
```

## 6. Test the standard webhook

Use the existing `openclaw-mock` tool:

```bash
go run ./cmd/openclaw-mock --register --token $API_KEY \
  --pingclaw-url http://localhost:8090
```

Then send a location update and the mock should print the webhook
payload.

## 7. Test the dashboard UI

Open `http://localhost:8090` in a browser. Social sign-in (Apple/
Google) won't work against the local server — create a web session
token directly instead.

### Create a web session for the test user

This only needs to be done once (the token persists in the local
database):

```bash
docker exec -i pingclaw-postgres psql -U pingclaw -d pingclaw -c "
  INSERT INTO user_tokens (token_hash, user_id, kind, label)
  VALUES (encode(sha256('ws_localtest'::bytea), 'hex'), 'usr_test123', 'web_session', 'test')
  ON CONFLICT DO NOTHING;
"
```

### Sign in via the browser console

Open `http://localhost:8090` and run this in the browser's developer
console (Cmd+Option+J on Mac):

```js
localStorage.setItem('web_session', 'ws_localtest');
location.reload();
```

This skips the sign-in flow and goes straight to the dashboard.
The session persists across page reloads until you sign out.

## Notes

- Production API keys from `pingclaw.me` won't work against the
  local database — different users table.
- The local server and `pingclaw.me` are completely independent.
  Your phone continues sending to production while you test locally.
- Both the standard webhook and OpenClaw gateway destination can
  be active at the same time — they fire concurrently on each
  location update.
