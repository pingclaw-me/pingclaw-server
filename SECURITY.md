# Security Policy

## Reporting a vulnerability

If you find a security issue in PingClaw, please report it privately
rather than opening a public GitHub issue.

**Email:** [contact@pingclaw.me](mailto:contact@pingclaw.me)

Include:

- A short description of the issue
- Steps to reproduce, or a proof-of-concept if you have one
- The commit or release version you tested against
- Any suggested mitigation, if known

You should expect an acknowledgement within a few days. PingClaw is a
small project — please be patient with response times.

Please do not test against the production service at `pingclaw.me` in a
way that could affect other users (e.g. brute-forcing tokens, sending
high-volume traffic). Run a local instance via `docker compose up` and
test there.

## What's in scope

- The server binary in `cmd/server` and the packages it imports
- The MCP endpoint at `/pingclaw/mcp` and its auth model
- The webhook delivery path (outbound POSTs to user-supplied URLs)
- Authentication / token handling (api_key, pairing_token, web_session)
- Anything that could leak another user's location, phone number hash,
  or tokens

## What's out of scope

- Issues in third-party dependencies (please report those upstream;
  feel free to mention them here too)
- Findings that require a malicious operator with full server access
- Self-XSS, missing security headers without a concrete impact, denial
  of service through resource exhaustion

## Security model summary

- Phone numbers are stored as SHA-256 hashes, never plaintext.
- Auth tokens (`api_key`, `pairing_token`, `web_session`) are stored
  hashed; plaintext is shown to the user once at creation.
- Location data lives only in Redis with a 24-hour TTL — never
  written to PostgreSQL.
- Webhook secrets are stored in plaintext because the server itself
  replays them on every outbound POST; receivers verify the bearer.
- All MCP and dashboard requests authenticate against `user_tokens`;
  no shared global bearer.

## Coordinated disclosure

Once a fix is shipped, you're welcome to publish details. We're happy
to credit you in the release notes — let us know if you'd prefer to
remain anonymous.
