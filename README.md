# alib-fetcher

Always-on Go service that fetches the latest listings from Alib category,
series, and publisher pages, then delivers unseen books to a Telegram chat as
Rich Messages on a configurable cron schedule. Delivered listings are tracked
by their unique `Купить` link in an embedded bbolt database, which also stores
the pending send queue.

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
| `ALIB_CATEGORIES` | no | empty | Comma-separated ASCII category names; each creates a `https://www.alib.ru/<category>.phtml?tnew=7` page |
| `ALIB_SERIES` | no | empty | Comma-separated Unicode series names representable in Windows-1251; each creates a `https://alib.ru/findp.php4?seria=<encoded>&lday=7` page |
| `ALIB_PUBLISHERS` | no | empty | Comma-separated Unicode publisher names representable in Windows-1251; each creates a `https://alib.ru/findp.php4?izdat=<encoded>&lday=7` page |
| `ALIB_REQUEST_INTERVAL` | no | `1s` | Non-negative Go duration between sequential Alib page requests; `0s` disables the delay |
| `TELEGRAM_API_BASE` | no | `https://api.telegram.org` | Bot API base URL; custom/local servers require Bot API 10.1+ |
| `HTTP_TIMEOUT` | no | `30s` | Positive Go duration applied to each external request |
| `MESSAGE_LIMIT` | no | `32000` | Displayed Rich Message text rune limit, allowed range `64..32768` |

Configuration is validated before process startup. Invalid chat IDs, including
plain text without `@`, an empty `@` username, whitespace, and numeric overflow,
fail fast with an error naming `TELEGRAM_CHAT_ID`.
`ALIB_URL` is no longer supported. Before upgrading, replace it with
`ALIB_CATEGORIES`, `ALIB_SERIES`, `ALIB_PUBLISHERS`, or a combination; there is
no compatibility fallback or default category.
All three variables are optional independently, but at least one must be
non-empty. Each variable is parsed as one CSV record: surrounding whitespace is
trimmed, empty elements and malformed quotes are rejected, and values retain
their source order. Categories must be non-empty ASCII letters only. Repeated
series and publisher names are ignored after their first occurrence. Series and
publishers are entered as Unicode, but each value must be representable in
Windows-1251. Each value is first encoded to Windows-1251, then those bytes are
URL-escaped as the `seria` or `izdat` query value, so spaces, `&`, `/`, and
commas cannot inject extra parameters. An unrepresentable character is a
configuration error naming the source variable.
Requests always use the fixed seven-day window (`tnew=7` or `lday=7`), in
category, series, then publisher order. For example:

```bash
ALIB_CATEGORIES='tramka,detektivy' \
ALIB_SERIES='"История, тома",Фантастика & фэнтези' \
ALIB_PUBLISHERS='Эксмо,"Международный центр фантастики"' \
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
download failure has no parse event. Generated page URLs are also included in
errors and written verbatim to stdout.
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
  `FRESH_BOOKS`, and also marks listings without a recognized publication year;
- other unrecognized publication years receive no marker.

The January `🔥` rule applies even when `FRESH_BOOKS` is empty or its configured
boundary excludes the previous year.

The parser recognizes the last four-digit year in the bibliography followed by
`г` or `г.`. Years found only in content or other listing sections do not affect
freshness markers.

Each digest first checks fetched listing identities against the state database,
then records renderable new listings as pending records. A book-specific parse
or rendering failure excludes that book from the current digest; a new failed
book is not written to state and is retried on the next cycle. Such failures
are counted once per book.

Recorded listings contain the full semantic Alib payload: title, bibliography,
publication year, content, seller name and URL, location, price, condition and
other details, purchase URL, and ordered photo URLs with normalized captions. The parser derives these fields
from DOM nodes and logical `<br>`-delimited lines; it does not parse HTML with
regular expressions. Existing records keep their sent status while refreshing
the parsed payload from the latest source pages. The first successful run sends
every listing that can be rendered; failed listings remain
undelivered for rediscovery.

`Store.Pending` returns every pending record in first-discovery order, not only
books found in the current fetch result. Before rendering, the service puts
books without a recognized publication year (`0`) first, preserving their
first-discovery order, then sorts recognized years in descending order with the
same stable ordering for equal years. Books that could not be sent remain
pending across later digest cycles. A chunk is acknowledged only after
Telegram accepts it, and then its records become sent. Sent records older than
14 days are removed once at the beginning of every digest cycle; pending
records are not removed by retention pruning. The `MESSAGE_LIMIT` counts
Unicode runes in the displayed Rich Message text after parsing its HTML:
formatting tags and URL attribute values do not count, while encoded text and
`<br/>` line breaks do. Content that makes a listing too long is shortened with
`…` before HTML escaping to the longest prefix that fits within
`MESSAGE_LIMIT - 1` displayed runes; only `Content` is shortened. If the
listing's mandatory displayed fields plus minimal content still do not fit,
`digest.ErrMessageTooLong` is returned; other renderable pending listings are
still sent while that listing remains pending.
Chunks split before Telegram's limit of 500 Rich Message blocks. Each ordinary
chunk contains at most 250 listings.
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
2. content in its own section, when present;
3. seller as `Продавец: <a href="...">Name</a>, Location.`, then price,
   condition/other details, and source photo links when present on separate lines;
4. a final `Купить` link in its own section.

When photos are available, the source photo-link section is rendered as
`Смотрите: <a href="...">Обложка</a> - <a href="...">фото</a>` in source order,
including repeated links. Captions come from the source anchor; an empty caption
falls back to `фото`. When an announcement has no photos, the photo line is omitted.
When seller URL is absent, seller name is rendered as plain text. Missing
optional fields do not create empty sections. Dynamic text and URLs are
HTML-escaped. Every encoded line break uses `<br/>`; sections use `<br/><br/>`
to render one empty line without client-specific paragraph spacing. Rendered
Telegram HTML contains no literal CR or LF characters. The heading has the same
separator before the first listing. Content and details are independent optional
sections between `main` and the final `Купить` section. When both are absent,
the layout is exactly `main → <br/><br/> → Купить`. Adjacent listings in one
Rich Message are separated by `<hr/>`; no divider appears before the first
listing or after the last. A digest uses multiple Telegram messages when
pending content exceeds `MESSAGE_LIMIT` or 500 blocks; the heading appears only
in the first message.
If the heading and first pending listing cannot fit together but the listing
fits alone, the first message contains only the heading and the listing follows
in a headerless message.
When book-specific failures occurred, the final message includes
`Не удалось обработать книг: N`; it follows an `<hr/>` when the chunk also
contains books. With no renderable books, the digest contains the heading and
summary, split into two messages if required by the limits. The count includes
listings skipped because of `digest.ErrMessageTooLong`.

Photos are never downloaded or transformed. Every source photo is rendered in
one `Смотрите` section with its original URL, source caption, order, and
repeated links; an empty caption is rendered as `фото`.
Only the final message uses the normal notification sound; all earlier messages
are silent. Whenever a digest sends at least one message, the final message
includes an inline `Обновить` button.
Pressing it asks a running service with the same bot token to start one
out-of-schedule digest. If that refresh sends new notifications, the clicked
message's old button is removed before the first new message is sent, and the
last new message receives a fresh `Обновить` button. If the refresh produces no
message chunk, the old button stays in place. A failure-summary-only digest does
produce a chunk and therefore moves the button.
The callback is answered immediately with `Формирование дайджеста запущено`
after the refresh runner lock is acquired; the digest continues in the
background on the service lifetime context. No completion callback answer is
sent; completion and errors are reported through `digest.completed` and
`digest.failed` logs.
`HTTP_TIMEOUT` applies to each external request, not to the whole refresh
digest. Toast display duration is controlled by the Telegram client;
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
ALIB_CATEGORIES=tramka \
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
ALIB_CATEGORIES=tramka \
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
`SIGTERM`. Telegram transport errors include the underlying sanitized cause; the
bot token and API base URL are redacted.

On first run after upgrading from older timestamp-marker releases, raw legacy
state entries are migrated to JSON records. Structured records from releases
that stored `text_before_seller`, `text_before_buy`, and `text_after_buy` remain
readable: a narrow JSON compatibility decoder converts those fragments to the
semantic `Book` model in memory. Opening the database does not rewrite valid
legacy structured records. A structured legacy `has_photos` field is ignored
because it contains no recoverable photo URLs; its photo line stays
absent until rediscovery supplies URLs. Legacy `photo_urls` arrays decode as
photo records with caption `фото`. Opening the database does not rewrite them;
the next mutating write, including rediscovery or successful-delivery
acknowledgement, stores the current `photos` schema.
Values that look like structured JSON records must decode successfully, and their stored purchase
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
  -e ALIB_CATEGORIES=tramka \
  ghcr.io/<owner>/<repository>:latest
```

Alternatively, start the service with the Compose v3.8 configuration:

```bash
export TELEGRAM_BOT_TOKEN=...
export TELEGRAM_CHAT_ID=...
export ALIB_CATEGORIES=tramka
docker compose up -d
```

With required Telegram variables already supplied by the environment or an
untracked `.env`, runtime settings can be overridden without putting credentials
in the command or Compose file:

```bash
FRESH_BOOKS=age:5 \
TIMEZONE=Europe/Moscow \
ALIB_CATEGORIES=tramka,detektivy \
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
