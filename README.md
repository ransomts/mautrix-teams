# mautrix-teams

A Matrix-Microsoft Teams puppeting bridge built on the [mautrix bridgev2](https://docs.mau.fi/bridges/) framework. It bridges Teams chats, channels, and group conversations into Matrix rooms.

## Features

- **1:1 DMs, group chats, channels, and meetings** bridged as Matrix rooms
- **Bidirectional messaging** with text, formatting, mentions, replies, edits, and deletes
- **Inline images and file attachments** (Teams AMS + SharePoint/OneDrive via Graph API)
- **Stickers, adaptive cards, voice messages, and call/meeting notifications**
- **Reactions** synced bidirectionally
- **Typing indicators and read receipts**
- **Presence sync** via Microsoft Graph batch API
- **Room naming** with structured prefixes (`DM:`, `Group:`, `Meeting:`) and team/channel context
- **Backfill** of message history on first sync
- **Long-poll sync mode** for lower-latency event delivery
- **Enterprise/organizational account support** with configurable OAuth endpoints

## Prerequisites

- Docker and Docker Compose
- A Matrix homeserver (e.g., Synapse)
- A Microsoft Teams account (personal or enterprise/organizational)

## Setup

### Docker Compose (recommended)

1. Clone the repository
2. Copy `pkg/connector/example-config.yaml` and configure your bridge settings in `config.yaml`
3. Set up your `docker-compose.yml` with the bridge service alongside your homeserver
4. Build and start:

```bash
docker compose build mautrix-teams
docker compose up -d
```

### Standalone

```bash
./build.sh
./mautrix-teams
```

## Login

The bridge extracts MSAL tokens from the Teams web client's localStorage. There are two login flows:

1. **Manual**: Open Teams Web, extract localStorage JSON, and pass it to the bridge
2. **Webview**: Automated browser-based extraction (if supported by your setup)

A helper script `teams-login.py` is provided in the repo root for the manual flow. Run `python3 teams-login.py --help` for usage.

## Configuration

Key config options in `example-config.yaml`:

| Option | Description |
|--------|-------------|
| `client_id` | OAuth client ID (leave empty for default Teams web app) |
| `authorize_endpoint` | OAuth authorize URL (set for enterprise/tenant accounts) |
| `token_endpoint` | OAuth token URL (set for enterprise/tenant accounts) |
| `skype_token_endpoint` | Skype token URL (set for enterprise accounts) |
| `sync_mode` | `"poll"` (default) or `"longpoll"` for lower latency |
| `log_level` | `"trace"`, `"debug"`, `"info"` (default), `"warn"`, or `"error"` |

For enterprise/organizational accounts, you'll need to configure the tenant-specific OAuth endpoints.

## Development

```bash
# Build
./build.sh

# Run tests
go test ./...

# Lint (requires goimports, staticcheck)
pre-commit run --all-files

# Docker rebuild + restart
docker compose build mautrix-teams && docker compose up -d mautrix-teams
```

## Credits

Forked from [gekiclaws/mautrix-teams](https://github.com/gekiclaws/mautrix-teams). Built on the [mautrix bridgev2](https://github.com/mautrix/go) framework.
