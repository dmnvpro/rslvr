# rslvr // resolver
`rslvr` - tool that helps to answer tests questions.

```text
              .__                
_______  _____|  |___  _________ 
\_  __ \/  ___/  |\  \/ /\_  __ \
 |  | \/\___ \|  |_\   /  |  | \/
 |__|  /____  >____/\_/   |__|   
            \/                   
```

`rslvr` grabs frames from a video device (webcam, capture card, OBS
Virtual Camera), sends them to an OpenAI-compatible vision model, and shows the
model's answer to the quiz question on a local web page.

Platforms: **Linux** (V4L2) and **Windows** (DirectShow) are supported; macOS
is not. No cgo — the binary cross-compiles cleanly; ffmpeg is the only
external dependency (see [docs/adr/0001](docs/adr/0001-capture-via-external-ffmpeg.md)).

---

## Requirements

- **ffmpeg** on PATH (frame capture + device listing on Windows)
- Any OpenAI-compatible API key (OpenRouter, native OpenAI, local proxies)

### Install ffmpeg

**Windows**

```powershell
winget install Gyan.FFmpeg
# or download https://www.gyan.dev/ffmpeg/builds and add bin\ to PATH
```

**Linux (Debian/Ubuntu)**

```bash
sudo apt install ffmpeg
```

Verify: `ffmpeg -version`.

---

## Build

```bash
make build          # bin/rslvr (host platform)
make build-win      # bin/rslvr-windows-amd64.exe (CGO_ENABLED=0)
make cross          # both
```

Or plain Go: `go build -o bin/rslvr ./cmd/rslvr` (Go 1.25+).

---

## Quickstart (≤ 10 minutes)

1. Install ffmpeg (above), then:

   ```bash
   cp .env.example .env       # put your key into OPENAI_API_KEY=
   make build
   ```

2. See what devices you have:

   ```bash
   ./bin/rslvr --list-devices
   # [0]  /dev/video0  USB Camera            (Linux)
   # [0]  USB2.0 HD UVC WebCam               (Windows)
   ```

3. Run the web UI and resolve quiz questions:

   ```bash
   ./bin/rslvr                       # listens on http://127.0.0.1:8080
   ./bin/rslvr --listen 0.0.0.0:8080 # reachable from the LAN
   ./bin/rslvr --open-browser
   ```

On the page: pick the device, press **Take screenshot** (or hit `Space`) for
every screen of the question — the numbered frames pile up below — then press
**Resolve** to send the whole set to the model in one request. The answer, the
question it recognized, the options, an explanation and the frame preview
appear below. **New question** clears the set. The last 10 answers stay in the
in-memory history and can be downloaded as a text file.

---

## Configuration

Precedence: **flags > environment (incl. `.env`) > `config.yaml` > defaults**.
A missing `./config.yaml` is fine (built-in defaults); a missing explicitly
passed `--config` path is an error. See `config.example.yaml` for every key
and `.env.example` for the key storage.

The key is read from `OPENAI_API_KEY` (or the name configured in
`vision.api_key_env`). It is never logged — debug logs carry a masked form
(`sk-***abc`). The default endpoint is OpenRouter
(`https://openrouter.ai/api/v1`, model `openai/gpt-5-image-mini`); point
`vision.base_url` at any OpenAI-compatible `/chat/completions` provider and
adjust the model id (aggregators use `vendor/model`, native OpenAI does not).

Useful flags (full list: `--help`):

| Flag | Meaning |
|---|---|
| `--device a,b` | filters the UI device dropdown |
| `--prompt "..."` | override the user prompt for this run |
| `--model`, `--api-key` | override the config |
| `--timeout 30s` | overall deadline of one grab+analyze cycle |
| `--no-cache` | disable the 30s answer cache for identical frames |
| `--output-dir ""` | disable saving frame copies |
| `--log-level debug` | verbose logs (raw model replies, usage) |

Exit codes: `0` success, `1` general error, `2` no video devices, `3` API
error (no key / rejected key / upstream / unreadable frame), `4` timeout.

---

## HTTP API

| Method | Path | Purpose |
|---|---|---|
| `GET /` | | single-page UI (inline CSS/JS, works offline) |
| `GET /api/devices` | | `{"devices":[{index,id,name,path}],"selected":"..."}` |
| `POST /api/frame` | | grab one frame without analyzing: body `{"device","save"}` optional; returns `{mime,sha256,image,preview,saved_path}` |
| `POST /api/screenshot` | | analyze an ordered frame set: body `{"frames":[{"data":"<base64>"},...],"prompt_override"}`; returns the result JSON |
| `GET /api/status` | | `{"state":"idle"\|"busy","busy":bool,"job_id"}` |
| `GET /api/health` | | `{"ok":true}` (exempt from auth) |

Error mapping: `400` bad JSON, `409` another capture is running (slot pool,
no queue) or the device is busy, `422` no signal / model replied UNREADABLE,
`502` upstream API error, `504` deadline exceeded.

Security: `server.allowed_ips` is a CIDR whitelist evaluated against the
socket address (`X-Forwarded-For` is ignored on purpose); `server.auth_token`
enables Bearer / `X-Auth-Token` / cookie auth — open `/?token=...` once and
the page remembers it.

---

## Troubleshooting

- **`no video devices found`** — check `--list-devices`; on Linux verify the
  user can read `/dev/video*` (add yourself to the `video` group).
- **`video device is busy`** — another app (OBS, browser, a studio) holds the
  camera. Close it or point rslvr at a virtual camera.
- **`no signal from video device`** — a capture card without a feed on the
  selected input; switch the input on the card or check the cable.
- **Black / unfocused first frame** — increase `capture.warmup_ms` (default
  700ms); capture cards often need a moment for auto-exposure after open.
- **`API authentication failed`** — the key is missing or rejected. At startup
  a rejected key is fatal; a network failure only warns and retries per
  request.
- **Page is empty from another PC** — start with `--listen 0.0.0.0:8080` and
  check `server.allowed_ips`.

---

## Development

```bash
make verify        # vet + tests + race detector
make cover         # coverage of config / vision / capture (gate: >= 70%)
make lint          # golangci-lint (auto-installed pinned version)
make e2e-prepare   # sudo: v4l2loopback fake camera + testsrc feed
make e2e           # run the real binary against the fake camera
```

Tests never require a camera or an API key: capture and vision sit behind the
`Capturer` / `Analyzer` interfaces and fakes cover the pipeline.

Project layout:

```
cmd/rslvr/          CLI entry point
internal/app/       modes, preflight checks, exit codes
internal/capture/   ffmpeg backend, V4L2 enumeration, dshow parsing
internal/vision/    OpenAI-compatible client, retries, answer normalization
internal/jobs/      slot pool, frame pipeline, answer cache, shot saving
internal/server/    HTTP API, CIDR/token middleware, embedded UI
internal/config/    flags > env > .env > YAML > defaults
web/index.html      the whole UI (go:embed)
docs/adr/           architecture decisions
```

---

## License
This project is licensed under The Unlicense License - see the [LICENSE](LICENSE) file for details.
