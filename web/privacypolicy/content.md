## What PingClaw does

PingClaw sends your phone's GPS location to a server so your AI assistant can answer location-aware questions. You control when sharing is on or off.

## Phone number

Your phone number is used only to verify your identity at sign-up and at each subsequent sign-in. It is immediately converted into a form that cannot be reversed. PingClaw does not store, recover, view, or share your actual phone number.

When you sign in, your phone number is sent to Twilio (our SMS delivery provider) so it can deliver the verification code. Twilio retains the number per their own privacy policy. PingClaw still does not store your plaintext phone number — only its hash.

## Location data

- Only your most recent location is stored — there is no location history.
- Location data is held only in our Redis cache and expires automatically after 24 hours. It is never written to a permanent database.
- Your most recent location is replaced every time your phone sends an update.
- Your location is accessible only through your own account.

## What PingClaw does not do

- Does not sell, share, or provide your data to third parties.
- Does not use your data for advertising.
- Does not track you when sharing is off.
- Does not store location history.

## Authentication tokens

Your account has a pairing token used by the app and an API key used by your AI agent. Both can be rotated at any time from your dashboard, which immediately invalidates the previous value.

## Account deletion

You can delete your account at any time from within the app or web dashboard. This permanently removes your account, your authentication tokens, your webhook configuration, and your cached location from the server. Deletion is immediate and irreversible.

## What is stored on the server

- **Account data**: a unique ID, a SHA-256 hash of your phone number, your authentication tokens (stored as hashes), and — if you've configured one — your webhook URL and the secret you supplied. Plus the dates the account was created and last updated.
- **Current location**: your most recent location only, automatically deleted after 24 hours.

Standard web request metadata (IP address, User-Agent) may be observed by our hosting infrastructure; PingClaw does not durably store it in the application database.

## Contact

Questions about this policy? Email [contact@pingclaw.me](mailto:contact@pingclaw.me).
