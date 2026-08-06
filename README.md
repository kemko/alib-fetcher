# alib-fetcher

Always-on Go service that fetches the latest listings from
[`alib.ru/tramka.phtml?tnew=7`](https://www.alib.ru/tramka.phtml?tnew=7) and
sends unseen books to a Telegram chat on a configurable cron schedule. Delivered
listings are deduplicated by their unique `Купить` link in an embedded bbolt
database.

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | yes | - | Bot token from BotFather |
| `TELEGRAM_CHAT_ID` | yes | - | Numeric chat ID or `@channel` username |
| `CRON_SCHEDULE` | no | `0 0 * * *` | Standard five-field cron expression |
| `TIMEZONE` | no | `Europe/Moscow` | IANA timezone used by the scheduler |
| `STATE_PATH` | no | `/var/lib/alib-fetcher/state.db` | bbolt state database |
| `ALIB_URL` | no | source URL above | Listing page, also useful for testing |
| `TELEGRAM_API_BASE` | no | `https://api.telegram.org` | Bot API base URL |
| `HTTP_TIMEOUT` | no | `30s` | Timeout for each external request |
| `MESSAGE_LIMIT` | no | `4000` | Safe Telegram message size, max `4096` |

The first successful run sends every listing currently present on the source
page. Later runs send only links that have not been acknowledged in the state
database. A chunk is acknowledged only after Telegram accepts it. State entries
older than 14 days are removed once at the beginning of every digest cycle.
Each Telegram listing keeps the full Alib announcement text and its seller and
purchase links. The source photo-link section is replaced with `Фото: есть` or
`Фото: нет`. When a digest is split into multiple messages, only the final
message uses the normal notification sound; earlier messages are silent.

When Telegram returns a flood-control `retry_after`, the service waits for the
specified duration and retries the same message before continuing with later
chunks. Waiting stops promptly when the service context is canceled. Books are
recorded as delivered only after their containing chunk is accepted.

## Run

Run one cycle locally:

```bash
TELEGRAM_BOT_TOKEN=... \
TELEGRAM_CHAT_ID=... \
STATE_PATH=./data/state.db \
go run ./cmd/alib-fetcher -once
```

Run the scheduler:

```bash
TELEGRAM_BOT_TOKEN=... \
TELEGRAM_CHAT_ID=... \
CRON_SCHEDULE='*/30 * * * *' \
STATE_PATH=./data/state.db \
go run ./cmd/alib-fetcher
```

Service mode runs one digest cycle immediately after startup and then continues
using `CRON_SCHEDULE` in the configured timezone. The five fields are minute,
hour, day of month, month, and day of week; descriptors such as `@hourly` are
also accepted. The state database is open only while a digest cycle is running,
so a separate `-once` invocation can use it between scheduled cycles.

The process emits structured JSON logs and stops gracefully on `SIGINT` or
`SIGTERM`.

## Container

Successful CI runs for pushes to `master` publish images as
`ghcr.io/<owner>/<repository>:latest`. Pull requests and failed verification or
vulnerability checks never publish an image. The final image runs as the
distroless `nonroot` user. Keep the state directory on a named volume:

```bash
docker run -d --name alib-fetcher \
  --read-only \
  --mount type=volume,src=alib-fetcher-state,dst=/var/lib/alib-fetcher \
  -e TELEGRAM_BOT_TOKEN=... \
  -e TELEGRAM_CHAT_ID=... \
  ghcr.io/<owner>/<repository>:latest
```

Alternatively, start the service with the Compose v3.8 configuration:

```bash
export TELEGRAM_BOT_TOKEN=...
export TELEGRAM_CHAT_ID=...
docker compose up -d
```

With Podman Desktop, use `podman compose up -d`. Compose stores the database in
the persistent named volume `alib-fetcher-state`. Set `ALIB_FETCHER_IMAGE` to
override the default `ghcr.io/kemko/alib-fetcher:latest` image.

## Development

Go 1.26.5 is required. Run the complete non-mutating quality gate:

```bash
make verify
```

Use `make fmt` to apply formatting. `make verify` checks formatting, runs strict
linting and race-enabled tests, and builds `bin/alib-fetcher`. Quality targets
automatically provision and use the pinned golangci-lint under ignored
`bin/tools`; an unrelated binary earlier in `PATH` cannot affect verification.
CI uses the same Make targets and additionally runs `govulncheck`.

## Security-only dependency updates

Dependabot is configured for Go modules, Docker, and GitHub Actions with normal
version-update pull requests disabled. Enable **Dependabot alerts** and
**Dependabot security updates** under repository Settings > Security > Code
security and analysis. Security advisories can then open update pull requests;
new versions without a known vulnerability do not create pull requests.
