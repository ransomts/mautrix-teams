# Changelog

This fork originates from [gekiclaws/mautrix-teams](https://github.com/gekiclaws/mautrix-teams).
Entries below cover changes made in this fork only.

## Unreleased

* Room names no longer flap on restart. Thread discovery replaced every stored name with the conversation list's generic "Chat" whenever the stored name had no `DM:`/`Group:`/`Meeting:` prefix, so each restart renamed every channel three times ("Chat", a member list, then "Team / Channel") and every system stream twice, burying the recent messages under state events. The generic name never replaces a worked-out one now, and the member-list naming pass only touches real group chats.
* Team spaces: channel portals are parented to their team's space on every sync, independently of renaming, and a repair pass fixes portals from earlier versions: a team portal whose room is a plain room (created on demand before team portals were declared spaces; bridgev2 could only warn "Tried to change existing room type") is detached and recreated as a space with its channels re-added, and the orphan login-scoped twin portals are deleted.
* Messages that are only a Teams emoticon (🦐, ❤️, …) or a GIF-picker GIF were bridged as a blank line. Emoticons are `<img>` tags with the emoji in `alt` and no text; they now render as that emoji. GIF-picker GIFs come as a bare `<img>` on a GIF CDN without the Giphy itemtype; they were mistaken for pasted images, had their host rewritten to the AMS endpoint and failed to download. GIFs are now recognised by host, fetched without any Teams credential and bridged as images, with a link as fallback. A message the converter still gets nothing out of is bridged as a notice naming its Teams message type instead of a single space.
* Inline images that are not on Teams' media store are downloaded without the skypetoken (it was sent to whatever host the image was on).
* DMs whose counterpart has no profile (typically a deleted account) are named `DM: unknown user <id>` instead of being left nameless (which clients showed as "bridge bot, <me>"), and the counterpart's ghost is put in the room from the thread ID. Unknown users are looked up in the Graph directory before giving up, for DM names, ghost names and unnamed group chats' member lists (`GetThreadMembers`).
* Ghost display names now follow the profile table: a ghost first seen through a reaction or read receipt was named with its raw Teams ID (`8:orgid:…`) and never renamed once a message revealed the name. Messages now rename the ghost when they update the profile, a repair pass runs on connect, and a message without a sender name no longer overwrites a known name in the profile table.
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
