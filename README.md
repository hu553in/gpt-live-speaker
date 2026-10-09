# GPT-Live speaker

[![CI](https://github.com/hu553in/gpt-live-speaker/actions/workflows/ci.yml/badge.svg)](https://github.com/hu553in/gpt-live-speaker/actions/workflows/ci.yml)
[![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/hu553in/gpt-live-speaker)](https://github.com/hu553in/gpt-live-speaker/blob/main/go.mod)

A DIY smart speaker on OpenAI `gpt-live-1`. Say "hey Jarvis" from the couch, talk, and cut it off
mid-sentence like a person; it hangs up on its own when the conversation goes quiet. A headless Go
daemon for a USB speakerphone, meant to run on a Raspberry Pi.

## What it does

- Waits for a wake word with a local, offline detector, then beeps and opens a GPT-Live session; the
  request can follow the wake word in the same breath
- Streams microphone audio to `gpt-live-1` over a WebSocket and plays the answer through the
  speakerphone
- Hands questions that need current facts or careful reasoning to a backend model through Responses
  delegation, with web search, and tells both models the current date, time, and location if set
- Hangs up when you say goodbye, after a minute without speech, or after 10 minutes at most, and
  carries the conversation over to the next session for an hour; the timings are configurable
- Logs each turn, the session's voice seconds and cost, and every wake word attempt's score

The speaker works in Russian: the prompts for both models, the assistant's speech, and the backend's
answers. The voice prompt lets you ask it to switch languages during a conversation, but there is no
setting for the default language yet; to change it, edit the prompts in
[`internal/speaker/prompt.go`](internal/speaker/prompt.go). The wake words are English either way.

It has no timers, alarms, music, or device control yet, and says so when asked.

> [!IMPORTANT]
>
> GPT-Live bills every second a session is open, including silence, at $0.05 per minute. The backend
> model and web search are billed separately.

## Requirements

- Go, using the version declared in [`go.mod`](go.mod) or newer, and a C compiler for cgo
- [uv](https://docs.astral.sh/uv/), which installs the wake word detector on the first run
- Make
- A USB speakerphone with hardware echo cancellation; see [Speakerphone](#speakerphone)
- An OpenAI API key with GPT-Live access

## Speakerphone

The speaker is built and tested with the EMEET OfficeCore M0 Plus, a USB-C conference speakerphone.

- **Why this one:** EMEET specifies four MEMS microphones with 360° pickup, hardware echo
  cancellation and noise suppression, and full-duplex audio, so it hears you while it talks.
- **Tested:** interrupting the model mid-answer works on the speakerphone's own processing alone,
  without any software echo cancellation, which is exactly what a headless device gets.
- **Audio:** its microphone delivers only 16 kHz mono, the wake word detector's native rate, so the
  speaker captures it without resampling.
- **Connection:** use USB, not Bluetooth; Bluetooth headset profiles lower voice quality and add
  latency. macOS lists it as `EMEET OfficeCore M0 Plus`.
- **Wake word:** through it, the pretrained "hey jarvis" model scored real attempts anywhere from
  below 0.3 to 0.95, which is why `WAKE_THRESHOLD` defaults to 0.5.

Other speakerphones that show up as a standard USB audio device and cancel echo in hardware should
work the same way, but none has been tested.

## Setup

1. Copy `.env.example` to `.env` and set `OPENAI_API_KEY`.
2. Connect the speakerphone over USB and set `AUDIO_DEVICE` to part of its name, such as `EMEET`.
3. Run `make run`.

## Configuration

| Name             | Required | Default        | Description                                                                                                                      |
| ---------------- | -------- | -------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `OPENAI_API_KEY` | Yes      | -              | OpenAI API key with access to GPT-Live                                                                                           |
| `AUDIO_DEVICE`   | No       | system default | Part of the speakerphone's name, such as `EMEET`; the speaker lists the available names if nothing matches                       |
| `WAKE_WORD`      | No       | `hey_jarvis`   | One of the pretrained wake words: `hey_jarvis`, `alexa`, `okay_nabu`, `hey_mycroft`                                              |
| `WAKE_THRESHOLD` | No       | `0.5`          | Score between 0 and 1 above which the wake word counts; lower it if the speaker misses you, raise it if it wakes up on its own   |
| `IDLE_TIMEOUT`   | No       | `1m`           | How long a session waits without speech before it closes                                                                         |
| `MAX_SESSION`    | No       | `10m`          | Hard limit for one session, so a TV that keeps talking cannot run up the bill                                                    |
| `HISTORY_TTL`    | No       | `1h`           | How long the conversation carries over to the next session; `0` starts every session fresh                                       |
| `BACKEND_MODEL`  | No       | `gpt-6-luna`   | Responses model for delegated questions, such as `gpt-6-sol` for harder tasks                                                    |
| `LOCATION`       | No       | -              | Where the speaker stands, such as `Омск`, for weather and other local questions; the model always gets the current date and time |
| `LOG_LEVEL`      | No       | `info`         | `debug` adds a per-second microphone report: chunks, peak level, and chunks dropped on the way to the detector                   |

Durations use Go syntax, such as `90s` or `5m`.

## Usage

1. Wait for `listening` in the log. The first run downloads the wake word detector.
2. Say the wake word, wait for the beep, and talk. Interrupt whenever you like.
3. Say goodbye, such as "пока" or "всё, спасибо", to hang up right away, or just stop talking: the
   session closes after `IDLE_TIMEOUT`. Either way the speaker listens for the wake word again.
4. Press Ctrl+C to quit. A running session closes first, so it stops billing.

Each line of the conversation log has `at`, the moment it was said, counted from the start of the
session. Your words are transcribed with a delay, so they can appear after the reply; `at` shows the
real order.

## Troubleshooting

- **The wake word does not trigger:** the pretrained models are trained for English and score real
  voices unevenly. Each attempt that scores at least 0.3 logs `wake word score`; set
  `WAKE_THRESHOLD` a little below your usual scores. If clear, close attempts log nothing, run with
  `LOG_LEVEL=debug` to check that the microphone delivers sound.
- **`microphone delivers pure silence`:** the speakerphone is muted, or the terminal may not use the
  microphone. On macOS, allow it in System Settings > Privacy & Security > Microphone.
- **`microphone delivers no audio at all`:** `AUDIO_DEVICE` matches the wrong device, or the
  speakerphone is disconnected. Run with `LOG_LEVEL=debug` to see the microphone level every second.
- **Interruptions take effect late:** the session usage line reports `max_playback_queue`, the
  answer audio that piled up ahead of playback. The speaker finishes that much before it goes quiet.

## Development

Requirements in addition to Go and uv:

- Bun
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
make check-types
make check-deps
make check-vulns
make test
```

Build the binary into `dist/`:

```bash
make build
```

The wake word detector is a small Python project in `internal/wakeword`, locked with `uv.lock`. The
binary embeds it and unpacks it into the user cache directory, so a device needs only uv installed.
