# Repository guide

## Project purpose

`alib-fetcher` is a small always-on Go service. It fetches the newest listings
from one or more configured Alib pages, records discovered books in an embedded
bbolt database, renders pending books as Telegram HTML messages, sends them to
one chat, and records successful deliveries in the same database.

The module is `github.com/kemko/alib-fetcher`. The executable entry point is
`./cmd/alib-fetcher`. Go 1.26.5 is the supported toolchain; `make tools`
installs the pinned golangci-lint v2 release.

## Runtime flow and invariants

One digest cycle is deliberately ordered as follows:

1. Remove sent records strictly older than 14 days.
2. Download all configured Alib pages sequentially, retaining successful response
   bodies and their source order.
3. Parse successful responses only after all downloads finish, then combine and
   deduplicate listings by their resolved `BuyURL`, retaining the first occurrence.
4. Read existing `BuyURL` records without writing new fetched listings.
5. Sequentially prepare new and pending books, isolating book-specific errors
   and cleaning each temporary directory before continuing.
6. Record only fully prepared, renderable new listings in bbolt, preserving sent status.
7. Load all pending records from bbolt in first-discovery order, including
   books from earlier failed cycles.
8. Sort pending books with year `0` first, then publication year descending,
   preserving first-discovery order within each group.
9. Render pending books into Telegram-sized chunks, adding the book-failure
   summary when needed.
10. Send each chunk and mark only that chunk's books as delivered, only after
   Telegram accepts it.

Preserve these semantics:

- The first successful run records and sends every listing currently on the
  configured source pages that can be prepared and rendered. A book-specific
  failure is counted once, omitted from the current digest, and a new failed
  book is not recorded so the next cycle can rediscover it.
- `BuyURL` is the persistent identity of a listing; titles and other metadata
  are not stable deduplication keys.
- A failed Telegram chunk must remain pending so a later cycle can retry it.
  Earlier successfully sent chunks stay acknowledged.
- `State.Pending` returns records in first-discovery/source order, not bbolt key
  sort order; the digest sends year `0` records first, then recognized years in
  descending order, with stable first-discovery order within each group.
- `MESSAGE_LIMIT` counts Unicode runes in displayed Rich Message text after
  parsing HTML; formatting tags and URL attribute values do not count, while
  encoded text and `<br/>` line breaks do. A listing's `Content` is shortened
  with `…` to the longest prefix that fits within `MESSAGE_LIMIT - 1` displayed
  runes. If mandatory displayed fields plus minimal content still cannot fit,
  it remains pending and must not block other renderable pending listings;
  `digest.ErrMessageTooLong` is reported.
- Chunks split before Telegram's 500-block limit, including nested slideshow
  images, or 50-media limit. Ordinary chunks contain at most 250 listings.
- A slideshow contains at most 50 images; successful images beyond that limit
  remain source links so one listing stays atomic and deliverable.
- The final digest chunk includes `Не удалось обработать книг: N` after an
  `<hr/>` when one or more book-specific failures occurred; the count includes
  listings skipped by `digest.ErrMessageTooLong`.
- When a digest has multiple chunks, the first uses the normal notification
  sound and all later chunks are sent silently.
- Every sent digest attaches the `Обновить` inline button only to the final
  Telegram chunk. A digest that sends no chunks does not create or move a
  refresh button.
- A Telegram flood-control response with a positive `retry_after` waits for the
  specified duration and retries the same chunk before later chunks. The wait
  honors context cancellation, and the chunk remains unacknowledged until a
  retry succeeds.
- A valid Alib search page with no listings is a successful empty result. An
  empty or structurally changed page is `alib.ErrNoBooks`; a cycle fails only
  when every configured page fails or the context is canceled.
- Retention uses a strict boundary for sent records: records sent before the
  14-day cutoff are removed; a record exactly at the cutoff remains. Pending
  records are not pruned by retention.
- Legacy bbolt marker values are migrated at open time to sent JSON records
  with only `Book.BuyURL` when full metadata cannot be recovered, and must not
  be pruned immediately. Values shaped as structured JSON records must decode
  successfully and have `Book.BuyURL` equal to their bbolt key; otherwise open
  fails transactionally without converting the corrupt value or committing
  neighboring migrations.
- Structured records using legacy `text_before_seller`, `text_before_buy`, and
  `text_after_buy` fields decode into the semantic `Book` model in memory. A
  database open must not rewrite them; the next mutating write, including
  rediscovery or successful-delivery acknowledgement, writes the current schema
  while preserving delivery state, queue order, and timestamps.
- Legacy `photo_urls` arrays decode as semantic photo records with caption
  `фото`; database open does not rewrite them, and the next mutating write uses
  the current `photos` schema.
- Service mode runs one cycle immediately after startup by default, then follows
  the cron schedule. `RUN_ON_STARTUP=false` skips the startup cycle. Overlapping
  cron jobs are skipped.
- Service mode starts SDK-managed polling for Telegram `callback_query` updates
  registered for the stable `telegram.RefreshCallbackData` value. `-once` sends
  the refresh button when it sends books, but never starts the SDK listener.
- Refresh callbacks run through the same digest path and bbolt state path as
  startup and scheduled jobs. Startup, scheduled, and refresh-triggered digests
  share one process-local runner lock; scheduled and refresh-triggered digests
  skip when another digest is already running.
- Callback polling must continue while a refresh-triggered digest is running so
  duplicate button presses can be answered and skipped. The SDK owns update
  offsets and polling retry/backoff; polling errors are reported through the
  local stable log event without exposing secrets.
- Unknown callback data is ignored while the SDK advances the update offset. A
  refresh callback from a different numeric chat ID or public `@channel`
  username is answered and ignored. A refresh callback skipped because another
  digest is running must still be answered.
- After acquiring the runner lock, a refresh callback is answered immediately
  with `Формирование дайджеста запущено`; the digest continues in the
  background on the service lifetime context and has no overall deadline.
  Digest results and errors are recorded in `digest.completed` and
  `digest.failed`; `HTTP_TIMEOUT` still applies to each external request.
  Toast display duration is controlled by the Telegram client; the Bot API
  cannot guarantee an exact duration.
- For refresh-triggered digests, remove the clicked message's old reply markup
  only after renderable chunks are known and before the first new Telegram
  message is sent. If no chunk will be sent, leave the old button in place. If
  old-button removal fails, do not send chunks or mark books delivered.
- The bbolt database is open only while a digest cycle or `-forget-latest`
  maintenance operation is running, allowing another process to use it between
  scheduled cycles.
- `SIGINT` and `SIGTERM` stop scheduling gracefully and wait for cron shutdown.
- External requests use the configured timeout and request context.

## Repository map

- `cmd/alib-fetcher/main.go`: thin bootstrap wiring for JSON logging, `-once`,
  `-forget-latest`, configuration loading, adapter construction, signal
  context, `internal/process.Run`, and `internal/process.ForgetLatest`.
- `internal/process`: service process lifecycle orchestration, state DB open
  lifetime, startup and scheduled digest runs, robfig/cron lifecycle, refresh
  callback policy and listener lifecycle, shared digest-runner concurrency, and
  the state-only forget-latest maintenance operation.
- `internal/config`: environment loading, defaults, and validation.
- `internal/alib`: HTTP client plus charset-aware, DOM-first HTML parser. The
  real page may be Windows-1251. Listings are recognized inside `<p>` elements
  by a title in `<b>` and a `Купить` link; seller links contain `bs.php4`.
  Logical lines are split on `<br>` and become semantic bibliography,
  publication year, content, seller, location, price, condition, purchase URL,
  and ordered photo URLs and normalized captions without regex-based HTML
  parsing. Relative photo URLs are resolved while source order and repeats are
  preserved. Publication year is
  the last four-digit year in the bibliography followed by `г` or `г.`; content
  years are ignored.
- `internal/app`: use-case orchestration through small `Fetcher`, `State`,
  `PhotoProcessor`, and `Sender` interfaces. It saves prepared pending books and
  completes temporary-file cleanup before rendering. Keep policy here and
  transport/storage details in their adapter packages.
- `internal/digest`: full-listing Telegram Rich HTML block rendering and
  chunking only between complete listings, including slideshow block/media limits.
- `internal/slink`: SSRF-safe source downloader, HTTP/META redirect handling,
  content-based image detection, temporary files, and Slink multipart uploads.
- `internal/store`: bbolt storage in bucket `sent_books`; keys are buy URLs and
  values are JSON records containing the full semantic `alib.Book`, observed
  timestamp, pending queue order, sent status, and sent timestamp for delivered
  records. `alib.Book` has a narrow decoder for persisted legacy fragment fields.
- `internal/telegram`: adapter backed by `github.com/go-telegram/bot` v1.23.0
  for `SendRichMessage`, callback polling, `AnswerCallbackQuery`, and
  `EditMessageReplyMarkup`; digest messages use `rich_message.html`. The SDK
  owns Bot API endpoints, request/response models, serialization, allowed
  updates, offsets, and polling retry/backoff.
- `Dockerfile`: multi-stage static build; final distroless Debian image runs as
  UID/GID 65532 (`nonroot`) and stores state under `/var/lib/alib-fetcher`.
- `docker-compose.yml`: read-only, capability-dropped service with a persistent
  named state volume.
- `.github/workflows/ci.yml`: runs `make verify` and `govulncheck` on pushes/PRs
  to `master`, then validates Compose and builds the production image. Pull
  requests never log in or push; a successful `master` push publishes
  `ghcr.io/${github.repository}:latest` from its single image build. Ordinary
  quality commands must not be duplicated in CI.
- `.github/dependabot.yml`: normal scheduled version PRs are disabled; updates
  are intended to be security-only through repository security settings.

## Configuration contract

Required for digest and service modes; `-forget-latest` reads only `STATE_PATH`
and ignores the remaining service configuration:

- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_CHAT_ID` (signed decimal `int64` chat ID or non-empty `@channel`
  username, with no whitespace)

Optional defaults:

| Variable | Default | Validation/meaning |
| --- | --- | --- |
| `CRON_SCHEDULE` | `0 0 * * *` | robfig standard five-field cron; descriptors such as `@hourly` and `@every 6h` are accepted |
| `TIMEZONE` | `Europe/Moscow` | IANA location used by cron and publication-year markers |
| `RUN_ON_STARTUP` | `true` | whether service mode runs one digest cycle immediately after startup |
| `FRESH_BOOKS` | empty | optional inclusive `✨` threshold: `age:N` or `since:YYYY`; empty disables only `✨` |
| `STATE_PATH` | `/var/lib/alib-fetcher/state.db` | bbolt database; parent directories are created with mode `0750`, DB with `0600` |
| `ALIB_URL` | `https://www.alib.ru/tramka.phtml?tnew=7` | One HTTP(S) source or comma-separated list; surrounding whitespace is trimmed, literal commas must use `%2C`, URL userinfo is rejected |
| `ALIB_REQUEST_INTERVAL` | `1s` | Non-negative Go duration between sequential Alib requests; `0s` disables the delay |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | HTTP(S) API base; override it in tests |
| `HTTP_TIMEOUT` | `30s` | positive Go duration applied per external request |
| `MESSAGE_LIMIT` | `32000` | displayed Rich Message text rune count after HTML parsing, allowed range 64..32768 |
| `SLINK_URL` | empty | HTTP(S) Slink base URL without userinfo, query, or fragment; empty disables Slink when all Slink variables are empty |
| `SLINK_API_KEY` | empty | Slink API key beginning with `sk_`; required with the other Slink variables and never logged |
| `SLINK_TAG_ID` | empty | UUID of the pre-created Slink `alib` tag owned by the API-key account; required with the other Slink variables |

Invalid configuration, including a malformed or overflowing
`TELEGRAM_CHAT_ID`, prevents process startup. Errors name the invalid variable.
Never log or expose the bot token; note that the SDK internally puts it in the
Bot API URL.
Slink is disabled only when all three `SLINK_*` values are empty. Partial
configuration fails with the missing variable name. Changing `SLINK_URL` or
`SLINK_TAG_ID` changes the persisted processing profile and reprocesses pending
photos; API-key rotation alone does not.

`FRESH_BOOKS=age:N` accepts a non-negative integer and sets the inclusive lower
year to `current local year - N`; `age:0` therefore includes only the current
year. `FRESH_BOOKS=since:YYYY` accepts a four-digit inclusive lower year. Empty
or absent `FRESH_BOOKS` disables `✨`, not `🔥`. The cycle time in `TIMEZONE`
controls classification: the current year gets `🔥`; in January, the previous
year also gets `🔥` regardless of the optional threshold. Other recognized
years from the threshold through the current year get `✨`. A recognized year
greater than the current year gets `🛸` independently of `FRESH_BOOKS`; a year
`0` also gets `🛸`, while other unrecognized years get no marker. The recognized
year is the last four-digit year in the bibliography followed by `г` or `г.`;
years elsewhere in
the listing do not participate.

## Digest and transport details

The first message starts with `<b>Новые книги на Alib.ru</b>`; when a listing
follows in the same chunk, `<br/><br/>` separates it from the heading. Later
chunks start with a listing. Messages split on text, block, and media limits. If
the header and first listing do not fit together
but the listing fits alone, the first chunk contains only the header. Rich HTML
uses `<br/>` for every encoded line break and `<br/><br/>` between sections to
render one empty line without client-specific paragraph spacing. Rendered
Telegram HTML contains no literal CR or LF characters. Content and details are
independent optional sections between `main` and the final `Купить` section.
When both are absent, the block is exactly `main → <br/><br/> → Купить`. The
heading and final `Купить` section use the same separator. Adjacent listings
within one chunk use `<hr/>`, with no divider at chunk edges. Chunks contain at
most 500 blocks including nested slideshow images and 50 media attachments;
ordinary chunks therefore contain at most 250 listings.
Each listing renders, in order: emoji plus bold title and bibliography; optional
content as a separate section; seller, price, condition/other details, and photo
links on separate lines; an optional Slink slideshow; then a final `Купить` link
in its own section.
The seller format is
`Продавец: <a href="...">Name</a>, Location.`; without seller URL, the name is
plain text. Missing optional fields must not create extra empty sections. Photo
links use normalized source captions and fall back to `фото` when empty, in source
order including repeats; the line is omitted when no photos exist. Published
images for the active Slink profile render as `<img>` children of one
`<tg-slideshow>` with unique captions in one `<figcaption>`. A slideshow contains
at most 50 images; additional successful images remain source links. All
dynamic text and URLs must be HTML-escaped. Limits are counted in Unicode runes
of displayed Rich Message text after HTML parsing: formatting tags and URL
attribute values do not consume the limit, while encoded text and `<br/>` line
breaks do. Chunks may split only between listings. Content that exceeds the
limit is truncated before HTML escaping to the longest prefix plus `…` that
fits within `MESSAGE_LIMIT - 1`; only `Content` is shortened. If mandatory
displayed fields plus minimal content still cannot fit, the listing returns
`digest.ErrMessageTooLong`.

When Slink is enabled, source photos are downloaded sequentially through limited
HTTP and HTML META refresh redirects into one system-temp directory per book.
Targets must use HTTP(S), omit userinfo, and resolve only to public addresses at
every connection; loopback, private, link-local, multicast, and unspecified
addresses are rejected before dialing. Downloads are capped at 15 MiB and Slink
responses at 1 MiB. Image type is detected from content. Uploads use `POST
/api/external/upload` with multipart field `image`, repeated `tagIds[]`, Bearer
authentication, and an `Origin` derived from `SLINK_URL`. The API key must use
the `sk_` prefix, the tag must belong to that key's owner, and Slink
external-upload auto-publish must be enabled. Individual photo failures log a
safe stage/status/category and isolate the whole book; new failed books are not
recorded and pending failed books remain pending. Context cancellation stops the digest.
Prepared results are saved before their temp directory is removed and are reused
only for the same Slink URL/tag profile.

The Alib client accepts one or more HTTP(S) endpoints, sends
`User-Agent: alib-fetcher/1.0`, and requires HTTP 200. Endpoints are downloaded
sequentially with `ALIB_REQUEST_INTERVAL` between attempts. All download
attempts finish before successful responses are parsed in source order. Responses
larger than 4 MiB are rejected as download failures. Listings are then combined
in first-seen order and deduplicated by `BuyURL`; a failed download or parse does
not discard successful results from other pages. A valid empty search page is
successful, while a cycle fails if no page parses successfully. The client logs
`alib.page_downloaded` or
`alib.page_download_failed` for each download and, only after a successful
download, logs `alib.page_parsed` or `alib.page_parse_failed` for its parse.
Every event has the zero-based `index` and full configured endpoint `url`,
including GET parameters and fragments; parsed events also have `books`, and
failed events have `error`. Userinfo is rejected during configuration. The
configured endpoints are written verbatim to logs and page errors, so
`ALIB_URL` must not contain credentials or other secrets. The
SDK-backed Telegram adapter accepts only HTTP(S), caps
response decoding at 1 MiB, returns `telegram.ErrRequest` for transport failures
and `telegram.ErrRejected` for unsuccessful API responses, and includes
Telegram's description and optional `retry_after` delay in rejection errors.
Callback polling requests only `callback_query` updates and derives its
long-poll timeout from the configured HTTP timeout. The SDK owns Telegram
operations and polling mechanics; app/process keep chunk acknowledgement,
flood-control retry, chat filtering, refresh ordering, and runner-lock policy.

Structured logs go to stdout. Stable event names are `scheduler.started`,
`scheduler.stopped`, `digest.started`, `digest.completed`, `digest.failed`,
`alib.page_downloaded`, `alib.page_download_failed`, `alib.page_parsed`,
`alib.page_parse_failed`, `slink.photo_failed`, `slink.response_close_failed`,
`callback.poll_failed`, `callback.answer_failed`,
`state.forget_latest.completed`, and `service.failed`; digest completion fields
are `fetched`, `new`, `failed`, `pruned`, and `sent`, while forget-latest completion fields
are `requested` and `deleted`. Every Alib page event includes the zero-based
`index` and full configured endpoint `url`, including GET parameters and
fragments; `alib.page_parsed` also includes `books`, and failed events include
`error`. Userinfo is rejected during configuration. Keep slog attributes typed,
snake_case, and free of secrets; full Alib URL logging relies on the
credential-free `ALIB_URL` contract above.

## Development and verification

The Makefile is the canonical interface for all ordinary code-quality and build
operations; do not replace its targets with direct `go` or `golangci-lint`
invocations. The default target and canonical full check are:

```bash
make verify
```

It checks formatting, runs strict golangci-lint, executes race-enabled shuffled
tests without cache, and builds the binary. It does not silently rewrite source
files. The Makefile must remain sufficient and working for the full development
cycle, including from a clean checkout:

- `make fmt` formats all Go code with the configured golangci-lint formatters.
- `make fmt-check` fails and prints a diff when Go code is not formatted.
- `make lint` runs the complete configured linter set.
- `make test` runs the complete test suite with the race detector, shuffled
  order, and no result cache.
- `make coverage` writes `coverage.out` and fails when total statement coverage
  is below 80%.
- `make build` compiles `bin/alib-fetcher` with reproducible path trimming.
- `make tools` installs the exact golangci-lint version used by CI under the
  ignored project-local `bin/tools` tree.
- `make verify` runs `fmt-check`, `lint`, `test`, and `build`; it is also the
  default `make` target and provisions the pinned tool automatically.

When requirements change, update Makefile targets so these commands keep doing
what their names promise. CI and agent workflows must call the Make targets,
not duplicate their underlying `go` or `golangci-lint` commands. Any
environment-specific setup belongs inside or under the Make targets rather
than in undocumented one-off verification commands.

Quality targets must invoke repository-managed tools by explicit paths. Never
accept a successful verification from an arbitrary same-named executable found
earlier in `PATH`; `make tools` and `make verify` must use the same binary.

The lint configuration is intentionally strict. Important local constraints
include 120-column lines, gofumpt/goimports formatting, exhaustive error
checking, no unexplained or broad `nolint`, complexity limits, package/exported
comments, and strict `slog` usage. Prefer fixing the cause over weakening
`.golangci.yml`.

Tests use testify, generally follow `Given / When / Then`, and call
`t.Parallel()` when they do not mutate process-global state. Do not parallelize
configuration tests that use `t.Setenv` or main/signal/flag tests. Use
`httptest.Server`, temporary directories, and injected interfaces/URLs; the
test suite must not depend on live Alib or Telegram access. Add regression
tests for behavior changes, especially parsing, acknowledgement ordering,
retention boundaries, message limits, startup scheduling, and cancellation.

Useful local run commands:

```bash
TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=... STATE_PATH=./data/state.db \
  go run ./cmd/alib-fetcher -once

TELEGRAM_BOT_TOKEN=... TELEGRAM_CHAT_ID=... CRON_SCHEDULE='*/30 * * * *' \
  STATE_PATH=./data/state.db go run ./cmd/alib-fetcher
```

Local files `.env`, `.env.*`, `data/`, `bin/`, `coverage.out`, and `tnew7.txt`
are ignored; a credential-free `.env.example` may be tracked. `.env*`, local
data, and database files are excluded from the Docker build context. Do not
commit credentials, state databases, captured production data, or generated
binaries.

## Change and commit policy

- For every task that modifies repository files, the default deliverable is one
  or more commits. After verification, stage all and only task-related changes
  and commit them before handoff without waiting for a separate request. Skip
  the commit only when the user explicitly asks not to commit or verification
  cannot complete; in that case, report the exact worktree and index state.
- Keep changes focused and commit them in coherent, independently understandable
  parts. Do not combine unrelated refactors, behavior changes, and dependency
  updates in one commit.
- Commit only complete work for which `make verify` passes in full and the
  linter reports no findings. Never commit known failing tests, lint debt, or a
  partially working state.
- Use concise Conventional Commit subjects consistent with the history, for
  example `feat: ...`, `fix: ...`, `ci: ...`, or `chore: ...`.
- Keep `README.md` accurate in the same change whenever behavior, configuration,
  defaults, commands, deployment, dependencies, or operational expectations
  change. Documentation drift is a failed change, even when code passes.
- Preserve existing user changes in a dirty worktree. Inspect the diff before
  editing and before committing; do not rewrite or include unrelated work.
- Before handoff, report the verification performed and any checks that could
  not be run. A commit is not ready while verification is incomplete.

## Deployment notes

`master` is the CI and image-publishing branch. Pull requests and pushes build
the production image only after verification; pull requests never publish, and
a successful `master` push builds once and publishes `latest`. The container is
expected to run with a read-only root filesystem and a writable persistent
volume mounted at `/var/lib/alib-fetcher`; do not move mutable state elsewhere
without updating the image, Compose, and README together. Preserve the nonroot
runtime, capability drop, and `no-new-privileges` hardening.

Slink-enabled read-only containers require a writable `/tmp`; Compose provides a
`noexec,nosuid,nodev` tmpfs. Compose passes `SLINK_URL`, `SLINK_API_KEY`, and
`SLINK_TAG_ID` from the environment. Keep the API key out of the image and
tracked files.

For Compose, `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` must come from the
environment. `FRESH_BOOKS`, `TIMEZONE`, and `ALIB_FETCHER_IMAGE` are
credential-free runtime overrides; Compose passes an empty `FRESH_BOOKS` by
default. Never bake secrets into the image or commit them in Compose. Local
`.env` files must stay untracked and out of the Docker build context;
`.env.example` must never contain credentials.
