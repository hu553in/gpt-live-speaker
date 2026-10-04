# GPT-Live speaker

[![CI](https://github.com/hu553in/gpt-live-speaker/actions/workflows/ci.yml/badge.svg)](https://github.com/hu553in/gpt-live-speaker/actions/workflows/ci.yml)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/hu553in/gpt-live-speaker)](https://github.com/hu553in/gpt-live-speaker/blob/main/go.mod)

Talk to OpenAI `gpt-live-1` through any microphone and speaker, and cut it off mid-sentence like a
person. A local test bench for a DIY smart speaker: check that a USB speakerphone hears you over the
model's own voice before building hardware around it.

## What it does

- Connects Chrome to `gpt-live-1` over WebRTC; a small Go server keeps the API key and creates
  sessions
- Hands questions that need careful reasoning to a `gpt-6-luna` backend through Responses delegation
- Lets you choose the microphone and speaker, and toggle the browser's echo cancellation, noise
  suppression, and automatic gain control
- Streams your transcript and the model's transcript side by side, so overlapping speech is visible
- Records the microphone audio the browser sends to the model and offers it as a `.webm` download
- Shows billed voice seconds and their cost
- Closes the session automatically after 10 minutes

The interface and the assistant speak Russian.

> [!IMPORTANT]
>
> GPT-Live bills every second a session is open, including silence. Press Stop when you are done.

## Requirements

- Go, using the version declared in [`go.mod`](go.mod) or newer
- Bun
- Make
- Google Chrome
- An OpenAI API key with GPT-Live access

## Setup

1. Copy `.env.example` to `.env` and set `OPENAI_API_KEY`.
2. Run `make run`.

Open <http://localhost:3000>. Use `localhost`, not `127.0.0.1`: the server accepts session requests
only from that origin.

## Configuration

| Name             | Required | Default | Description                            |
| ---------------- | -------- | ------- | -------------------------------------- |
| `OPENAI_API_KEY` | Yes      | -       | OpenAI API key with access to GPT-Live |

## Usage

1. Allow microphone access when the page asks; device names appear after that.
2. Choose the microphone and speaker, then press Start.
3. Talk when the status reads "Можно говорить". Interrupt whenever you like.
4. Press Stop, then download the microphone recording if you need it.

## Testing a speakerphone

1. Make the speakerphone the macOS default input and output, and select it in both lists. Connect it
   over USB, not Bluetooth.
2. Run one session with all three browser checkboxes on and one with them off, at the same volume
   and distance.
3. Interrupt the model in both runs and compare the recordings. The run with the checkboxes off
   shows how the speakerphone performs on its own, as it would in a headless device.

## Development

Requirements in addition to Go and Bun:

- golangci-lint v2
- [prek](https://prek.j178.dev/)

```bash
make install-deps
prek install
make check
```

Use `make check-fix` to apply formatting before running the same full gate.

Focused checks:

```bash
make lint
make lint-fix
make check-web
make check-deps
make check-vulns
make test
```

Build the binary into `dist/`:

```bash
make build
```
