# LinkDrop

A Telegram bot that downloads media from popular platforms and sends them directly to your chat. Supports video and audio extraction with real-time progress tracking.

## Supported Platforms

- YouTube & YouTube Shorts
- Instagram (Reels & Posts)
- X (Twitter)

## Features

- Download video in 1080p (MP4)
- Extract audio in MP3 format
- Real-time download progress updates
- Interactive inline button UI for format selection

## How It Works

1. Send any supported link to the bot
2. Choose between **Video (1080p)** or **Audio (MP3)**
3. The bot downloads the media and sends it to your chat

## Prerequisites

- A Telegram Bot Token from [@BotFather](https://t.me/BotFather)

## Running with Docker

### Using Docker Compose (recommended)

1. Create a `docker-compose.yml` file:

```yaml
services:
  linkdrop-bot:
    image: umit144/linkdrop-bot:latest
    container_name: linkdrop-bot
    restart: always
    env_file:
      - .env
    environment:
      - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
```

2. Create a `.env` file in the same directory:

```
TELEGRAM_BOT_TOKEN=your_bot_token_here
```

3. Start the bot:

```bash
docker compose up -d
```

4. Check logs:

```bash
docker compose logs -f linkdrop-bot
```

5. Stop the bot:

```bash
docker compose down
```

### Using Docker Run

```bash
docker run -d \
  --name linkdrop-bot \
  --restart always \
  -e TELEGRAM_BOT_TOKEN=your_bot_token_here \
  umit144/linkdrop-bot:latest
```

## Building from Source

### With Docker

```bash
docker build -t linkdrop-bot .
docker run -d --name linkdrop-bot -e TELEGRAM_BOT_TOKEN=your_bot_token_here linkdrop-bot
```

### Without Docker

Requires Go 1.25+, [yt-dlp](https://github.com/yt-dlp/yt-dlp), and [FFmpeg](https://ffmpeg.org/) installed on your system.

```bash
go build -o linkdrop .
TELEGRAM_BOT_TOKEN=your_bot_token_here ./linkdrop
```

## Environment Variables

| Variable | Required | Description |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | Yes | Telegram Bot API token from BotFather |

## Tech Stack

- **Go** - Application runtime
- **yt-dlp** - Media downloading
- **FFmpeg** - Audio/video processing
- **telebot v3** - Telegram Bot API client
