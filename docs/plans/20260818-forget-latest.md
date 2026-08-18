# Удаление последних записей из состояния

## Overview

Добавить обслуживающий режим `-forget-latest N`. Он удаляет до `N` последних добавленных записей по убыванию `queue_order`, независимо от статуса доставки, и сразу завершает процесс. Режим не запускает загрузку Alib, Telegram, callback polling или scheduler. Команда `-forget-latest 6` позволит повторно обнаружить и отправить шесть удалённых записей при следующем digest.

## Context

- Files involved: `internal/store/store.go`, `internal/store/store_test.go`, `internal/process/job.go`, `internal/process/process_test.go`, `internal/config/config.go`, `internal/config/config_test.go`, `cmd/alib-fetcher/main.go`, `cmd/alib-fetcher/main_test.go`, `README.md`
- Related patterns:
  - `queue_order` выдаётся через bbolt sequence и определяет порядок обнаружения.
  - `Store.Prune` выполняет атомарное удаление внутри write-транзакции с проверкой context.
  - `executeJob` открывает БД только на время операции и объединяет ошибки операции и закрытия.
  - CLI использует стандартный пакет `flag`; тесты изолируют глобальные `flag.CommandLine` и `os.Args`.
  - Процесс пишет структурированные JSON-логи через `slog`.
- Dependencies: новые внешние зависимости не нужны; используется существующий bbolt.
- Accepted behavior:
  - интерфейс: числовой флаг `-forget-latest N`;
  - удаляются записи с наибольшим `queue_order`, включая sent и pending;
  - после удаления процесс немедленно завершается;
  - `-forget-latest` несовместим с `-once`;
  - положительное `N` обязательно; если записей меньше, удаляются все доступные;
  - записи с одинаковым или отсутствующим `queue_order` получают детерминированный fallback по `observed_at` и ключу.

## Development Approach

- **Testing approach**: TDD — сначала регрессионные тесты каждого поведения, затем минимальная реализация.
- Завершать каждую задачу полностью перед следующей.
- После каждой задачи запускать `make test`; продолжать только после успешного результата.
- Не добавлять maintenance-методы в `app.State`: удаление обходит digest use case и работает напрямую через storage/process lifecycle.
- Не сбрасывать bbolt sequence после удаления: повторно обнаруженные записи должны получить новые возрастающие `queue_order`.
- Каждая кодовая задача включает новые или обновлённые тесты.
- Все тесты должны пройти перед началом следующей задачи.

## Implementation Steps

### Task 1: Добавить атомарное удаление последних записей в хранилище

**Files:**

- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [x] Сначала добавить тесты `Store.DeleteLatest`: выбор максимальных `queue_order` при несовпадающем лексикографическом порядке ключей, удаление sent и pending записей, лимит больше размера БД.
- [x] Добавить тест отменённого context, подтверждающий отсутствие частичного удаления.
- [x] Добавить тест повторного обнаружения удалённой записи: она считается новой и получает новый `queue_order`, без перемотки bucket sequence.
- [x] Реализовать `DeleteLatest(ctx, limit) (int, error)` одной bbolt write-транзакцией: декодировать записи, отсортировать по свежести, удалить до лимита, вернуть фактическое количество.
- [x] Для нулевых или одинаковых `queue_order` применить детерминированный fallback: `observed_at`, затем bbolt key.
- [x] Возвращать контекстную ошибку с названием операции; при ошибке декодирования или отмене откатывать всю транзакцию.
- [x] Запустить `make test`; все тесты должны пройти до Task 2.

### Task 2: Добавить process-level maintenance-операцию

**Files:**

- Modify: `internal/process/job.go`
- Modify: `internal/process/process_test.go`

- [ ] Сначала добавить тесты операции: БД открывается, нужные записи удаляются, затем БД доступна для повторного открытия.
- [ ] Добавить тест фактического количества удалённых записей при `N`, превышающем размер БД.
- [ ] Реализовать отдельную экспортируемую process-функцию для открытия состояния, вызова `DeleteLatest` и гарантированного закрытия с `errors.Join`.
- [ ] Записать успешный структурированный лог `state.forget_latest.completed` с typed-полями `requested` и `deleted`; ошибки вернуть вызывающему коду без запуска digest.
- [ ] Не создавать `app.Service`, fetcher, sender, scheduler или callback listener.
- [ ] Запустить `make test`; все тесты должны пройти до Task 3.

### Task 3: Подключить и провалидировать CLI-режим

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [ ] Сначала добавить тесты чтения `STATE_PATH` отдельно от полной Telegram/digest-конфигурации: значение из environment и существующий default.
- [ ] Добавить числовой флаг `-forget-latest` с отключённым значением по умолчанию.
- [ ] Добавить тесты ошибок для отрицательного значения и комбинации `-forget-latest N` с `-once`.
- [ ] Вынести минимальное получение `STATE_PATH`, чтобы maintenance-режим не требовал `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` и остальные digest-настройки.
- [ ] При положительном значении создать signal-aware context, вызвать maintenance-операцию до создания Alib/Telegram adapters и сразу вернуть её результат.
- [ ] Сохранить существующее поведение обычного и `-once` режимов без изменения сигнатуры digest lifecycle.
- [ ] Запустить `make test`; все тесты должны пройти до Task 4.

### Task 4: Документировать и закрепить пользовательский сценарий

**Files:**

- Modify: `README.md`
- Modify: `cmd/alib-fetcher/main_test.go`

- [ ] Добавить end-to-end CLI-тест документированного сценария `STATE_PATH=... alib-fetcher -forget-latest 6`: удалить шесть максимальных `queue_order`, оставить остальные, не выполнять HTTP-запросы.
- [ ] В разделе Run описать назначение, команду, немедленное завершение и независимость от Telegram credentials.
- [ ] Явно предупредить, что операция необратимо удаляет записи из состояния, затрагивает sent и pending записи, а следующий digest обнаружит доступные на Alib удалённые книги заново.
- [ ] Описать поведение при количестве записей меньше `N` и несовместимость с `-once`.
- [ ] Запустить `make test`; все тесты должны пройти до Task 5.

### Task 5: Verify acceptance criteria

- [ ] Запустить `make verify`: formatting check, полный strict lint, race-enabled shuffled tests и reproducible build должны пройти.
- [ ] Запустить `make coverage`; итоговое statement coverage должно быть не ниже 80%.
- [ ] Проверить регрессиями, что `-forget-latest 6` удаляет ровно шесть последних записей по `queue_order`, независимо от sent/pending.
- [ ] Проверить, что maintenance-режим не обращается к Alib/Telegram и не запускает scheduler/callback polling.
- [ ] Проверить итоговый diff на отсутствие посторонних изменений и рассинхронизации README.

### Task 6: Update documentation

**Files:**

- Modify: `README.md`

- [ ] Подтвердить, что `README.md` содержит актуальную команду, ограничения и последствия удаления.
- [ ] Не менять `CLAUDE.md`/`AGENTS.md`, если внутренние соглашения проекта не изменились.
