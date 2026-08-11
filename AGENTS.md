# Repository guide

## Project purpose

`alib-fetcher` is a small always-on Go service. It fetches the newest listings
from `https://www.alib.ru/tramka.phtml?tnew=7`, records discovered books in an
embedded bbolt database, renders pending books as Telegram HTML messages, sends
them to one chat, and records successful deliveries in the same database.

The module is `github.com/kemko/alib-fetcher`. The executable entry point is
`./cmd/alib-fetcher`. Go 1.26.5 is the supported toolchain; `make tools`
installs the pinned golangci-lint v2 release.

## Runtime flow and invariants

One digest cycle is deliberately ordered as follows:

1. Remove sent records strictly older than 14 days.
2. Fetch and decode the configured Alib page.
3. Parse and deduplicate listings by their resolved `BuyURL`.
4. Record fetched listings in bbolt as JSON records, preserving sent status.
5. Load all pending records from bbolt in first-discovery order, including
   books from earlier failed cycles.
6. Render pending books into Telegram-sized chunks.
7. Send each chunk and mark only that chunk's books as delivered, only after
   Telegram accepts it.

Preserve these semantics:

- The first successful run records and sends every listing currently on the
  source page.
- `BuyURL` is the persistent identity of a listing; titles and other metadata
  are not stable deduplication keys.
- A failed Telegram chunk must remain pending so a later cycle can retry it.
  Earlier successfully sent chunks stay acknowledged.
- Pending delivery order is the first-discovery/source order, not bbolt key
  sort order.
- A pending listing that cannot fit one Telegram message remains pending and
  must not block other renderable pending listings.
- When a digest has multiple chunks, all but the last are sent silently; the
  last chunk uses the normal notification sound.
- Every sent digest attaches the `Обновить` inline button only to the final
  Telegram chunk. A digest that sends no chunks does not create or move a
  refresh button.
- A Telegram flood-control response with a positive `retry_after` waits for the
  specified duration and retries the same chunk before later chunks. The wait
  honors context cancellation, and the chunk remains unacknowledged until a
  retry succeeds.
- An empty or structurally changed Alib page is an error (`alib.ErrNoBooks`),
  not a successful empty digest. This protects against silently accepting a
  broken parser.
- Retention uses a strict boundary for sent records: records sent before the
  14-day cutoff are removed; a record exactly at the cutoff remains. Pending
  records are not pruned by retention.
- Legacy bbolt marker values are migrated at open time to sent JSON records
  with only `Book.BuyURL` when full metadata cannot be recovered, and must not
  be pruned immediately.
- Service mode runs one cycle immediately after startup by default, then follows
  the cron schedule. `RUN_ON_STARTUP=false` skips the startup cycle. Overlapping
  cron jobs are skipped.
- Service mode polls Telegram `callback_query` updates for the stable
  `telegram.RefreshCallbackData` value. `-once` sends the refresh button when it
  sends books, but never starts the callback polling loop.
- Refresh callbacks run through the same digest path and bbolt state path as
  startup and scheduled jobs. Startup, scheduled, and refresh-triggered digests
  share one process-local runner lock; scheduled and refresh-triggered digests
  skip when another digest is already running.
- Callback polling must continue while a refresh-triggered digest is running so
  duplicate button presses can be answered and skipped. Poll errors must not
  spin in a tight loop.
- Unknown callback data is ignored after the update offset advances. A refresh
  callback skipped because another digest is running must still be answered.
- For refresh-triggered digests, remove the clicked message's old reply markup
  only after renderable chunks are known and before the first new Telegram
  message is sent. If no chunk will be sent, leave the old button in place. If
  old-button removal fails, do not send chunks or mark books delivered.
- The bbolt database is open only while a digest cycle is running, allowing a
  separate `-once` process to use it between scheduled cycles.
- `SIGINT` and `SIGTERM` stop scheduling gracefully and wait for cron shutdown.
- External requests use the configured timeout and request context.

## Repository map

- `cmd/alib-fetcher/main.go`: process wiring, `-once`, JSON logging, signals,
  startup run, robfig/cron lifecycle, refresh callback polling, and shared
  digest-runner concurrency.
- `internal/config`: environment loading, defaults, and validation.
- `internal/alib`: HTTP client plus charset-aware HTML parser. The real page may
  be Windows-1251. Listings are recognized inside `<p>` elements by a title in
  `<b>` and a `Купить` link; seller links contain `bs.php4`.
- `internal/app`: use-case orchestration through small `Fetcher`, `State`, and
  `Sender` interfaces. Keep policy here and transport/storage details in their
  adapter packages.
- `internal/digest`: full-listing Telegram HTML rendering and chunking only
  between complete listings.
- `internal/store`: bbolt storage in bucket `sent_books`; keys are buy URLs and
  values are JSON records containing the full `alib.Book`, observed timestamp,
  pending queue order, sent status, and sent timestamp for delivered records.
- `internal/telegram`: Telegram Bot API client for `sendMessage`,
  `getUpdates`, `answerCallbackQuery`, and `editMessageReplyMarkup`; digest
  messages use HTML parse mode with link previews disabled.
- `Dockerfile`: multi-stage static build; final distroless Debian image runs as
  UID/GID 65532 (`nonroot`) and stores state under `/var/lib/alib-fetcher`.
- `docker-compose.yml`: read-only, capability-dropped service with a persistent
  named state volume.
- `.github/workflows/ci.yml`: runs `make verify` and `govulncheck` on pushes/PRs
  to `master` in the `verify` job, then publishes
  `ghcr.io/${github.repository}:latest` only after a successful push run on
  `master`; ordinary quality commands must not be duplicated in CI.
- `.github/dependabot.yml`: normal scheduled version PRs are disabled; updates
  are intended to be security-only through repository security settings.

## Configuration contract

Required:

- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_CHAT_ID` (numeric chat ID or `@channel`)

Optional defaults:

| Variable | Default | Validation/meaning |
| --- | --- | --- |
| `CRON_SCHEDULE` | `0 0 * * *` | robfig standard five-field cron; descriptors such as `@hourly` and `@every 6h` are accepted |
| `TIMEZONE` | `Europe/Moscow` | IANA location used by cron |
| `RUN_ON_STARTUP` | `true` | whether service mode runs one digest cycle immediately after startup |
| `STATE_PATH` | `/var/lib/alib-fetcher/state.db` | bbolt database; parent directories are created with mode `0750`, DB with `0600` |
| `ALIB_URL` | `https://www.alib.ru/tramka.phtml?tnew=7` | HTTP(S) source; override it in integration tests |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | HTTP(S) API base; override it in tests |
| `HTTP_TIMEOUT` | `30s` | positive Go duration applied per external request |
| `MESSAGE_LIMIT` | `4000` | rune count, allowed range 64..4096 |

Invalid configuration prevents process startup. Never log or expose the bot
token; note that the sender internally puts it in the Bot API URL.

## Digest and transport details

Messages start with `<b>Новые книги на Alib.ru</b>`. Each listing contains its
full source text with a bold title plus seller and `Купить` links. The source
`Смотрите` section is omitted and replaced with `Фото: есть` or `Фото: нет`.
All dynamic text and URLs must be HTML-escaped. Limits are counted in Unicode
runes, and chunks may split only between listings. A single listing that cannot
fit returns `digest.ErrMessageTooLong`.

The Alib client accepts only HTTP(S), sends `User-Agent: alib-fetcher/1.0`, and
requires HTTP 200. The Telegram sender accepts only HTTP(S), caps response
decoding at 1 MiB, returns `telegram.ErrRequest` for transport failures and
`telegram.ErrRejected` for unsuccessful API responses, and includes Telegram's
description and optional `retry_after` delay in rejection errors. Callback
polling must request only `callback_query` updates and derive its long-poll
timeout from the configured HTTP timeout.

Structured logs go to stdout. Stable event names are `scheduler.started`,
`scheduler.stopped`, `digest.started`, `digest.completed`, `digest.failed`,
`callback.poll_failed`, `callback.answer_failed`, and `service.failed`;
completion fields are `fetched`, `new`, `pruned`, and `sent`. Keep slog
attributes typed, snake_case, and free of secrets.

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

Local files `data/`, `bin/`, `coverage.out`, and `tnew7.txt` are ignored. Do not
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

`master` is the CI and image-publishing branch. The container is expected to run
with a read-only root filesystem and a writable persistent volume mounted at
`/var/lib/alib-fetcher`; do not move mutable state elsewhere without updating
the image, Compose, and README together. Preserve the nonroot runtime,
capability drop, and `no-new-privileges` hardening.

For Compose, `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` must come from the
environment. `ALIB_FETCHER_IMAGE` overrides the image. Never bake secrets into
the image or commit them in Compose.
