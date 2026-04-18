# Documentation Audit Report — Terms of Service
Generated: 2026-04-18 | Commit: fe7f829

## Executive Summary
| Metric | Count |
|--------|-------|
| Documents scanned | 1 |
| Claims verified | 9 |
| Verified TRUE | 6 (67%) |
| **Verified FALSE / Incomplete** | **3 (33%)** |

## False / Incomplete Claims Requiring Fixes

### web/termsofservice/content.md

| Line | Claim | Reality | Fix |
|------|-------|---------|-----|
| 9 | "The iOS app sends your phone's location" | Android app also exists and sends location | Update to "iOS and Android apps" |
| 13 | "keeping your pairing token and API key confidential" | Three token types exist: pairing_token, api_key, web_session. Web session omitted. | Mention web sessions or clarify "credentials" broadly |
| 13 | "Rotate them from the dashboard" | API key rotated from dashboard; pairing token rotated from within the app, not the dashboard | Clarify: "Rotate them from the dashboard or app settings" |

## Verified Claims

| Line | Claim | Status |
|------|-------|--------|
| 9 | "the hosted instance at pingclaw.me by default" | ✓ Confirmed in iOS, Android, and server defaults |
| 9 | "users can self-host" | ✓ docker-compose.yml + README instructions exist |
| 13 | "Anyone in possession of these credentials can read your stored location and modify your account" | ✓ All token types share identical permissions via requireAuth |
| 29 | "delete your account at any time from the dashboard or the app" | ✓ DELETE endpoint + iOS + Android all implement this |
| 29 | "Account deletion is immediate and irreversible" | ✓ Hard DELETE with CASCADE, no soft-delete or grace period |
| 9 | "not a navigation, social, or sharing service" | ✓ No social/sharing features in codebase |

## Human Review Queue
- None — all issues are straightforward text updates
