# Приёмка и точечное усиление alib-fetcher

## Overview

Архитектура проекта принята: `cmd` остаётся тонким bootstrap-слоем, `internal/app` содержит policy,
`internal/process` — lifecycle/concurrency, adapters разделены по пакетам. Полный рефакторинг не нужен.
Текущая база качественная: `make verify` проходит, 94 теста проходят с race detector, покрытие —
85,9%. Нужны четыре ограниченных улучшения:

- fail-fast validation `TELEGRAM_CHAT_ID`;
- строгая обработка Telegram API responses;
- защита bbolt от маскировки повреждённых JSON records как legacy markers;
- проверка контейнера до merge и защита локальных секретов/build context.

## Context

- Files involved:
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/telegram/sender.go`
  - `internal/telegram/sender_test.go`
  - `internal/telegram/sender_internal_test.go` при необходимости custom transport
  - `internal/store/store.go`
  - `internal/store/store_test.go`
  - `.gitignore`
  - `.dockerignore`
  - `.github/workflows/ci.yml`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - Ошибки конфигурации оборачиваются в `config.ErrInvalid`.
  - Transport failures Telegram определяются через `telegram.ErrRequest`; rejected API responses — через
    `telegram.ErrRejected`.
  - Legacy bbolt markers мигрируются внутри транзакции `store.Open`.
  - Тесты используют testify, `httptest`, временные bbolt databases и `t.Parallel()` там, где нет
    process-global state.
  - Канонический quality gate — `make verify`.
- Dependencies:
  - Новые runtime-зависимости не нужны.
  - Container verification требует доступного Docker/BuildKit в CI.
- Acceptance baseline:
  - `make verify`: passed.
  - Race-enabled tests: 94 passed in 8 packages.
  - Statement coverage: 85.9%.
  - Worktree перед приёмкой чистый.

## Development Approach

- **Testing approach**: TDD — сначала regression tests на каждый найденный дефект, затем минимальная
  реализация.
- Complete each task fully before moving to the next.
- Сохранить текущие package boundaries; новые архитектурные слои не вводить.
- Сохранить legacy marker migration, delivery ordering, retry и acknowledgement semantics.
- Не раскрывать Telegram token в ошибках или логах.
- После полной проверки создать один или несколько focused Conventional Commits только из task-related
  changes.
- **CRITICAL: every task that modifies code MUST include new/updated tests.**
- **CRITICAL: all tests must pass before starting next task.**

## Implementation Steps

### Task 1: Усилить validation Telegram chat ID

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] Добавить regression tests: принимать signed decimal `int64` chat ID и непустой `@channel` username без
  whitespace.
- [ ] Добавить regression tests: отклонять произвольный текст без `@`, пустой username `@`, whitespace и
  numeric overflow.
- [ ] Реализовать минимальный private validator для заявленного configuration contract.
- [ ] Возвращать `config.ErrInvalid` с именем переменной, не включая token или другие секреты.
- [ ] Исправить существующие negative tests, использующие невалидный placeholder `chat`, чтобы проверялся
  целевой параметр.
- [ ] Run `make test` and `make lint`; both must pass before Task 2.

### Task 2: Сделать Telegram response handling строгим и однозначным

**Files:**

- Modify: `internal/telegram/sender.go`
- Modify: `internal/telegram/sender_test.go`
- Create: `internal/telegram/sender_internal_test.go` only if custom `RoundTripper` is required

- [ ] Добавить regression test: non-200 plain-text/malformed response возвращает `telegram.ErrRejected` и HTTP
  status, а не generic JSON decode error.
- [ ] Добавить regression test: body read failure классифицируется как `telegram.ErrRequest`; canceled context
  остаётся доступен через `errors.Is`.
- [ ] Добавить regression tests для response больше 1 MiB и trailing non-whitespace data после JSON document.
- [ ] Прочитать максимум `maxAPIResponseBytes+1`, явно отклонить oversized response и декодировать ровно один
  JSON document.
- [ ] Для non-200/`ok:false` сохранить description и `retry_after`; при невалидном error body использовать HTTP
  status.
- [ ] После полного успешного чтения не превращать подтверждённый `ok:true` delivery в retryable failure только
  из-за `Body.Close`; close error присоединять только к уже существующей read/protocol error.
- [ ] Сохранить token-safe errors: transport details с URL/token наружу не возвращать.
- [ ] Run `make test` and `make lint`; both must pass before Task 3.

### Task 3: Не маскировать повреждение state DB под legacy migration

**Files:**

- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] Добавить regression test: malformed JSON object в `sent_books` заставляет `store.Open` завершиться ошибкой
  и не перезаписывается legacy record.
- [ ] Добавить regression test: JSON record с `Book.BuyURL`, не совпадающим с bbolt key, отклоняется как corrupt
  state.
- [ ] Сохранить tests миграции RFC3339 marker и opaque legacy marker без немедленного pruning.
- [ ] Отличать JSON-object records новой схемы от raw legacy markers до запуска migration.
- [ ] Для structured record возвращать decode/validation error вместо silent conversion в sent marker.
- [ ] Проверять invariant `record.Book.BuyURL == string(bucketKey)` во всех read/update paths.
- [ ] Сохранить transactional initialization: при одной повреждённой записи никакие соседние migrations не
  должны частично фиксироваться.
- [ ] Run `make test` and `make lint`; both must pass before Task 4.

### Task 4: Проверять контейнер до merge и исключить секреты из repository/build context

**Files:**

- Modify: `.gitignore`
- Modify: `.dockerignore`
- Modify: `.github/workflows/ci.yml`

- [ ] Добавить `.env`/`.env.*` в `.gitignore`, оставив возможность tracked `.env.example`.
- [ ] Исключить `.env*`, `data/`, state databases и `.ralphex/` из Docker build context.
- [ ] Добавить automated checks через `git check-ignore --no-index` для representative `.env`, `.env.local` и
  `data/state.db`.
- [ ] Перестроить image job: после `verify` собирать Docker image на PR и push; login/push `latest` выполнять
  только для успешного push в `master`.
- [ ] Не выполнять две одинаковые image builds на `master`.
- [ ] Проверить Compose с dummy required environment через `docker compose config`.
- [ ] Собрать production Dockerfile локально или в эквивалентном BuildKit environment.
- [ ] Run `make verify`; must pass before Task 5.

### Task 5: Verify acceptance criteria

**Files:**

- Modify tests only if verification exposes a real regression or missing assertion.

- [ ] Run full canonical gate: `make verify`.
- [ ] Run coverage using a temporary file outside repository and verify total statement coverage remains at
  least 80%.
- [ ] Run production Docker image build.
- [ ] Verify numeric and `@channel` chat IDs remain supported; invalid values fail before process startup.
- [ ] Verify Telegram flood-control `retry_after`, context cancellation and token secrecy remain intact.
- [ ] Verify valid legacy markers still migrate while corrupt structured records fail without mutation.
- [ ] Verify PR workflow builds but does not push image; successful `master` push builds once and publishes
  `latest`.
- [ ] Inspect final diff and index; include only task-related changes.

### Task 6: Update documentation

**Files:**

- Modify: `README.md`
- Modify: `AGENTS.md`

- [ ] Document accepted `TELEGRAM_CHAT_ID` forms and fail-fast validation.
- [ ] Document that malformed structured state records stop startup instead of being silently migrated; retain
  backup/rollback guidance.
- [ ] Update CI description: PRs validate image build, only successful `master` pushes publish.
- [ ] Document `.env` handling without adding real credentials or example secrets.
- [ ] Update repository guide invariants and CI map where behavior changed.
- [ ] Run `make verify` after documentation changes.
- [ ] Commit verified task-related changes using concise Conventional Commit message(s).
