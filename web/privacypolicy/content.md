*Last updated: April 19, 2026*

## What PingClaw does

PingClaw sends your phone's GPS location to a server so your AI assistant can answer location-aware questions. You control when sharing is on or off.

## Sign-in

The iOS and Android apps use Sign in with Apple or Sign in with Google for authentication. When you sign in, PingClaw receives only a unique, opaque identifier from your provider. PingClaw does not request or store your email address, phone number, contacts, photos, or other account data.

The web dashboard does not use social sign-in directly. Instead, you generate a short-lived code on your phone and enter it on the website. This ensures your web session is always linked to the same account as your phone.

## Location data

- Only your most recent location is stored — there is no location history.
- Location data is held only in a Redis cache and expires automatically after 24 hours. It is never written to a permanent database.
- Your most recent location is replaced every time your phone sends an update.
- Your location is accessible only through your own account.

## What PingClaw does not do

- Does not sell, share, or provide your data to third parties.
- Does not use your data for advertising.
- Does not track you when sharing is off.
- Does not store location history.

## Authentication tokens

Your account may have up to three token types:

- **Pairing token** — used by the iOS or Android app to authenticate with the server.
- **API key** — used by your AI agent (e.g. via MCP or ChatGPT) to read your location.
- **Web session** — issued when you sign in on the web dashboard.

API keys can be rotated at any time from your dashboard, which immediately invalidates the previous value. Pairing tokens are reissued when you sign in again on the app. All tokens are stored as irreversible SHA-256 hashes — the plaintext is shown once at creation and cannot be retrieved.

## Webhooks

If you configure a webhook, PingClaw stores the webhook URL and the secret you provide. The secret is stored in plaintext (not hashed) because PingClaw must replay it on every outbound POST so your receiver can verify the request came from PingClaw.

## Account deletion

You can delete your account at any time from within the app or web dashboard. This permanently removes your account, your sign-in identities, your authentication tokens, your webhook configuration, and your cached location from the server. Deletion is immediate and irreversible.

## What is stored on the server

- **Account data**: a unique user ID and the dates the account was created and last updated.
- **Sign-in identities**: for each provider you've used (Apple, Google), the provider name and a provider-issued opaque identifier. No email or personal information is stored. Stored in PostgreSQL.
- **Authentication tokens**: SHA-256 hashes of your pairing token, API key, and web sessions. The plaintext is never stored. Stored in PostgreSQL.
- **Webhook** (if configured): the URL and secret you supplied. Stored in PostgreSQL.
- **Current location**: your most recent location only. Stored in Redis with a 24-hour expiry.
- **Transient caches**: to reduce database load, PingClaw temporarily caches token lookups (5-minute expiry), webhook configurations (5-minute expiry), and one-time sign-in codes (5-minute expiry) in Redis. These caches contain no data beyond what is already stored in the database and expire automatically.
- **Rate limit counters**: to prevent abuse, PingClaw stores temporary per-IP request counters (1-hour expiry) and per-user location request counters (1-minute expiry) in Redis. These contain only an identifier and a count, no location or personal data.

Standard web request metadata (IP address, User-Agent) may be observed by our hosting infrastructure; PingClaw does not durably store it in the application database.

## Contact

Questions about this policy? Email [contact@pingclaw.me](mailto:contact@pingclaw.me).
