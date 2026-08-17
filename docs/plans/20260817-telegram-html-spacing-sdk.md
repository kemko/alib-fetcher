# Восстановление отступов Rich HTML и переход на Telegram SDK

## Overview

Восстановить пустые строки между смысловыми блоками книги. Все переносы Telegram HTML
кодировать через `<br/>`, без literal `\n`. Одна пустая строка представляется парой `<br/><br/>`.
С описанием: основная информация → пустая строка → описание → пустая строка →
остальные данные. Без описания: основная информация → ровно одна пустая строка →
остальные данные.

Заменить собственную реализацию Telegram Bot API на `github.com/go-telegram/bot v1.23.0`. SDK должен
обслуживать `sendRichMessage`, callback queries, long polling, inline keyboard, `answerCallbackQuery`,
`editMessageReplyMarkup`, custom API URL и `retry_after`.

## Context

- Files involved:
  - `go.mod`
  - `go.sum`
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/app/service_test.go`
  - `internal/telegram/sender.go`
  - `internal/telegram/sender_test.go`
  - `internal/telegram/sender_internal_test.go`
  - `internal/telegram/updates.go`
  - `internal/telegram/updates_test.go`
  - `internal/process/callbacks.go`
  - `internal/process/callbacks_test.go`
  - `internal/process/process.go`
  - `internal/process/process_test.go`
  - `cmd/alib-fetcher/main.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - До commit `1129b41` `renderBook` разделял смысловые блоки через `"\n\n"`.
  - После Rich HTML migration блоки стали соединяться без separator, поэтому визуальные отступы
    исчезли.
  - Текущий `lineBreak` равен `<br>` и уже заменяет переводы строк внутри динамических полей.
  - Новый единый HTML-контракт: каждый перенос — `<br/>`, межблочный отступ — `<br/><br/>`, literal
    CR/LF в готовом сообщении отсутствуют.
  - `app.Service` владеет chunk ordering, acknowledgement и flood-control retry.
  - `process` владеет chat filtering, runner lock, refresh lifecycle и стабильными log events.
  - `telegram.Callback` остаётся внутренним DTO, изолирующим process layer от типов SDK.
- Dependencies:
  - Pin `github.com/go-telegram/bot v1.23.0`.
  - Release: https://github.com/go-telegram/bot/releases/tag/v1.23.0
  - Official Bot API: https://core.telegram.org/bots/api
  - SDK берёт на себя endpoints, request/response models, serialization, update offsets и polling retry/backoff.
  - Собственный raw Bot API fallback не сохраняется.

## Development Approach

- **Testing approach**: TDD — сначала regression tests, затем минимальная реализация.
- Complete each task fully before moving to the next.
- После каждой кодовой задачи выполнять `make test`; все тесты должны пройти.
- Сохранить app/process policy отдельно от Telegram SDK adapter.
- Сохранить timeout, custom `TELEGRAM_API_BASE`, 1 MiB response cap, context cancellation и token redaction как локальные
  transport/security policies.
- Сохранить `telegram.ErrRequest`, `telegram.ErrRejected` и `RetryAfter() time.Duration` на границе adapter, преобразуя SDK
  errors без собственного Bot API parsing.
- Не добавлять второй Telegram client, raw HTTP fallback или новый config switch.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Восстановить HTML-переносы и межблочные отступы

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`
- Modify: `internal/app/service_test.go`

- [x] Добавить точный render test для книги с описанием: `main → <br/><br/> → content → <br/><br/> → details`.
- [x] Добавить точный render test для книги без описания: `main → <br/><br/> → details`, без
  дополнительного separator отсутствующего content.
- [x] Заменить canonical line break `<br>` на `<br/>`.
- [x] Нормализовать `\r\n`, `\r` и `\n` внутри динамических полей в `<br/>`.
- [x] Соединять непустые paragraph blocks через `<br/><br/>`; не создавать пустой content paragraph.
- [x] Сохранить отдельный paragraph и межблочный отступ перед ссылкой `Купить`.
- [x] Добавить assertion, что готовый Telegram HTML не содержит literal `\r` или `\n`.
- [x] Обновить expected HTML, rune limits и chunk boundaries с учётом длины `<br/>`.
- [x] Подтвердить тестами escaping, `<br/>` внутри multiline fields и `<hr/>` только между книгами.
- [x] Выполнить `make test`; все тесты должны пройти до Task 2.

### Task 2: Перевести Telegram API operations на SDK

**Files:**

- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `internal/telegram/sender.go`
- Modify: `internal/telegram/sender_test.go`
- Modify or remove obsolete cases: `internal/telegram/sender_internal_test.go`
- Modify: `internal/telegram/updates.go`
- Modify: `internal/telegram/updates_test.go`

- [x] Добавить и зафиксировать `github.com/go-telegram/bot v1.23.0`.
- [x] Создавать SDK bot с `WithServerURL`, configured HTTP client/timeout, `WithSkipGetMe` и разрешением только
  `callback_query` updates.
- [x] Реализовать отправку через SDK `SendRichMessage` с `models.InputRichMessage{HTML: text}`.
- [x] Передавать `chat_id`, `disable_notification` и inline-кнопку `Обновить` через типы SDK.
- [x] Реализовать callback acknowledgement через SDK `AnswerCallbackQuery`.
- [x] Реализовать удаление старой клавиатуры через SDK `EditMessageReplyMarkup`.
- [x] Удалить собственные Bot API method constants, payload/response DTO, endpoint construction, JSON serialization и response
  decoding.
- [x] Сохранить generic HTTP response-size limit и безопасную нормализацию ошибок без знания Bot API JSON
  schema.
- [x] Преобразовывать SDK rejection и `TooManyRequestsError` в существующие adapter contracts, включая Telegram
  description и положительный `RetryAfter()`.
- [x] Добавить/обновить tests для Rich HTML message, silent flag, final refresh button, callback answer, markup removal, 429 retry,
  cancellation, invalid config и отсутствия token/API URL в ошибках.
- [x] Выполнить `make test`; все тесты должны пройти до Task 3.

### Task 3: Передать SDK управление callback polling

**Files:**

- Modify: `internal/telegram/updates.go`
- Modify: `internal/telegram/updates_test.go`
- Modify: `internal/process/callbacks.go`
- Modify: `internal/process/callbacks_test.go`
- Modify: `internal/process/process.go`
- Modify: `internal/process/process_test.go`

- [x] Заменить pull-контракт `PollCallbacks(offset)` на callback listener, запускающий SDK `Bot.Start(ctx)`.
- [x] Преобразовывать SDK callback query в узкий `telegram.Callback`; безопасно обрабатывать
  отсутствующее или inaccessible message.
- [x] Зарегистрировать обработку stable `telegram.RefreshCallbackData`; unknown callback data игнорируется,
  пока SDK самостоятельно продвигает update offset.
- [x] Удалить собственные offset bookkeeping, idle delay и poll-error retry loop из process layer.
- [x] Передавать SDK polling errors в существующий `callback.poll_failed` log event без token.
- [x] Запускать SDK listener только в service mode; `-once` продолжает отправлять кнопку без polling.
- [x] На shutdown дождаться завершения SDK listener и refresh runner.
- [x] Сохранить chat filtering, callback answers, shared runner lock, duplicate refresh skip и продолжение polling во время
  refresh digest.
- [x] Сохранить удаление старой кнопки только после появления renderable chunks и до первой
  новой отправки.
- [x] Обновить tests для service/once lifecycle, unknown callback, foreign chat, duplicate press, graceful cancellation, poll error logging
  и ordering `answer → remove → send`.
- [x] Выполнить `make test`; все тесты должны пройти до Task 4.

### Task 4: Обновить bootstrap, сквозные тесты и документацию

**Files:**

- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `README.md`
- Modify: `AGENTS.md`

- [ ] Передать один SDK-backed Telegram adapter одновременно как app sender и callback listener.
- [ ] Обновить once-mode integration tests для нового SDK wire behavior без привязки к удалённым
  собственным DTO.
- [ ] Закрепить integration tests: все переводы строк представлены `<br/>`, literal CR/LF отсутствуют.
- [ ] Закрепить integration tests отступов для книги с описанием и без описания.
- [ ] Подтвердить integration tests: rich message, silent intermediate chunks, final button, custom API base и отсутствие callback
  polling в `-once`.
- [ ] Обновить README: `<br/>` для переносов, `<br/><br/>` для пустых строк и поддерживаемый Telegram
  SDK вместо собственного Bot API client.
- [ ] Обновить README dependency/operational text без изменения configuration contract.
- [ ] Обновить AGENTS.md: Telegram operations и polling принадлежат SDK; app/process invariants остаются
  локальными; HTML не содержит literal CR/LF.
- [ ] Выполнить `make test`; все тесты должны пройти до Task 5.

### Task 5: Verify acceptance criteria

- [ ] Выполнить `make verify`; formatting, lint, race/shuffle/no-cache tests и build должны пройти.
- [ ] Выполнить `make coverage`; total statement coverage должен быть не ниже 80%.
- [ ] Подтвердить tests: все переносы представлены `<br/>`; literal `\r` и `\n` в готовом HTML
  отсутствуют.
- [ ] Подтвердить tests: с content присутствуют два требуемых межблочных отступа; без content
  — один.
- [ ] Подтвердить tests: send, callback polling, answer и markup edit проходят через SDK.
- [ ] Подтвердить отсутствие собственных Bot API request/response structs, method strings и offset loop.
- [ ] Подтвердить сохранение `retry_after`, cancellation, final-button, chat filtering, runner-lock и acknowledgement invariants.
- [ ] Проверить diff на отсутствие credentials, state DB, generated binaries и посторонних изменений.
- [ ] Stage только task-related files и создать сфокусированный Conventional Commit после успешной
  проверки.
