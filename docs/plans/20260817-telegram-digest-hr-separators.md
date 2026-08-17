# Разделители <hr/> между книгами в Telegram digest

## Overview

Перевести отправку digest на Telegram `sendRichMessage` с `rich_message.html` и заменить межкнижный
разделитель с пустой строки на `<hr/>`. Разделитель должен появляться только между
книгами внутри одного сообщения: перед первой и после последней книги его нет.

## Context

- Files involved:
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/telegram/sender.go`
  - `internal/telegram/sender_test.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
- Related patterns:
  - `digest.Render` уже собирает chunks и добавляет separator только когда `len(current.Books) > 0`.
  - Chunking уже считает separator в лимите и сбрасывает separator после split, что подходит для
    требования "нет разделителя по краям".
  - `internal/app.Service` может оставить интерфейс `Sender.Send(ctx, text, silent, attachRefresh)` без изменения:
    transport-детали остаются в `internal/telegram`.
  - Telegram sender уже использует общий `post`, общий разбор ошибок, `retry_after`, inline refresh button и
    `disable_notification`; это нужно сохранить.
- Dependencies:
  - Telegram Bot API Rich Messages: `sendRichMessage` принимает `rich_message`, а `InputRichMessage.html` описывает HTML-content.
  - Telegram Rich Message divider соответствует HTML tag `<hr/>`.
  - Official docs: https://core.telegram.org/bots/api#sendrichmessage and https://core.telegram.org/bots/api#inputrichmessage
  - No new Go module dependencies.

## Development Approach

- **Testing approach**: TDD for changed behavior: first update/add focused tests for expected payload and separators, then implement.
- Complete each task fully before moving to the next.
- Use repository patterns: small adapter changes, no new abstractions unless tests show real duplication.
- Use Makefile for verification commands.
- Preserve current digest invariants: chunking only between complete listings, per-chunk acknowledgement, final refresh button only, flood-control retry behavior.
- Preserve current `MESSAGE_LIMIT` behavior and count `<hr/>` as part of rendered rich HTML.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Обновить digest-разделитель

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`

- [x] Add or update a render test with multiple books in one chunk asserting exactly one `<hr/>` between adjacent books.
- [x] Assert no `<hr/>` appears before the first rendered book.
- [x] Assert no `<hr/>` appears after the last rendered book.
- [x] Change `digest.Render` separator from the current blank-line-only separator to an `<hr/>` separator.
- [x] Keep split behavior unchanged: when adding book plus separator would exceed `MESSAGE_LIMIT`, start a new chunk without leading `<hr/>`.
- [x] Update existing expected render strings affected by the new separator.
- [x] Run `make test` and fix failures before Task 2.

### Task 2: Перевести Telegram sender на sendRichMessage

**Files:**

- Modify: `internal/telegram/sender.go`
- Modify: `internal/telegram/sender_test.go`

- [x] Update sender tests to expect `/bot<token>/sendRichMessage` instead of `/sendMessage`.
- [x] Update sender tests to expect payload shape with `chat_id`, `rich_message.html`, `disable_notification`, and optional `reply_markup`.
- [x] Assert sender no longer sends `text`, `parse_mode`, or `link_preview_options` in rich-message payloads.
- [x] Implement `sendRichMessage` method constant and request payload using `rich_message: {"html": text}`.
- [x] Preserve inline `Обновить` button payload exactly for final chunks.
- [x] Preserve existing rejection, retry_after, context, oversized response, and transport error behavior through the existing `post` path.
- [x] Update sender comments to describe rich HTML messages instead of `sendMessage` parse-mode HTML.
- [x] Run `make test` and fix failures before Task 3.

### Task 3: Обновить сквозные тесты и документацию

**Files:**

- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `README.md`

- [x] Update main integration test payload structs to decode `rich_message.html`.
- [x] Update once-mode wiring assertions to expect `/sendRichMessage`.
- [x] Assert generated rich HTML contains `<hr/>` between two books and no edge divider.
- [x] Preserve assertions for chat ID, silent/audible chunk behavior, final refresh button, and token redaction.
- [x] Update README digest/Telegram transport wording to mention Rich Messages and `<hr/>` separators between listings.
- [x] Update README examples/descriptions only where behavior changed; do not add config options.
- [x] Run `make test` and fix failures before Task 4.

### Task 4: Verify acceptance criteria

**Files:**

- Modify: none expected

- [x] Run `make verify`.
- [x] Run `make coverage`.
- [x] Confirm `make coverage` reports total statement coverage at or above 80%.
- [x] Confirm tests cover all acceptance criteria: `<hr/>` only between books, no divider before first book, no divider after last book, and Telegram payload uses `sendRichMessage` with `rich_message.html`.
