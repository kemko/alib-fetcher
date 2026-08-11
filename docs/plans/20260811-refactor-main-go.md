# Пересмотр структуры проекта: разгрузить cmd/alib-fetcher/main.go

## Overview

Без изменения поведения вынести process-level orchestration из `cmd/alib-fetcher/main.go` в новый пакет `internal/process`. `main.go` должен остаться тонким bootstrap-слоем: логгер, флаги, загрузка config, создание adapters, signal context, запуск process. Scheduler, runner lock, callback polling, refresh handling и open/close state будут жить рядом в `internal/process`.

## Context

- Files involved:
  - `cmd/alib-fetcher/main.go`
  - `cmd/alib-fetcher/main_test.go`
  - `internal/process/process.go` (new)
  - `internal/process/job.go` (new)
  - `internal/process/runner.go` (new)
  - `internal/process/callbacks.go` (new)
  - `internal/process/scheduler.go` (new)
  - `internal/process/*_test.go` (new)
  - `README.md` (inspect/update only if structure docs become stale)
  - `AGENTS.md` or `CLAUDE.md` if present and structure guidance becomes stale
- Related patterns:
  - Existing adapters stay in `internal/alib`, `internal/store`, `internal/telegram`.
  - Use-case policy stays in `internal/app`.
  - Command package should only wire dependencies and process lifecycle.
  - Tests use package-local fakes, `t.Parallel()` where safe, `httptest`/temp dirs, no live network.
  - Quality gate remains `make verify`.
- Dependencies:
  - No new external dependencies.
  - Keep existing `github.com/robfig/cron/v3` in process lifecycle package.

## Development Approach

- **Testing approach**: Regular, because this is behavior-preserving refactor with existing characterization tests.
- Complete each task fully before moving to next.
- Keep public surface minimal: `internal/process.Run`, `internal/process.Settings`, `internal/process.CallbackClient`; everything else unexported unless tests or command wiring require otherwise.
- Preserve log event names and typed snake_case slog attributes exactly.
- Preserve callback semantics, runner lock semantics, `-once` behavior, state DB open lifetime, and cron startup/shutdown order.
- **CRITICAL: every task that modifies code MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Create internal/process boundary

**Files:**
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`
- Create: `internal/process/process.go`
- Create: `internal/process/job.go`
- Create: `internal/process/process_test.go`

- [x] Create `internal/process.Settings` with cron spec, location, state path, and run-on-startup fields currently held by `processSettings`.
- [x] Create `internal/process.CallbackClient` matching Telegram callback operations currently required by main.
- [x] Move `runProcess` into `internal/process.Run`, keeping once-mode short-circuit before callback polling.
- [x] Move `executeJob` into `internal/process` as unexported job execution used by `Run`.
- [x] Update `cmd/alib-fetcher/main.go` to call `process.Run` and keep only bootstrap responsibilities.
- [x] Move/update tests for once-mode and state DB close behavior into `internal/process`.
- [x] Run `go test ./internal/process ./cmd/alib-fetcher` and fix failures before Task 2.

### Task 2: Move scheduler lifecycle

**Files:**
- Modify: `internal/process/process.go`
- Create: `internal/process/scheduler.go`
- Modify/Create: `internal/process/scheduler_test.go`

- [x] Move `runScheduler` into `internal/process/scheduler.go`.
- [x] Keep startup digest before `scheduler.Start()` when `RunOnStartup` is true.
- [x] Keep `RUN_ON_STARTUP=false` behavior: no startup job, scheduler still starts.
- [x] Preserve graceful shutdown: wait on context, then wait for `scheduler.Stop().Done()`.
- [x] Move/update scheduler tests for startup-enabled and startup-disabled behavior.
- [x] Run `go test ./internal/process ./cmd/alib-fetcher` and fix failures before Task 3.

### Task 3: Move digest runner concurrency

**Files:**
- Create: `internal/process/runner.go`
- Modify: `internal/process/process.go`
- Modify/Create: `internal/process/runner_test.go`

- [x] Move `digestRunner` into `internal/process/runner.go`.
- [x] Keep shared lock across startup, scheduled, and refresh-triggered digests.
- [x] Keep scheduled and refresh-triggered digests non-blocking when another digest runs.
- [x] Keep refresh digests in background and expose only internal wait behavior used by `Run`.
- [x] Preserve trigger-specific `digest.failed` log attribute values: `startup`, `scheduled`, `refresh`.
- [x] Move/update tests for skipped scheduled digest and duplicate refresh while background digest runs.
- [x] Run `go test ./internal/process ./cmd/alib-fetcher` and fix failures before Task 4.

### Task 4: Move callback polling and refresh handling

**Files:**
- Create: `internal/process/callbacks.go`
- Modify: `internal/process/process.go`
- Modify/Create: `internal/process/callbacks_test.go`

- [x] Move `startCallbackPolling`, `pollRefreshCallbacks`, `waitForCallbackPoll`, and `handleRefreshCallback` into `internal/process/callbacks.go`.
- [x] Keep callback polling active while refresh digest runs.
- [x] Keep unknown callback data ignored after offset advances.
- [x] Keep poll error backoff at 5s and idle delay at 1s, honoring context cancellation.
- [x] Keep refresh callback answers: `Проверяю новые книги` when started, `Проверка уже выполняется` when skipped.
- [x] Keep old reply markup removal as `BeforeDelivery`, so removal happens only when renderable chunks exist and before first send.
- [x] Move/update callback tests for offset advancement, unknown data, refresh send ordering, no-books behavior, skip behavior, duplicate refresh, and poll backoff.
- [x] Run `go test ./internal/process ./cmd/alib-fetcher` and fix failures before Task 5.

### Task 5: Clean command package and imports

**Files:**
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go` or remove if fully migrated
- Modify/Create: `cmd/alib-fetcher/main_test.go` only if command-level behavior remains testable without process internals

- [x] Remove process-only constants, interfaces, structs, and helper functions from `main.go`.
- [x] Keep `main.go` focused on logger setup, `-once`, `config.Load`, adapter construction, signal context, and `process.Run`.
- [x] Ensure `cmd/alib-fetcher` imports no `cron`, `store`, or process-internal concurrency packages.
- [x] Keep token handling unchanged; never log or expose Telegram token.
- [x] Add or update command-level smoke test only if bootstrap behavior has a stable test seam after refactor.
- [x] Run `go test ./cmd/alib-fetcher ./internal/process` and fix failures before Task 6.

### Task 6: Verify acceptance criteria

**Files:**
- Modify tests only if verification exposes missing coverage or broken assumptions.

- [x] Run `make fmt-check`.
- [x] Run `make lint`.
- [x] Run `make test`.
- [x] Run `make build`.
- [x] Run `make verify`.
- [x] Run `go test -cover ./...` and verify overall coverage is at least 80%.
- [x] Confirm `cmd/alib-fetcher/main.go` is substantially smaller and contains only bootstrap wiring.
- [x] Confirm `-once`, scheduler startup, callback polling, refresh button handling, and state DB lifetime semantics remain covered by tests.

### Task 7: Update documentation

**Files:**
- Modify: `README.md` if needed
- Modify: `AGENTS.md` or `CLAUDE.md` if present and needed

- [ ] Inspect `README.md` for stale references to process structure or commands.
- [ ] Update README only if user-visible commands, behavior, or development workflow changed.
- [ ] Inspect internal guidance files if present for stale repository map or ownership descriptions.
- [ ] Update internal guidance only if it names `cmd/alib-fetcher/main.go` as owner of behavior now moved to `internal/process`.
- [ ] Run `make verify` after documentation-related code/test adjustments if any code changed after Task 6.
