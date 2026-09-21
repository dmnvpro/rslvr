# ТЗ: консольное приложение
`rslvr` (Go) — захват видеоустройства, распознавание ответа через OpenAI, локальный веб-UI

## 1. Цель

Консольная утилита на Go, которая:

1. Перечисляет доступные видеоустройства (камеры / карты захвата / виртуальные камеры).
2. Умеет снять **один кадр** (скриншот) с выбранного устройства.
3. Отправляет кадр в **OpenAI Vision API** вместе с текстовым промптом («прочитай вопрос и дай
   ответ») и получает распознанный ответ.
4. Параллельно поднимает **локальный HTTP-сервер** на заданном IP:port, который отдаёт одну
   HTML-страницу:
    - выбор видеоустройства (список);
    - кнопка **«Сделать скриншот»**;
    - по нажатию — захват кадра → отправка в OpenAI → ответ отображается **в форме ниже** на той же
      странице.
5. Работает и в headless-режиме (без веб-сервера), и с ним — по флагу.

## 2. Допущения и термины

| Термин в задаче              | Как трактуем                                                                                                                                                          |
|------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| «OpenAI API»                 | **OpenAI API** (Chat Completions, `image_url` с base64)                                                                                                               |
| «скриншот страницы»          | одиночный кадр (frame) с видеоустройства, а не screenshot окна/браузера                                                                                               |
| «поток, запущенный в студии» | источник — карта захвата / виртуальная камера (OBS Virtual Camera и т.п.), ОС видит её как обычное видеоустройство; приложение не взаимодействует со студией напрямую |
| Ответ на вопрос              | текстовый ответ, извлечённый из ответа модели (plain text)                                                                                                            |
| ОС                           | Windows + Linux (V4L2), кроссплатформенно                                                                                                                             |

## 3. Архитектура

```
┌───────────────────────────────────────────────────────────┐
│                        main (cmd/rslvr)                   │
│  парсинг флагов → загрузка конфига → инициализация модулей│
└────────────┬───────────────────────────────┬──────────────┘
             │                               │
   ┌─────────▼──────────┐          ┌─────────▼───────────────┐
   │  internal/capture  │          │  internal/server        │
   │  ListDevices()     │          │  net/http + HTML/JS     │
   │  Grab(deviceIdx)   │◄─────────┤  POST /api/screenshot   │
   └─────────┬──────────┘          │  GET  /api/devices      │
             │ []byte (JPEG)       │  GET  /api/health       │
             │                     └─────────┬───────────────┘
   ┌─────────▼──────────┐          ┌─────────▼───────────────┐
   │  internal/vision   │          │  internal/jobs          │
   │  Analyze(img,      │◄─────────┤  очередь/мьютекс,       │
   │          prompt)   │          │  статусы задач          │
   └─────────┬──────────┘          └─────────────────────────┘
             │ string (answer)
   ┌─────────▼──────────┐
   │  internal/config   │  internal/logx  |  internal/httpx (retry)
   └────────────────────┘
```

Плюс CLI-режим `--once` (сделать один кадр, вывести ответ в stdout и выйти) и `--list-devices`.

## 4. Стек и зависимости

- Go **1.25.6+**, без обязательной cgo-зависимости в базовом варианте.
- Захват кадра — **два взаимозаменяемых бэкенда за одним интерфейсом**:
    - `ffmpeg` как внешний процесс (рекомендуемый дефолт):
      `ffmpeg -f dshow -i video="<name>" -frames:v 1 -q:v 2 -f image2pipe -` (Windows) /
      `-f v4l2 -i /dev/videoN` (Linux). Просто, без cgo, работает почти везде.
- HTTP-сервер — **только `net/http`** (стандартная библиотека). Никаких тяжёлых веб-фреймворков.
- OpenAI — HTTP-клиент (`net/http` + `encoding/json`), без сторонних SDK (стабильнее и прозрачнее).
- UI — один статический HTML-файл с vanilla JS (`go:embed`), без сборщиков и npm.
- Логи — `log/slog` (JSON или text по флагу).
- Конфиг — YAML (`gopkg.in/yaml.v3`) + переопределение флагами и ENV.
- Тесты — стандартный `testing` + `httptest`.

## 5. CLI

```
rslvr [flags]

Флаги:
  --config string            путь к YAML-конфигу (default "./config.yaml")
  --device string            имя или индекс видеоустройства; список — через запятую
  --list-devices             вывести доступные устройства в stdout и выйти
  --once                     сделать один скриншот, вывести ответ, выйти (без веб-сервера)
  --serve                    поднять локальный веб-сервер (default true, если не --once)
  --listen string            адрес веб-сервера, напр. 127.0.0.1:8080
  --prompt string            промпт для модели (переопределяет конфиг)
  --model string             модель OpenAI (default gpt-4o-mini)
  --api-key string           ключ; приоритет: флаг > ENV OPENAI_API_KEY > конфиг
  --output-dir string        куда сохранять копии кадров (default ./shots)
  --timeout duration         таймаут на один цикл (default 30s)
  --log-level string         debug|info|warn|error (default info)
  --open-browser             открыть страницу в браузере при старте
  --version
```

Коды выхода: `0` — успех; `1` — общая ошибка; `2` — нет доступных устройств; `3` — ошибка OpenAI (
нет ключа / сеть / отказ); `4` — таймаут.

## 6. Конфиг (YAML)

```yaml
listen: "127.0.0.1:8080"
capture:
  backend: "ffmpeg"          # ffmpeg | gocv
  ffmpeg_path: "ffmpeg"
  device: ""                 # пусто = первое доступное; можно имя или индекс
  output_format: "jpeg"      # jpeg | png
  jpeg_quality: 85
  save_dir: "./shots"        # "" = не сохранять
vision:
  base_url: "https://api.openai.com/v1"
  api_key_env: "OPENAI_API_KEY"
  api_key: ""                # ⚠ не хранить в git
  model: "gpt-4o-mini"
  timeout: 30s
  max_retries: 3
  retry_backoff: 800ms
  max_tokens: 500
  temperature: 0.0
  detail: "high"             # low | high  (OpenAI image detail)
  system_prompt: |
    Ты — ассистент, который читает текст на изображении.
  user_prompt: |
    На изображении — вопрос викторины. Прочитай вопрос и варианты ответа.
    Ответь одной строкой: сначала буква(ы) правильного варианта, затем через
    вертикальную черту — текст ответа. Если изображение нечитаемо, ответь
    ровно "UNREADABLE".
server:
  read_timeout: 15s
  write_timeout: 60s          # должно быть > timeout vision
  max_concurrent_jobs: 1      # >1 = очередь; 1 = кнопка блокируется на время запроса
  allowed_ips: [ ]             # пусто = без ограничений; иначе CIDR-whitelist
  auth_token: ""              # "" = без авторизации; иначе cookie/заголовок
logging:
  level: "info"
  format: "text"              # text | json
```

## 7. Модуль `internal/capture`

```go
type Device struct {
Index int    `json:"index"`
ID    string `json:"id"` // /dev/video0 | dshow-имя | "0"
Name  string `json:"name"`
Path  string `json:"path,omitempty"`
}

type Capturer interface {
List(ctx context.Context) ([]Device, error)
Grab(ctx context.Context, dev Device) ([]byte, string /*mime*/, error) // JPEG/PNG
Close() error
}
```

Требования:

- `List` на Linux — перебор `/dev/video*` + `v4l2-ctl --list-devices`/ioctl fallback; на Windows —
  `ffmpeg -list_devices true -f dshow -i dummy` с парсингом stderr; на macOS — AVFoundation (
  `ffmpeg -f avfoundation -list_devices true -i ""`).
- `Grab` — обязательный **таймаут из контекста**; убийство дочернего ffmpeg-процесса при отмене (
  `exec.CommandContext` + `Kill` группы процессов).
- Кадр читается в память с ограничением (**лимит 20 МБ**), валидируется как декодируемое
  изображение (`image.DecodeConfig`).
- Ошибки: `ErrDeviceBusy`, `ErrNoSignal`, `ErrTimeout` — с человекочитаемым текстом (устройство
  занято студией — типовой кейс).

## 8. Модуль `internal/vision`

```go
type Result struct {
Answer    string    `json:"answer"`
Raw       string    `json:"raw"` // сырой ответ модели
Model     string    `json:"model"`
LatencyMS int64     `json:"latency_ms"`
Usage     Usage     `json:"usage"`
RequestID string    `json:"request_id"`
ImageHash string    `json:"image_sha256"`
SavedPath string    `json:"saved_path,omitempty"`
}

type Analyzer interface {
Analyze(ctx context.Context, img []byte, mime string) (Result, error)
}
```

- Запрос: `POST {base_url}/chat/completions`, `messages`: `system` + `user` с content-массивом
  `[{"type":"text",...},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,...","detail":"high"}}]`.
- Ключ — только из ENV/флага; **никогда не логировать**, маскировать в debug-логах (`sk-***abc`).
- Ретраи: только на `429`, `5xx`, сетевые ошибки; экспоненциальный backoff с джиттером; уважать
  `Retry-After`.
- Пост-обработка ответа: trim, снятие markdown-обёрток, извлечение ответа по правилам из промпта;
  если ответ `UNREADABLE` — вернуть `ErrUnreadableImage`.
- Логирование: сохранять сырой ответ + `usage` в `debug`-лог.

## 9. Локальный веб-сервер (`internal/server`)

Поднимается параллельно с CLI-режимом, слушает `listen` (по умолчанию `127.0.0.1:8080`).

### Эндпоинты

| Метод  | Путь              | Назначение                                   | Ответ                                                                           |
|--------|-------------------|----------------------------------------------|---------------------------------------------------------------------------------|
| `GET`  | `/`               | HTML-страница (embed)                        | `text/html`                                                                     |
| `GET`  | `/api/devices`    | Список видеоустройств                        | `{"devices":[{"index":0,"id":"...","name":"..."}],"selected":"..."}`            |
| `POST` | `/api/screenshot` | Захват + распознавание                       | `200` → `vision.Result`; `4xx/5xx` → `{"error":{"code":"...","message":"..."}}` |
| `GET`  | `/api/status`     | Текущее состояние: idle/busy, текущий job id | `{"state":"idle","busy":false}`                                                 |
| `GET`  | `/api/events`     | SSE: `state`, `progress`, `result`, `error`  | `text/event-stream`                                                             |
| `GET`  | `/api/health`     | liveness                                     | `{"ok":true}`                                                                   |

Тело `POST /api/screenshot`:

```json
{
  "device": "0",
  "prompt_override": null,
  "save": true
}
```

Поля опциональны; при отсутствии — берутся из конфига.

Коды: `400` bad request, `409` уже идёт захват (`max_concurrent_jobs`), `422` кадр нечитаем/нет
сигнала, `502` ошибка OpenAI, `504` таймаут.

### Страница (UI), один файл, vanilla JS

- Заголовок, состояние: «Готово / Идёт захват… / Ошибка».
- Селект **«Видеоустройство»** — заполняется из `GET /api/devices`, сохраняется в `localStorage`.
- Большая кнопка **«Сделать скриншот»** (`id="btn-shot"`).
- По нажатию: кнопка `disabled` + спиннер, `POST /api/screenshot`, ниже появляется блок:
    - превью отправленного кадра (миниатюра);
    - **форма ниже** — поле/карточка **«Ответ»** с текстом ответа, модель, время ответа, latency;
    - сворачиваемая секция «Сырой ответ модели» и «Ошибка» (если была).
- История последних N=10 ответов внизу страницы (in-memory, без БД).
- Дополнительно: чекбокс «сохранить кадр на диск», поле «переопределить промпт» (collapsible),
  горячая клавиша `Space` = кнопка.
- Страница должна корректно работать в LAN (доступ с другого ПК по `http://<ip>:8080`), если
  `listen: 0.0.0.0:8080`.
- Никаких CDN: весь CSS/JS инлайн, чтобы работало без интернета (кроме самого API).

## 10. Конкурентность и поведение

- Единый `context.Context`, отменяемый по `SIGINT`/`SIGTERM` (`signal.NotifyContext`), graceful
  shutdown HTTP-сервера (`srv.Shutdown`, 5s).
- Захват кадра защищён мьютексом/семафором: **устройство нельзя открыть дважды** — иначе «device
  busy».
- Порядок в одном цикле: `Grab` → (опц. save) → `Analyze` → отдать результат. Общий дедлайн цикла =
  `--timeout`; на OpenAI выделяется `min(vision.timeout, остаток дедлайна)`.
- Кэш по хэшу кадра (SHA-256) с TTL 30s — повторное нажатие по тому же кадру не жжёт токены (
  отключаемо флагом `--no-cache`).
- Никакой утечки горутин: `go test -race`, `-run` сценарии отмены.

## 11. Сборка, поставка, эксплуатация

- Структура репозитория:

```
rslvr/
├── cmd/rslvr/main.go
├── internal/{capture,vision,server,jobs,config,logx}/...
├── web/index.html            # go:embed
├── config.example.yaml
├── shots/.gitkeep
├── Makefile                  # build, run, test, lint, race, cross
├── README.md
└── LICENSE
```

- `Makefile` цели: `build` (в `bin/rslvr`), `build-win` (`GOOS=windows GOARCH=amd64 CGO_ENABLED=0`),
  `build-linux`, `test`, `race`, `lint` (`golangci-lint`), `docker`.
- Системный трей/установщик не требуется. Требуется **один бинарник без внешних зависимостей**,
  кроме `ffmpeg` в PATH (проверять на старте и выдавать внятную ошибку, если не найден).
- Проверка предусловий на старте: `ffmpeg` доступен → устройства найдены → API-ключ валиден (лёгкий
  `GET /models`, при `401` — внятная ошибка) → порт свободен.
- README: установка ffmpeg (Windows/Linux), примеры запуска, troubleshooting («нет сигнала»,
  «устройство занято», «камера отдаёт чёрный кадр — увеличить warmup_ms», «invalid_api_key»).

## 12. Тесты

- Unit: парсинг вывода `ffmpeg -list_devices`, парсинг/нормализация ответа модели, даунскейл и
  кодирование кадра, конфиг (флаги > ENV > YAML), retry-логика (на `httptest`-сервере с 429/500).
- Integration: `httptest.Server` как fake OpenAI — проверяем полный путь `POST /api/screenshot` →
  результат в JSON → рендер (проверка ключевых подстрок HTML).
- Захват видео — за интерфейсом `Capturer`, в тестах используется `fakeCapturer` (отдаёт
  детерминированный JPEG 1×1 и PNG-фикстуру с текстом вопроса). **Тесты не должны требовать реальной
  камеры.**
- Обязательно: `go vet ./...`, `golangci-lint run`, `go test -race ./...` — зелёные.

## 13. Критерии приёмки

1. `rslvr --list-devices` печатает устройства и завершается с кодом 0; при их отсутствии — код 2 и
   понятное сообщение.
2. `rslvr --once` делает кадр, выводит ответ модели в stdout, сохраняет кадр (если `save_dir`задан),
   завершается < `--timeout`.
3. `rslvr --serve` поднимает страницу по `--listen`; страница открывается, селект содержит реальные
   устройства.
4. Нажатие «Сделать скриншот»: кнопка блокируется, через N секунд **ниже кнопки в форме**
   отображается ответ, превью кадра и latency. Повторное нажатие работает.
5. При ошибке API/устройства страница показывает понятную ошибку, а не «висит»; кнопка
   разблокируется.
6. `curl -X POST localhost:8080/api/screenshot -d '{}'` возвращает JSON с полем `answer` (то же, что
   на странице).
7. `Ctrl+C` корректно завершает процессы ffmpeg, сервер гасится, код выхода 0.
8. Бинарник собирается для windows/amd64 и linux/amd64 без cgo; размер < 25 МБ.
9. Ключ API отсутствует во всех логах, файлах страницы и в выводе консоли.
10. `go test -race ./...` и `golangci-lint run` — без ошибок; покрытие модулей `vision`, `config`,
    `capture` (парсинг) ≥ 70 %.
11. README позволяет новому человеку запустить проект на Windows за ≤ 10 минут.


