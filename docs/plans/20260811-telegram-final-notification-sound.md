# Telegram Final Notification Sound

## Overview

Зафиксировать и при необходимости восстановить поведение: когда digest разбивается на несколько Telegram-сообщений, только последнее сообщение отправляется со звуком, а все предыдущие отправляются silent. Обязательно добавить явный регрессионный тест, чтобы это поведение не потерялось снова.

## Context

- Files involved:
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/telegram/sender.go`
  - `internal/telegram/sender_test.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
- Related patterns:
  - `app.Service.Run` уже управляет порядком chunk delivery и вызывает `Sender.Send(ctx, text, silent, attachRefresh)`.
  - `internal/app/service_test.go` уже использует fakeSender с записью `silent` и `attachRefresh`.
  - `internal/telegram/sender_test.go` проверяет JSON payload `sendMessage`, включая `disable_notification`.
  - `README.md` уже документирует: при нескольких сообщениях звук есть только у финального.
- Dependencies:
  - Внешние зависимости не нужны.
  - Проверка через `make verify`.

## Development Approach

- **Testing approach**: TDD. Сначала добавить или усилить регрессионный тест, который падает при инверсии/потере silent-флага.
- Complete each task fully before moving to the next.
- Следовать существующему стилю Given / When / Then и testify require/assert.
- Не менять Telegram API контракт без необходимости.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Add explicit app-level regression test for multi-message sound behavior

**Files:**
- Modify: `internal/app/service_test.go`

- [x] Добавить отдельный тест `Test_Service_sends_only_final_chunk_with_sound` или переименовать/расширить существующий тест так, чтобы его цель была явно про звук.
- [x] Подобрать pending books и `MESSAGE_LIMIT` так, чтобы digest стабильно рендерился минимум в 3 отдельных chunks.
- [x] Проверить, что `sender.silent` равен `[]bool{true, true, false}`.
- [x] Проверить, что порядок сообщений и acknowledgements сохраняется.
- [x] Проверить, что `attachRefresh` остается только на последнем chunk: `[]bool{false, false, true}`.
- [x] Запустить `go test ./internal/app -run Test_Service_sends_only_final_chunk_with_sound -count=1` и убедиться, что тест отражает текущую поломку или защищает исправление.

### Task 2: Fix chunk silent flag selection in service orchestration

**Files:**
- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`

- [x] Проверить текущий расчет `silent` около цикла отправки chunks.
- [x] Исправить логику так, чтобы `silent=true` было для каждого `index < len(chunks)-1`, а `silent=false` только для `index == len(chunks)-1`.
- [x] Убедиться, что retry того же chunk повторяет тот же `silent` и `attachRefresh` flags.
- [x] Убедиться, что oversized pending listings не меняют выбор финального renderable chunk.
- [x] Обновить существующие тесты, если они стали дублировать новый регрессионный тест или завязаны на старое имя.
- [x] Запустить `go test ./internal/app -count=1`.

### Task 3: Verify Telegram transport maps silent to disable_notification

**Files:**
- Modify: `internal/telegram/sender_test.go`
- Modify: `internal/telegram/sender.go`

- [ ] Добавить или усилить transport-level тест, который вызывает `Sender.Send(..., silent=false, attachRefresh=false)` и проверяет `disable_notification=false` в JSON payload.
- [ ] Сохранить существующий тест для `silent=true` и `disable_notification=true`.
- [ ] Если `sender.go` инвертирует или теряет флаг, исправить только `payload.DisableNotification` assignment.
- [ ] Запустить `go test ./internal/telegram -count=1`.

### Task 4: Add end-to-end wiring coverage for multiple Telegram messages

**Files:**
- Modify: `cmd/alib-fetcher/main_test.go`

- [ ] Расширить run wiring test или добавить новый тест с httptest Alib page и малым `MESSAGE_LIMIT`, чтобы сервис отправил несколько Telegram `sendMessage` requests.
- [ ] Собрать `disable_notification` по всем Telegram requests.
- [ ] Проверить, что все requests кроме последнего имеют `disable_notification=true`.
- [ ] Проверить, что последний request имеет `disable_notification=false` и содержит refresh button.
- [ ] Проверить, что bot token не попадает в тело запроса или логи, как в существующем тесте.
- [ ] Запустить `go test ./cmd/alib-fetcher -count=1`.

### Task 5: Verify acceptance criteria

**Files:**
- Modify: none

- [ ] Запустить `make verify`.
- [ ] Убедиться, что форматирование, lint, race-enabled shuffled tests и build проходят.
- [ ] Проверить, что тесты явно покрывают single chunk, multiple chunks, retry same final chunk, Telegram JSON payload и main wiring для нескольких сообщений.
- [ ] Проверить, что coverage для затронутых пакетов не снижена и остается выше 80% там, где применяется локальная метрика проекта.

### Task 6: Update documentation if behavior text changed

**Files:**
- Modify: `README.md`, only if needed

- [ ] Проверить `README.md`: поведение уже должно быть описано как “only the final message uses the normal notification sound; earlier messages are silent”.
- [ ] Если формулировка отсутствует или стала неточной после правки, обновить `README.md`.
- [ ] Если документация не менялась, оставить `README.md` без изменений.
- [ ] После изменения документации снова запустить `make verify`.
