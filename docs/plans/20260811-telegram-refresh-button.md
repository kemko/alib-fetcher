# Telegram Refresh Button

## Overview

Добавить inline-кнопку "Обновить" к последнему Telegram-сообщению каждого digest, если были отправлены книги. Нажатие запускает внеочередную проверку в сервисном режиме через Telegram callback updates. Если проверка приводит к отправке новых уведомлений, старая кнопка удаляется перед отправкой, а под последним новым сообщением появляется новая кнопка.

## Context

- Files involved:
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/telegram/sender.go`
  - `internal/telegram/sender_test.go`
  - `cmd/alib-fetcher/main.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
- Related patterns:
  - `app.Service.Run` already owns digest ordering, chunk sending, rate-limit retry, and sent acknowledgement.
  - `telegram.Sender` already wraps Bot API JSON POSTs, response decoding capped at 1 MiB, timeout, and `retry_after` rejection errors.
  - `cmd/alib-fetcher/main.go` already distinguishes `-once` from service mode and opens bbolt only inside `executeJob`.
- Dependencies:
  - No new external Go dependency planned.
  - Use Telegram Bot API methods: `sendMessage`, `getUpdates`, `answerCallbackQuery`, `editMessageReplyMarkup`.

## Development Approach

- **Testing approach**: Regular, code first then focused regression tests per task.
- Complete each task fully before moving to the next.
- Keep callback/update handling in `internal/telegram`; keep digest policy in `internal/app`; keep process concurrency and scheduling in `cmd/alib-fetcher`.
- `-once` sends the button when books are sent, but does not listen for callbacks. A running service with the same bot handles later button presses.
- Callback check runs through the same digest path as startup/scheduled/manual checks.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Add refresh-button send support

**Files:**
- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Modify: `internal/telegram/sender.go`
- Modify: `internal/telegram/sender_test.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] Extend the app sender contract from `Send(ctx, text, silent)` to include an `attachRefresh` boolean.
- [x] In `app.Service.Run`, pass `attachRefresh=true` only for the last rendered chunk.
- [x] Preserve existing silent notification behavior: all chunks except the last stay silent.
- [x] In `telegram.Sender.Send`, encode `reply_markup.inline_keyboard` with one button: text `Обновить`, callback data stable constant such as `refresh`.
- [x] Update fake senders/noop senders in tests for the new signature.
- [x] Add or update tests proving only the last chunk gets `attachRefresh=true`.
- [x] Add Telegram sender test proving the JSON payload contains the inline keyboard only when requested.
- [x] Run `make test` and fix failures before Task 2.

### Task 2: Add Telegram callback API support

**Files:**
- Modify: `internal/telegram/sender.go`
- Modify: `internal/telegram/sender_test.go`
- Create: `internal/telegram/updates.go` if separating polling keeps `sender.go` too large.
- Create: `internal/telegram/updates_test.go` if `updates.go` is created.

- [x] Refactor Telegram endpoint construction so one client can call multiple Bot API methods without duplicating token URL logic.
- [x] Add typed callback/update structs exposing callback ID, message chat ID, message ID, and callback data.
- [x] Implement `PollCallbacks(ctx, offset)` via `getUpdates` with `allowed_updates=["callback_query"]`, long-poll timeout derived from configured HTTP timeout, and next offset handling.
- [x] Implement `AnswerCallback(ctx, callbackID, text)` via `answerCallbackQuery`.
- [x] Implement `RemoveReplyMarkup(ctx, chatID, messageID)` via `editMessageReplyMarkup` with empty reply markup.
- [x] Reuse existing Bot API error handling, response-size cap, context handling, and secret-free errors.
- [x] Add httptest coverage for polling callback updates, offset payload, allowed updates, callback answering, reply markup removal, API rejection, invalid JSON, and context cancellation where relevant.
- [x] Run `make test` and fix failures before Task 3.

### Task 3: Run refresh callbacks in service mode

**Files:**
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] Introduce a small digest runner around `executeJob` that serializes startup, scheduled, and refresh-triggered runs with one shared lock.
- [x] Keep cron overlap semantics: scheduled runs are skipped when another digest is already running.
- [x] Start Telegram callback polling only in service mode, never in `-once`.
- [x] For callback data that does not match the refresh constant, advance offset and ignore it.
- [x] For valid refresh callbacks, trigger one digest run using the existing state path, dependencies, and logger.
- [x] When a refresh-triggered digest has sendable chunks, remove the clicked message's old button before sending the first new chunk.
- [x] If no books are sent by the refresh-triggered digest, leave the old button in place.
- [x] If another digest is already running, answer the callback and skip the duplicate refresh run.
- [x] Log callback polling/digest errors with stable snake_case attributes and no secrets.
- [x] Add tests for no callback loop in `-once`, callback-triggered job execution, concurrent callback skip, and old-button removal before new send.
- [x] Run `make test` and fix failures before Task 4.

### Task 4: Preserve digest invariants around callbacks

**Files:**
- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] Add a pre-delivery hook to `app.Dependencies` only if needed to guarantee old-button removal happens after renderable chunks are known and before the first new Telegram message.
- [x] Ensure the hook runs once per digest only when at least one chunk will be sent.
- [x] Ensure hook failure does not mark books sent before Telegram accepts their chunks.
- [x] Preserve flood-control retry behavior for chunks after the old-button removal hook.
- [x] Preserve oversized-listing behavior: renderable pending listings still send, oversized listings remain pending, and final sent chunk gets the refresh button.
- [x] Add tests for hook ordering relative to send/mark, hook not running when there are no chunks, and final-button behavior when one pending listing is oversized.
- [x] Run `make test` and fix failures before Task 5.

### Task 5: Update documentation

**Files:**
- Modify: `README.md`

- [x] Document that every sent digest ends with an `Обновить` inline button on the last message.
- [x] Document that `-once` sends the button but does not process callbacks after exit.
- [x] Document that service mode polls Telegram callback updates and a running service with the same bot can process a button sent by `-once`.
- [x] Document that pressing `Обновить` starts an out-of-schedule digest and leaves the old button in place when no new notification is sent.
- [x] Add or update README-adjacent tests only if docs examples or configuration parsing change.
- [x] Run `make test` and fix failures before Task 6.

### Task 6: Verify acceptance criteria

- [x] Run `make verify`.
- [x] Run `make lint` if not already covered by the final `make verify` output being inspected.
- [x] Run `go test -cover ./...` and verify coverage is at least 80% or report the exact package below threshold. Below threshold: `cmd/alib-fetcher` 54.5%, `internal/store` 76.2%.
- [x] Confirm no secrets appear in logs, errors, tests, or docs.
- [x] Confirm worktree contains only task-related source, test, and README changes.

### Task 7: Update internal guidance if needed

**Files:**
- Modify: `AGENTS.md` only if implementation changes durable repository workflow or invariants.

- [ ] Update `AGENTS.md` only if new callback/polling invariants need to be preserved by future agents.
- [ ] If `AGENTS.md` changes, add/update tests only when the documented invariant maps to executable behavior not already covered.
- [ ] Run `make test` after any internal guidance-related code/test adjustment.
