# cmd-translate

A conversational shell-command assistant with a messenger-style web UI. Describe what you need in plain English and get the right terminal command back.

Supports **LM Studio** (local models) and **Google AI Studio** (Gemini) as LLM providers — switch between them from the UI at any time.

## Features

- Conversational dialog: follow-up questions, clarifications, context memory
- Dangerous command detection with confirmation prompt before execution
- Messenger-style chat interface (emoji avatars, typing indicator, chat bubbles)
- Dynamic model selection from available provider models
- Dual provider support: local LM Studio or cloud Google AI Studio
- Google API key: set `GOOGLE_API_KEY` on the server, or paste a key in the browser when prompted (stored in `localStorage`, persists between visits)
- CLI mode for quick one-off translations

## Requirements

- Go 1.23 or newer (for local builds)
- **LM Studio** with a model loaded and Local Server enabled, _or_
- **Google AI Studio** API key from [aistudio.google.com/apikey](https://aistudio.google.com/apikey)

## Configuration

### LM Studio (default)

| Variable | Description |
| ---------- | ------------- |
| `LM_STUDIO_BASE_URL` | Base URL for the API. Default: `http://127.0.0.1:1234/v1` |
| `LM_STUDIO_MODEL` | Model id. Default: `mlx-community/Phi-4-mini-instruct-4bit` |
| `LM_STUDIO_API_KEY` | Optional. Sent as `Authorization: Bearer …` |
| `LM_STUDIO_MAX_TOKENS` | Optional. Override max output tokens (integer) |

### Google AI Studio (backup)

| Variable | Description |
| ---------- | ------------- |
| `GOOGLE_API_KEY` | Required for Google unless you supply a key in the web UI (see below) |
| `GOOGLE_MODEL` | Model id. Default: `gemini-2.0-flash` |
| `GOOGLE_AI_BASE_URL` | Override base URL. Default: `https://generativelanguage.googleapis.com/v1beta/openai` |

If `GOOGLE_API_KEY` is not set, the UI asks for a key and sends it per request as the header `X-Google-API-Key`. The key is saved in the browser’s `localStorage` (same device and browser profile). The CLI still requires the environment variable.

## Local build and run

Start LM Studio, load **mlx-community/Phi-4-mini-instruct-4bit**, start the server, then:

```bash
go build -o cmd-translate .
```

**Arguments** — pass the description as arguments:

```bash
./cmd-translate find all go files modified in the last 7 days
```

**Stdin** — pipe or type one line:

```bash
echo "kill whatever is on port 8081" | ./cmd-translate
```

## Web UI

Start a local HTTP server with a chat-style interface (same LM Studio backend as the CLI):

```bash
./cmd-translate serve
```

Open [http://127.0.0.1:8081](http://127.0.0.1:8081) in your browser. **Model**: **Sync models** loads ids from LM Studio’s `GET /v1/models` (via `GET /api/models`). The UI uses a native **dropdown** so every id returned by the server appears in a scrollable list (unlike a `<datalist>`, which browsers filter and can look incomplete). **Custom id** overrides the dropdown when filled. Empty selection uses `LM_STUDIO_MODEL` / built-in default. Choices are stored in `localStorage`. LM Studio only lists models **visible to the local server** (loaded or eligible with JIT loading); models not returned there cannot appear until LM Studio exposes them.

The UI keeps **session memory**: each request sends prior user/command turns so follow-ups stay in context. Use **New chat** to clear that session. The server does not persist chat sessions across tabs or reloads.

Optional flags and env:

- `-listen :8081` — listen address (default `:8081`, or override with `LISTEN_ADDR`)

```bash
./cmd-translate serve -listen 127.0.0.1:3000
```

**Docker** — copy `.env.example` to `.env`, fill in your keys, then:

```bash
docker build -t cmd-translate .
docker run --rm -p 8081:8081 --env-file .env \
  cmd-translate serve -listen :8081
```

**Debugging** — the server logs provider, model, HTTP status, and response snippets:

```bash
docker logs cmd-translate-web 2>&1 | tail -30
```

## Docker (CLI mode)

```bash
docker run --rm --env-file .env \
  cmd-translate "find all go files modified in the last 7 days"
```

For stdin, add `-i`:

```bash
echo "kill whatever is on port 8081" | \
  docker run --rm -i --env-file .env cmd-translate
```

See `build.sh` for more examples.

## How it works

- The assistant replies with shell commands in fenced code blocks
- Dangerous commands (`rm -rf`, `dd`, `mkfs`, etc.) are flagged and require user confirmation
- Session memory keeps the full conversation context until you click **New chat**
- The provider dropdown switches between LM Studio and Google AI Studio; model list auto-syncs
