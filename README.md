# alib-fetcher

Always-on Go service that fetches the latest listings from one or more Alib
pages, defaulting to
[`alib.ru/tramka.phtml?tnew=7`](https://www.alib.ru/tramka.phtml?tnew=7), and
delivers unseen books to a Telegram chat as Rich Messages on a configurable cron
schedule. Delivered listings are tracked by their unique `Купить` link in an
embedded bbolt database, which also stores the pending send queue.

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `TELEGRAM_BOT_TOKEN` | yes | - | Bot token from BotFather |
| `TELEGRAM_CHAT_ID` | yes | - | Signed decimal `int64` chat ID or non-empty `@channel` username, without whitespace |
| `CRON_SCHEDULE` | no | `0 0 * * *` | Standard five-field cron expression |
| `TIMEZONE` | no | `Europe/Moscow` | IANA timezone used by the scheduler and publication-year markers |
| `RUN_ON_STARTUP` | no | `true` | Run one digest cycle immediately after scheduler startup |
| `FRESH_BOOKS` | no | empty | Optional `✨` threshold: `age:N` or `since:YYYY` |
| `STATE_PATH` | no | `/var/lib/alib-fetcher/state.db` | bbolt state database |
| `ALIB_URL` | no | source URL above | One HTTP(S) listing page, or a comma-separated list of pages |
| `ALIB_REQUEST_INTERVAL` | no | `1s` | Non-negative Go duration between sequential Alib page requests; `0s` disables the delay |
| `TELEGRAM_API_BASE` | no | `https://api.telegram.org` | Bot API base URL; custom/local servers require Bot API 10.1+ |
| `HTTP_TIMEOUT` | no | `30s` | Positive Go duration applied to each external request |
| `MESSAGE_LIMIT` | no | `4000` | Safe Telegram message size, max `4096` |

Configuration is validated before process startup. Invalid chat IDs, including
plain text without `@`, an empty `@` username, whitespace, and numeric overflow,
fail fast with an error naming `TELEGRAM_CHAT_ID`.
`ALIB_URL` accepts one URL or a comma-separated list. Surrounding whitespace is
trimmed, URL order is preserved, and literal commas must be percent-encoded as
`%2C`. URL userinfo is rejected. For example:

```bash
ALIB_URL='https://example.com/first?tag=one&sort=new, https://example.com/second?tnew=7' \
ALIB_REQUEST_INTERVAL=2s
```

Pages are downloaded sequentially through one HTTP client. GET parameters are
preserved. The interval applies only between requests, including after a failed
page; a single URL is not delayed. The client completes all download attempts
before parsing any successful response. Responses larger than 4 MiB are rejected
as download failures. The client then parses responses in URL order and combines
listings in first-seen order, deduplicated by their `Купить` URL while keeping
the first copy. Each page has separate download and parse events:
`alib.page_downloaded` or `alib.page_download_failed`, followed for a successful
download by `alib.page_parsed` or `alib.page_parse_failed`. Every event has the
zero-based `index` and full configured `url`, including GET parameters and
fragments; parsed events also have `books`, and failed events have `error`. A
download failure has no parse event. Userinfo is rejected during configuration.
A valid search page with no listings counts as a successful empty result. The
fetch fails only when no page parses successfully or the context is canceled;
successful pages still produce a partial result when other pages fail.
`FRESH_BOOKS=age:N` marks publication years from the current local year minus
non-negative `N`, inclusive. For example, in 2026, `age:5` includes 2021.
`FRESH_BOOKS=since:YYYY` uses the given four-digit year as the inclusive lower
boundary. An absent or empty value disables only the optional `✨` marker; it
does not disable `🔥`. The cycle time converted to `TIMEZONE` determines the
current year and whether the January exception applies:

- `🔥` marks the current year and, during January, the previous year;
- `✨` marks other recognized years between the configured inclusive boundary
  and the current year;
- `🛸` marks any recognized year greater than the current year, independently of
  `FRESH_BOOKS`;
- unrecognized publication years receive no marker.

The January `🔥` rule applies even when `FRESH_BOOKS` is empty or its configured
boundary excludes the previous year.

The parser recognizes the last four-digit year in the bibliography followed by
`г` or `г.`. Years found only in content or other listing sections do not affect
freshness markers.

Each digest first records every fetched listing in the state database as a
pending record with the full semantic Alib payload: title, bibliography,
publication year, content, seller name and URL, location, price, condition and
other details, purchase URL, and photo marker. The parser derives these fields
from DOM nodes and logical `<br>`-delimited lines; it does not parse HTML with
regular expressions. Existing records keep their sent status while refreshing
the parsed payload from the latest source pages. The first successful run records
every listing currently present on those pages as pending and sends them.

Sending reads every pending record from the database in first-discovery order,
not only books found in the current fetch result. Books that could not be sent
remain pending across later digest cycles. A chunk is acknowledged only after
Telegram accepts it, and then its records become sent. Sent records older than
14 days are removed once at the beginning of every digest cycle; pending records
are not removed by retention pruning. If one pending listing cannot fit in a
Telegram message, other renderable pending listings are still sent while the
oversized listing remains pending and the cycle reports the rendering error.
Telegram operations use the pinned
[`github.com/go-telegram/bot`](https://github.com/go-telegram/bot) v1.23.0 SDK.
Each Telegram Rich Message is sent through its `SendRichMessage` method with
rendered HTML. The SDK also supplies request models, inline keyboards, callback
answers, reply-markup edits, and polling machinery; digest ordering,
acknowledgement, flood-control retry, chat filtering, and runner locking remain
service policy. Custom `TELEGRAM_API_BASE` endpoints and test doubles must accept
the SDK's `multipart/form-data` requests.
Each listing inside that HTML is structured as:

1. freshness marker, bold title, and bibliography;
2. content in its own paragraph, when present;
3. seller as `Продавец: <a href="...">Name</a>, Location.`, then price,
   condition/other details, and photo status on separate lines;
4. a final `Купить` link in its own paragraph.

The source photo-link section is replaced with `Фото: есть` or `Фото: нет`.
When seller URL is absent, seller name is rendered as plain text. Missing
optional fields do not create empty paragraphs. Dynamic text and URLs are
HTML-escaped. Paragraphs use `<p>`. Every encoded line break uses `<br/>`, and
non-empty paragraph blocks use one `<br/>` separator; rendered Telegram HTML
contains no literal CR or LF characters. A listing with content uses
`main → <br/> → content → <br/> → details`; without content it uses exactly
`main → <br/> → details`. The final `Купить` paragraph also has one `<br/>`
separator before it. Adjacent listings in one Rich Message
are separated by `<hr/>`; no divider appears before the first listing or after
the last. A digest uses multiple Telegram messages only when pending content is
split into chunks by `MESSAGE_LIMIT`; the heading appears only in the first
message.
If the heading and first pending listing cannot fit together but the listing
fits alone, the first message contains only the heading and the listing follows
in a headerless message.
Only the final message uses the normal notification sound; earlier messages are
silent.
Whenever a digest sends at least one message, the final message includes an
inline `Обновить` button.
Pressing it asks a running service with the same bot token to start one
out-of-schedule digest. If that refresh sends new notifications, the clicked
message's old button is removed before the first new message is sent, and the
last new message receives a fresh `Обновить` button. If the refresh finds no
sendable books, the old button stays in place.
The callback stays in Telegram's loading state until the refresh digest
finishes. A successful refresh with no newly discovered records shows the
`Новых книг нет` toast; a successful refresh with new records ends loading
with no text; a failed refresh shows `Ошибка обновления`, while details remain
in the service log. Refresh digests are canceled after 10 seconds so Telegram
can receive the final callback answer before the query expires; a timeout is a
failed refresh. Toast display duration is controlled by the Telegram client;
the Bot API cannot guarantee an exact duration.

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

Forget the latest records from the local state database:

```bash
STATE_PATH=./data/state.db \
go run ./cmd/alib-fetcher -forget-latest 6
```

This maintenance command deletes up to six records with the greatest discovery
order and exits immediately. It opens only `STATE_PATH`, so it does not require
Telegram credentials, contact Alib or Telegram, or start the scheduler or
callback polling. The deletion is irreversible and applies to both sent and
pending records; the next digest can discover any still-available deleted books
on Alib again. If the database contains fewer records than requested, all
available records are deleted. The value must be positive, and
`-forget-latest` cannot be combined with `-once`.

Run the scheduler:

```bash
TELEGRAM_BOT_TOKEN=... \
TELEGRAM_CHAT_ID=... \
CRON_SCHEDULE='*/30 * * * *' \
STATE_PATH=./data/state.db \
go run ./cmd/alib-fetcher
```

By default, service mode runs one digest cycle immediately after startup and
then continues using `CRON_SCHEDULE` in the configured timezone. Set
`RUN_ON_STARTUP=false` to wait for the first scheduled cycle instead. This does
not affect `-once`. The five cron fields are minute, hour, day of month, month,
and day of week; descriptors such as `@hourly` are also accepted. The state
database is open only while a digest cycle is running, so a separate `-once`
invocation can use it between scheduled cycles.

Service mode starts SDK-managed polling for Telegram callback updates matching
the `Обновить` button. The SDK owns update offsets and polling retry/backoff.
The `-once` command sends the button when it sends books, but exits without
starting callback polling. A running service that uses the same bot can process
a button sent earlier by `-once`. Refresh callbacks from other chats are
answered and ignored when their numeric chat ID or public `@channel` username
does not match `TELEGRAM_CHAT_ID`. Do not configure a Telegram webhook or
another `getUpdates` poller for the same bot token, or refresh callbacks may be
consumed outside this service.

The process emits structured JSON logs and stops gracefully on `SIGINT` or
`SIGTERM`.

On first run after upgrading from older timestamp-marker releases, raw legacy
state entries are migrated to JSON records. Structured records from releases
that stored `text_before_seller`, `text_before_buy`, and `text_after_buy` remain
readable: a narrow JSON compatibility decoder converts those fragments to the
semantic `Book` model in memory. Opening the database does not rewrite valid
legacy structured records. The next mutating write, including rediscovery or a
successful-delivery acknowledgement, writes the current schema. Values that look
like structured JSON records must decode successfully, and their stored purchase
URL must match the bbolt key. A malformed or mismatched structured record makes
state opening fail transactionally: it is not treated as a legacy marker, and no
neighboring migration is committed. Back up `STATE_PATH` before upgrading. If
validation fails, stop the service and restore a known-good backup; recreate the
database only when resetting delivery history is acceptable. Rolling back to an
older release also requires restoring or recreating the state database.

## Container

After verification succeeds, CI validates the Compose configuration and builds
the production image for both pull requests and pushes to `master`. Pull
requests build without registry login or push. A successful `master` push uses
that single build to publish `ghcr.io/<owner>/<repository>:latest`; failed
verification or vulnerability checks publish nothing. The final image runs as
the distroless `nonroot` user. Keep the state directory on a named volume:

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

With required Telegram variables already supplied by the environment or an
untracked `.env`, runtime settings can be overridden without putting credentials
in the command or Compose file:

```bash
FRESH_BOOKS=age:5 \
TIMEZONE=Europe/Moscow \
ALIB_FETCHER_IMAGE=ghcr.io/example/alib-fetcher:latest \
docker compose up -d
```

With Podman Desktop, use `podman compose up -d`. Compose stores the database in
the persistent named volume `alib-fetcher-state`. Set `ALIB_FETCHER_IMAGE` to
override the default `ghcr.io/kemko/alib-fetcher:latest` image.

Keep `TELEGRAM_BOT_TOKEN` and other runtime credentials in the process
environment or an untracked local `.env` file. Git ignores `.env` and `.env.*`,
while allowing a credential-free `.env.example` to be tracked. Docker also
excludes `.env*`, local `data/`, and database files from the build context.
Never put real credentials in `.env.example`, Compose, or an image.

## Development

Go 1.26.5 is required. Run the complete non-mutating quality gate:

```bash
make verify
```

Use `make fmt` to apply formatting. `make verify` checks formatting, runs strict
linting and race-enabled tests, and builds `bin/alib-fetcher`. Quality targets
automatically provision and use the pinned golangci-lint under ignored
`bin/tools`; an unrelated binary earlier in `PATH` cannot affect verification.
CI uses the same Make targets, additionally runs `govulncheck`, and builds the
production container on pull requests and `master` pushes. Only a successful
`master` push publishes the image.

Run the coverage gate separately:

```bash
make coverage
```

It writes ignored `coverage.out` and fails when total statement coverage is
below 80%.

## Security-only dependency updates

Dependabot is configured for Go modules, Docker, and GitHub Actions with normal
version-update pull requests disabled. Enable **Dependabot alerts** and
**Dependabot security updates** under repository Settings > Security > Code
security and analysis. Security advisories can then open update pull requests;
new versions without a known vulnerability do not create pull requests.
