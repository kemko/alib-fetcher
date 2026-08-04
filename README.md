# alib-fetcher

Always-on Go service that fetches the latest listings from
[`alib.ru/tramka.phtml?tnew=7`](https://www.alib.ru/tramka.phtml?tnew=7) and
sends unseen books to a Telegram chat once per day. Delivered listings are
deduplicated by their unique `Купить` link in an embedded bbolt database.

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | yes | - | Bot token from BotFather |
| `TELEGRAM_CHAT_ID` | yes | - | Numeric chat ID or `@channel` username |
| `RUN_AT` | no | `00:00` | Daily run time in `HH:MM` format |
| `TIMEZONE` | no | `Europe/Moscow` | IANA timezone used by the scheduler |
| `STATE_PATH` | no | `/tmp/alib-fetcher/state.db` | bbolt state database |
| `ALIB_URL` | no | source URL above | Listing page, also useful for testing |
| `TELEGRAM_API_BASE` | no | `https://api.telegram.org` | Bot API base URL |
| `HTTP_TIMEOUT` | no | `30s` | Timeout for each external request |
| `MESSAGE_LIMIT` | no | `4000` | Safe Telegram message size, max `4096` |

The first successful run sends every listing currently present on the source
page. Later runs send only links that have not been acknowledged in the state
database. A chunk is acknowledged only after Telegram accepts it.

## Run

Run one cycle locally:

```bash
TELEGRAM_BOT_TOKEN=... \
TELEGRAM_CHAT_ID=... \
go run ./cmd/alib-fetcher -once
```

Run the scheduler:

```bash
TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=... go run ./cmd/alib-fetcher
```

The process emits structured JSON logs and stops gracefully on `SIGINT` or
`SIGTERM`.

## Container

Images built from `master` are published as
`ghcr.io/<owner>/<repository>:latest`. The final image runs as the distroless
`nonroot` user. Keep the state directory on a named volume, even though its
in-container location is under `/tmp`:

```bash
docker run -d --name alib-fetcher \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --mount type=volume,src=alib-fetcher-state,dst=/tmp/alib-fetcher \
  -e TELEGRAM_BOT_TOKEN=... \
  -e TELEGRAM_CHAT_ID=... \
  ghcr.io/<owner>/<repository>:latest
```

## Development

Go 1.26.5 and golangci-lint v2 are required.

```bash
make verify
```

CI runs strict linting, race-enabled tests, a build, and `govulncheck`.

## Security-only dependency updates

Dependabot is configured for Go modules, Docker, and GitHub Actions with normal
version-update pull requests disabled. Enable **Dependabot alerts** and
**Dependabot security updates** under repository Settings > Security > Code
security and analysis. Security advisories can then open update pull requests;
new versions without a known vulnerability do not create pull requests.
