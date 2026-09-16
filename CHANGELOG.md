# Changelog

This fork originates from [gekiclaws/mautrix-teams](https://github.com/gekiclaws/mautrix-teams).
Entries below cover changes made in this fork only.

## Unreleased

* New login flow: "Microsoft account (device code)". Sign in with a one-time code at microsoft.com/devicelogin using a public client (default: the Teams desktop client ID), which yields a refresh token with the tenant's normal sliding lifetime (90 days by default) instead of the 24-hour token lifted from the Teams web app. Refreshes for such logins use the login's own client ID, scope and tenant token endpoint. New config keys: `network.device_code_client_id`, `network.device_code_scope`, `network.device_code_graph_scope`.
* `ProfileQuery.GetByTeamsUserID` returns `(nil, nil)` for an unknown user instead of `sql.ErrNoRows`, which made ghost user-info lookups fail for users the bridge had not seen.
* Added a test suite for `pkg/teamsdb` (schema upgrade, thread state, profiles, consumption horizons, per-login and per-bridge scoping) using a temporary SQLite database.
* Serialised Skype/Graph token refreshes under a mutex; previously the sync loop, presence loop and Matrix handlers refreshed and read tokens with no synchronisation.
* Failed token refreshes are now remembered and retried with backoff (30s doubling to 15min); `invalid_grant`/`interaction_required` go straight to 15min and mark the login as logged out instead of re-hitting the identity provider on every poll.
* Refreshed Skype tokens (and region URLs) are pushed into the cached consumer API client, which previously kept the token it was built with.
* `ListMessages` honours its cursor and pages back (up to 10 pages of 200) until the caller's sequence is reached, so bursts larger than one page are no longer dropped.
* Group read receipts bridge every participant's consumption horizon instead of giving up when more than one other member has one.
* Fixed a nil-map panic in `pollPresence` that crashed the process on the first successful presence batch.
* Channel thread roots are passed to bridgev2 as `ThreadRoot` instead of `m.relates_to` with fabricated event IDs; the bridge now advertises thread support.
* Stale typing control messages (at or below the cursor, or older than 15s) are ignored instead of being re-emitted on every poll.
* Long-poll requests are bounded by context rather than the client's global timeout, which was shorter than the server hold and killed every idle poll; retry sleeps honour context cancellation.
* Raised the general HTTP timeout to 60s for uploads/downloads and stopped logging the PKCE `code_verifier`.
* MBI token refresh derives the `/common` endpoint only for real Microsoft hosts (test servers are left alone).
* Removed unused helpers (`wrapTeamsReplyHTML`, `DownloadMatrixMedia`, `SendMessage`, `SendGIF`); replaced the upstream mautrix-discord changelog with this file.
* Fixed a `go vet` failure in `presenceLoop` and gofmt'd the tree.
