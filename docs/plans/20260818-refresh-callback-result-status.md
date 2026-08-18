---
# Итоговый статус обновления

## Overview

Оставлять Telegram callback в состоянии `Loading...` до завершения refresh-digest. Затем:

- при успешной проверке с `Result.New == 0` показывать toast `Новых книг нет`;
- при найденных книгах завершать `Loading...` без текста;
- при ошибке показывать текст этой ошибки в toast.

Длительность toast определяет клиент Telegram; Bot API не позволяет гарантировать ровно 10 секунд.

## Context

- Files involved:
  - `internal/process/job.go`
  - `internal/process/process.go`
  - `internal/process/process_test.go`
  - `internal/process/runner.go`
  - `internal/process/runner_test.go`
  - `internal/process/callbacks.go`
  - `internal/process/callbacks_test.go`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - `app.Result.New` содержит число впервые обнаруженных записей.
  - `app.Service.Run` возвращает частичный результат и ошибку.
  - `answerRefreshCallback` централизует ответы callback и логирование `callback.answer_failed`.
  - Refresh-digest выполняется асинхронно под общим runner lock.
  - Старую кнопку удаляет `BeforeDelivery` только при наличии отправляемых chunks.
  - Telegram-адаптер очищает ошибки от bot token.
- Dependencies: новые зависимости не нужны.

## Development Approach

- **Testing approach**: Regular — сначала минимальное изменение кода, затем регрессионные тесты.
- Complete each task fully before moving to the next.
- Сохранить текущие правила lock, callback polling, удаления старой кнопки и acknowledgement книг.
- Ошибка имеет приоритет над значением `Result.New`: при частично выполненном digest показывается ошибка.
- Не раскрывать bot token в callback-тексте.
- **CRITICAL: every task that changes code MUST include new/updated tests.**
- **CRITICAL: all tests must pass before starting the next task.**

## Implementation Steps

### Task 1: Передать итог digest в refresh-runner

**Files:**

- Modify: `internal/process/job.go`
- Modify: `internal/process/process.go`
- Modify: `internal/process/process_test.go`
- Modify: `internal/process/runner.go`
- Modify: `internal/process/runner_test.go`

- [x] Изменить `executeJob`, чтобы он возвращал `app.Result` вместе с ошибкой, включая частичный результат при сбое.
- [x] Сохранить закрытие bbolt, объединение ошибок закрытия и существующий `digest.completed` log.
- [x] Адаптировать once/startup/scheduled пути: once возвращает только ошибку наружу, остальные игнорируют результат.
- [x] Добавить для refresh completion hook, который вызывается после полного завершения `executeJob` и получает итоговые `app.Result` и `error`.
- [x] Сохранить общий runner lock, асинхронный refresh и `BeforeDelivery`.
- [x] Обновить process/runner-тесты: проверить передачу успешного результата, частичного результата, ошибки и ошибки закрытия state.
- [x] Выполнить `make test`; все тесты должны пройти до Task 2.

### Task 2: Завершать callback согласно результату проверки

**Files:**

- Modify: `internal/process/callbacks.go`
- Modify: `internal/process/callbacks_test.go`
- Modify: `README.md`
- Modify: `AGENTS.md`

- [x] Убрать ранний toast `Проверяю новые книги`, чтобы Telegram сохранял `Loading...` во время проверки.
- [x] После успешного refresh с `Result.New == 0` вызвать `AnswerCallback` с текстом `Новых книг нет`.
- [x] После успешного refresh с `Result.New > 0` вызвать `AnswerCallback` с пустым текстом.
- [x] При ошибке вызвать `AnswerCallback` с `err.Error()`, сохранив очистку Telegram-ошибок от bot token.
- [x] Сохранить `digest.failed` для ошибок digest и `callback.answer_failed` для ошибок отправки итогового callback.
- [x] Сохранить немедленные ответы `Проверка уже выполняется` и `Кнопка недоступна`; неизвестные callback data по-прежнему игнорировать.
- [x] Расширить callback-тесты: до завершения блокирующего fetch ответа нет; пустой успешный результат даёт `Новых книг нет`; найденная книга даёт пустой ответ; ошибка даёт её текст.
- [x] Проверить тестами, что удаление кнопки и отправка сохраняют прежние условия и порядок, а итоговый callback отправляется после digest.
- [x] Обновить README и runtime invariants в AGENTS.md: описать три итоговых статуса и указать, что время показа toast контролирует Telegram.
- [x] Выполнить `make test`; все тесты должны пройти до Task 3.

### Task 3: Verify acceptance criteria

- [ ] Выполнить `make verify`.
- [ ] Выполнить `make coverage` и подтвердить общее statement coverage не ниже 80%.
- [ ] Проверить итоговый diff на отсутствие изменений Telegram transport/API payload, утечек bot token и посторонних файлов.
---
