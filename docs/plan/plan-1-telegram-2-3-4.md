# Persistent pending Telegram queue

## Overview

Текущий алгоритм не соответствует шагам 1-3.
Сейчас bbolt хранит не книгу и не полный набор метаданных, а только marker отправки: key = BuyURL, value = RFC3339Nano timestamp. Найденные книги после fetch не записываются в БД до
отправки, а очередь отправки строится из массива текущего парсинга страницы. Шаг 4 уже соответствует требованию: книги помечаются отправленными
только после успешной отправки Telegram-чанка.
План переводит состояние на простую модель "запись книги в БД + sent bool". Каждая запись должна хранить полный `alib.Book`, полученный из страницы: `Title`,
`TextBeforeSeller`, `Seller`, `SellerURL`, `TextBeforeBuy`, `BuyURL`, `TextAfterBuy`, `HasPhotos`, плюс служебные поля `Sent` и `SentAt` для Telegram/retention. После fetch сервис сохраняет все обнаруженные книги в
БД, сохраняя `Sent=true` у уже отправленных записей. Затем отправка берет только pending-записи из БД, включая книги из прошлых неудачных циклов.
Legacy-маркеры мигрируются в sent-записи без повторной рассылки. Полные метаданные из старых marker-only записей восстановить нельзя, поэтому migration создает
минимальную sent-запись с `Book.BuyURL`; если такая книга снова встретится на странице, `RecordDiscovered` обновит `Book` полным свежим payload, не сбрасывая `Sent=true`.

## Context

- Files involved:
- internal/app/service.go
- internal/app/service_test.go
- internal/store/store.go
- internal/store/store_test.go
- internal/alib/book.go
- internal/digest/render.go
- internal/digest/render_test.go
- cmd/alib-fetcher/main.go
- README.md
- Related patterns:
- `app.Service` уже держит policy-слой и работает через маленький `State` interface.
- `digest.Render` уже принимает `[]alib.Book` и возвращает `Chunk.Books`, которые надо acknowledge после успешной отправки.
- `store.Open` уже выполняет bbolt initialization и legacy migration.
- Retention уже работает по strict boundary: удалять только записи раньше cutoff.
- `make verify` является канонической полной проверкой.
- Dependencies:
- Новые внешние зависимости не нужны.
- Для записи книги использовать стандартный `encoding/json` поверх существующего bbolt bucket.

## Development Approach

- Testing approach: TDD для `store` и `app`, затем минимальная реализация.
- Complete each task fully before moving to the next.
- Держать архитектуру прежней: `app` оркестрирует, `store` отвечает за persisted queue, `digest` не знает про БД.
- Не добавлять отдельную queue abstraction: достаточно расширить текущий `State` contract.
- CRITICAL: every task that modifies code MUST include new/updated tests.
- CRITICAL: all tests must pass before starting next task.

## Implementation Steps

### Task 1: Заменить State contract на persisted queue contract

Files:

- Modify: internal/app/service.go
- Modify: internal/app/service_test.go
- [x] Изменить `app.State`: заменить `Unseen(context.Context, []alib.Book)` на `RecordDiscovered(context.Context, []alib.Book, time.Time) (int, error)` и `Pending(context.Context) ([]alib.Book, error)`.
- [x] Обновить `Service.Run` sequence: `Prune`, `Fetch`, `RecordDiscovered`, `Pending`, `Render pending`, `Send chunks`, `MarkSent` after each accepted chunk.
- [x] Сохранить `Result.Fetched` как число книг из fetch.
- [x] Сохранить `Result.New` как число впервые созданных DB records, а не размер pending queue.
- [x] Сохранить `Result.Sent` как число книг, успешно отмеченных после отправки.
- [x] Обновить fakeState в service tests под новый interface.
- [x] Добавить/обновить тест: после fetch сервис вызывает `RecordDiscovered` до `Pending` и отправки.
- [x] Добавить/обновить тест: сервис отправляет книги из `Pending`, включая книгу, отсутствующую в текущем fetch result.
- [x] Сохранить тесты на per-chunk acknowledgement, `retry_after`, context cancellation, silent/non-silent chunks.
- [x] Run `go test -race -shuffle=on -count=1 ./internal/app` before task 2.

### Task 2: Перевести bbolt storage на JSON records с полным alib.Book

Files:

- Modify: internal/store/store.go
- Modify: internal/store/store_test.go
- [x] Ввести internal JSON record в `store`: `Book alib.Book`, `Sent bool`, `FirstSeenAt`/`LastSeenAt` или один `ObservedAt`, `SentAt` для retention.
- [x] Убедиться, что record хранит все поля `alib.Book`: `Title`, `TextBeforeSeller`, `Seller`, `SellerURL`, `TextBeforeBuy`, `BuyURL`, `TextAfterBuy`, `HasPhotos`.
- [x] Сохранить существующий bucket name `sent_books` для совместимости с уже созданными БД, но переименовать Go-level смысл в books/state records, если это упрощает чтение.
- [x] Реализовать `RecordDiscovered`: для нового `BuyURL` создать record с полным `Book` payload и `Sent=false`.
- [x] Реализовать `RecordDiscovered`: для существующей `Sent=false` записи обновить `Book` payload полными актуальными данными и оставить pending.
- [x] Реализовать `RecordDiscovered`: для существующей `Sent=true` записи обновить `Book` payload полными актуальными данными, но не сбрасывать `Sent` и `SentAt`.
- [x] Реализовать `Pending`: читать из БД все records с `Sent=false` и возвращать сохраненный `alib.Book`.
- [x] Обновить `MarkSent`: выставлять `Sent=true` и `SentAt=sentAt` для `BuyURL` из книг успешно отправленного чанка, не теряя сохраненный `Book`.
- [x] Добавить тест: `RecordDiscovered` сохраняет новые книги как pending с полным набором полей `alib.Book`.
- [x] Добавить тест: повторно обнаруженная already-sent книга обновляет book metadata, но не становится pending.
- [x] Добавить тест: pending книга из прошлой неудачной отправки возвращается `Pending` без участия текущего массива fetch.
- [x] Добавить тест: `MarkSent` убирает книги из `Pending` и сохраняет book metadata.
- [x] Run `go test -race -shuffle=on -count=1 ./internal/store` before task 3.

### Task 3: Обновить migration и retention под новую схему

Files:

- Modify: internal/store/store.go
- Modify: internal/store/store_test.go
- [ ] Обновить `migrateLegacyMarkers`: если value уже валидный JSON record новой схемы, оставить как есть.
- [ ] Обновить `migrateLegacyMarkers`: если value является RFC3339Nano timestamp, заменить на JSON record `{Book: {BuyURL: key}, Sent: true, SentAt: parsed timestamp}`.
- [ ] Обновить `migrateLegacyMarkers`: если value является unknown legacy marker, заменить на JSON record `{Book: {BuyURL: key}, Sent: true, SentAt: migratedAt}`.
- [ ] Явно зафиксировать в тестах, что legacy marker-only записи не имеют восстановимых title/seller/text/photo fields, но не попадают в pending, потому что `Sent=true`.
- [ ] Добавить тест: если legacy-sent `BuyURL` снова найден на странице, `RecordDiscovered` дополняет record полным `alib.Book`, сохраняя `Sent=true`.
- [ ] Обновить `Prune`: удалять только `Sent=true` records с `SentAt` strictly before cutoff.
- [ ] Не удалять `Sent=false` records при retention, даже если они старые.
- [ ] Сохранить strict boundary behavior: `SentAt` ровно на cutoff остается.
- [ ] Добавить тест миграции RFC3339Nano marker в sent record без повторной отправки.
- [ ] Добавить тест миграции unknown legacy marker без immediate pruning.
- [ ] Добавить тест: `Prune` не удаляет unsent запись старше cutoff.
- [ ] Run `go test -race -shuffle=on -count=1 ./internal/store` before task 4.

### Task 4: Проверить digest boundary behavior с DB-backed queue

Files:

- Modify: internal/digest/render_test.go if coverage gaps appear
- Modify: internal/app/service_test.go if chunk expectations need adjustment
- [ ] Убедиться, что `digest.Render` не требует production changes: он уже работает с `[]alib.Book`, теперь эти книги приходят из DB `Pending`.
- [ ] Если app tests перестали явно покрывать chunk-level `MarkSent`, добавить regression test: при send error на втором чанке первый чанк уже `MarkSent`, второй остается pending/not marked.
- [ ] Если порядок `Pending` из store стал deterministic by key, не добавлять отдельную order abstraction без требования; app tests должны задавать порядок через fakeState.
- [ ] Run `go test -race -shuffle=on -count=1 ./internal/digest ./internal/app` before task 5.

### Task 5: Обновить wiring и документацию поведения

Files:

- Modify: cmd/alib-fetcher/main.go if Result fields or State contract require call-site changes
- Modify: README.md
- [ ] Обновить main только если компиляция требует изменений вокруг `Result` logging; сохранить stable log fields `fetched`, `new`, `pruned`, `sent`.
- [ ] Обновить README: описать, что найденные книги сначала сохраняются в state DB как pending records с полным parsed book payload.
- [ ] Обновить README: отправка берет все pending records из DB, а не только текущий fetch result.
- [ ] Уточнить README: неотправленные книги остаются в очереди между циклами; sent records удаляются только retention pruning.
- [ ] Проверить, что описание первого успешного запуска остается верным: все книги текущей страницы попадут в pending и будут отправлены.
- [ ] Run `go test -race -shuffle=on -count=1 ./cmd/alib-fetcher ./internal/app ./internal/store` before task 6.

### Task 6: Verify acceptance criteria

Files:

- Modify: none
- [ ] Run `make verify`.
- [ ] Run `go test -coverprofile=coverage.out ./...`.
- [ ] Run `go tool cover -func=coverage.out`.
- [ ] Verify total test coverage is at least 80%.
- [ ] Inspect `git diff` to ensure only task-related files changed.
- [ ] Confirm DB record contains full `alib.Book` payload plus `Sent bool`.
- [ ] Confirm fetch records discovered books before send queue build.
- [ ] Confirm send queue comes from DB pending records.
- [ ] Confirm `MarkSent` happens only after accepted Telegram chunk.
