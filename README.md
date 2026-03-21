# SlackFiler

SlackFiler is a lightweight Go service that listens for Slack `file_shared` events delivered via Redis, downloads the files using the Slack API, saves them to configured local directories, and pushes a result message back to a Redis list for downstream consumers.

---

## Table of Contents

- [Requirements](#requirements)
- [How it Works](#how-it-works)
- [Configuration](#configuration)
- [Sensitive Credentials (.env)](#sensitive-credentials-env)
- [Running Locally](#running-locally)
- [Running with Docker Compose](#running-with-docker-compose)
- [Redis Message Formats](#redis-message-formats)

---

## Requirements

- Go 1.24+
- A Redis instance (external)
- A Slack bot with `files:read` scope and the bot token (`xoxb-…`)

---

## How it Works

1. The service polls a Redis list (`input_list`) with `LPOP` every second.
2. When a `file_shared` event_callback JSON payload is found:
   - The channel ID is looked up in the channel map.
   - If configured, `GetFileInfo` is called to retrieve file metadata.
   - The file is downloaded from `url_private_download` using the bot token.
   - The file is written atomically to the mapped local directory.
   - A JSON result (file info + saved path) is `RPUSH`-ed to the output Redis list.
3. Unknown channels and non-`file_shared` events are skipped with a log message.

---

## Configuration

Copy `config.example.yaml` to `config.yaml` and edit it:

```yaml
redis:
  host: "localhost"
  port: 6379
  input_list: "slack_file_events"   # LPOP source
  output_list: "slack_file_results" # RPUSH destination

channels:
  C1234567890: "/downloads/general"  # Slack channel ID → local directory
  C0987654321: "/downloads/random"
```

`config.yaml` is gitignored; only `config.example.yaml` is committed.

The path to the config file defaults to `config.yaml` in the working directory. Override it with the `CONFIG_PATH` environment variable.

---

## Sensitive Credentials (.env)

Copy `.env.example` to `.env` and fill in the values:

```
SLACK_BOT_TOKEN=xoxb-your-bot-token-here
REDIS_PASSWORD=your-redis-password-here
```

`.env` is gitignored and must never be committed. In Docker Compose the file is passed via `env_file`.

---

## Running Locally

```bash
# 1. Copy and edit the config
cp config.example.yaml config.yaml

# 2. Copy and edit the .env
cp .env.example .env

# 3. Build and run
go build -o slackfiler .
./slackfiler
```

---

## Running with Docker Compose

```bash
# 1. Copy and edit config and .env (see above)

# 2. Ensure the external Redis network exists, e.g.:
#    docker network create redis_net

# 3. Add volume mounts for each download directory to docker-compose.yaml

# 4. Build and start
docker compose up --build -d
```

The service container runs **read-only** (`read_only: true`). The config file is mounted read-only at `/etc/slackfiler/config.yaml`. Download directories are mounted as separate volumes.

---

## Redis Message Formats

### Input (LPOP from `input_list`)

Standard Slack `event_callback` JSON with an inner `file_shared` event:

```json
{
  "type": "event_callback",
  "event": {
    "type": "file_shared",
    "file_id": "F0123456789",
    "channel_id": "C1234567890",
    "user_id": "U0123456789",
    "event_ts": "1774118809.000300"
  }
}
```

### Output (RPUSH to `output_list`)

```json
{
  "file_info": { /* slack.File object from GetFileInfo */ },
  "target_file_path": "/downloads/general/report.pdf"
}
```

