# Terraria Discord Bot

Discord bot that manages a Terraria server running on Kubernetes. Uses Discord's
[Interactions Endpoint](https://discord.com/developers/docs/interactions/overview#setting-up-an-endpoint-url)
to receive slash commands via HTTP webhook (no persistent gateway connection required).

## Commands

| Command | Description |
|---------|-------------|
| `/terraria start` | Scale the Terraria deployment to 1 replica |
| `/terraria stop` | Scale the Terraria deployment to 0 replicas |
| `/terraria status` | Show server state, player count, and connection info |

## Features

- **Start/Stop** the Terraria server via Discord slash commands
- **Player tracking** - follows Terraria server pod logs to detect join/leave
  events and maintain a live player list
- **Player announcements** - posts to a Discord channel when players join or
  leave (requires bot token and channel ID)
- **Auto-stop** - automatically scales the server to 0 when all conditions are met:
  1. Server has been running longer than `MINIMUM_RUN_MINUTES` (default: 30)
  2. No players are currently connected
  3. The last player left at least `AUTO_STOP_MINUTES` ago (default: 5)
- **Status reporting** - shows server state, replica readiness, online player
  names, and connection address
- **Signature verification** - validates all incoming requests using Ed25519

## Discord Setup

1. Create a new application at https://discord.com/developers/applications
2. Note the **Application ID** and **Public Key** from the General Information page
3. Go to the **Bot** section and create a bot. Copy the **Bot Token**.
   Uncheck **Public Bot** to prevent others from adding it to their servers
4. Go to **Installation** and set Install Link to **None**
5. Install the bot to your server by going to the **OAuth2** tab.
   Scroll down to **OAuth2 URL Generator**, tick the boxes for `applications.commands` and `bot` permissions, and then visit the generated url.
   You can also generate the oauth2 url yourself (replace `YOUR_APP_ID`):
   ```
   https://discord.com/oauth2/authorize?client_id=YOUR_APP_ID&scope=applications.commands+bot
   ```
6. Copy the **Channel ID** of the channel where you want join/leave/auto-stop
   notifications (right-click the channel in Discord with Developer Mode enabled)
7. Create the Kubernetes secret with your credentials:

```bash
kubectl create secret generic discord-bot-secret -n terraria \
  --from-literal=public-key=YOUR_PUBLIC_KEY \
  --from-literal=bot-token=YOUR_BOT_TOKEN \
  --from-literal=app-id=YOUR_APP_ID \
  --from-literal=channel-id=YOUR_CHANNEL_ID
```

8. Deploy the Helm chart. The bot will register slash commands on startup
9. Set the **Interactions Endpoint URL** in your Discord application to:
   `https://terraria.msng.to/discord`

Discord will send a PING to verify the endpoint. Once verified, the slash commands
are active.

## Configuration

All configuration is via environment variables:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DISCORD_PUBLIC_KEY` | Yes | | Discord application public key (hex) |
| `DISCORD_BOT_TOKEN` | No | | Bot token for command registration and notifications |
| `DISCORD_APP_ID` | No | | Application ID for command registration |
| `DISCORD_CHANNEL_ID` | No | | Channel ID for player and auto-stop notifications |
| `TERRARIA_NAMESPACE` | No | `terraria` | Kubernetes namespace of the Terraria deployment |
| `TERRARIA_DEPLOYMENT` | No | `terraria` | Name of the Terraria deployment |
| `PORT` | No | `8080` | HTTP server port |
| `INTERACTION_PATH` | No | `/discord` | URL path for Discord interactions |
| `AUTO_STOP_MINUTES` | No | `5` | Minutes of inactivity (no players) before auto-stop |
| `MINIMUM_RUN_MINUTES` | No | `30` | Minimum minutes the server must run before auto-stop can trigger |

## Development

### Prerequisites

- Python 3.12+
- [uv](https://docs.astral.sh/uv/)

### Running locally

```bash
export DISCORD_PUBLIC_KEY=your_public_key_hex
uv run python server.py
```

Note: The player tracker and server monitor will fail to connect to the
Kubernetes API when running outside a cluster. The HTTP server and command
handling still work.

### Running tests

Unit tests (no cluster required):

```bash
uv run pytest
```

Integration tests (starts a real HTTP server, K8s API mocked):

```bash
uv run pytest -m integration
```

All tests:

```bash
uv run pytest -m ''
```

## Architecture

The bot runs as a stateless HTTP server using Python's stdlib `http.server`.
It receives Discord interactions at the configured path, verifies the Ed25519
signature, and processes slash commands.

### Player Tracking

A background thread (`PlayerTracker`) continuously:
1. Finds the running Terraria server pod via label selector
2. Streams the pod's container logs using the Kubernetes API
3. Parses join/leave messages (`<player> has joined.` / `<player> has left.`)
4. Maintains a live set of online players
5. Announces join/leave events to the configured Discord channel

When the pod disappears (server stopped), the player list is cleared and the
tracker retries every 10 seconds until a new pod appears.

### Auto-stop

A background thread (`ServerMonitor`) checks every 30 seconds whether all
auto-stop conditions are met:
- Server uptime exceeds `MINIMUM_RUN_MINUTES`
- Player count is 0
- Last player left more than `AUTO_STOP_MINUTES` ago

This design works with vanilla Terraria servers by reading stdout logs rather
than requiring a REST API or server mods.

### RBAC

The bot's ServiceAccount has minimal permissions:

- `apps/deployments` - `get` (read deployment status)
- `apps/deployments/scale` - `get`, `patch` (scale the deployment)
- `core/pods` - `list` (find the terraria server pod)
- `core/pods/log` - `get` (stream server logs for player tracking)

These permissions are scoped to the `terraria` namespace via a Role/RoleBinding.
