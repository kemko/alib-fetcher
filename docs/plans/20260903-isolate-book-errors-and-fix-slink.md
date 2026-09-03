# Изоляция ошибок книг и восстановление загрузки в Slink

## Overview

Сделать обработку каждой книги изолированной: ошибка разбора, подготовки фотографий, загрузки в Slink, очистки временных файлов или рендеринга исключает только эту книгу из текущего дайджеста. Новая сбойная книга не записывается в bbolt, поэтому следующий запуск обнаружит её заново. Остальные книги отправляются. Если число сбойных книг больше нуля, последняя часть дайджеста содержит это число в дополнительной секции после `<hr/>`.

Refresh callback подтверждается сразу текстом `Формирование дайджеста запущено`; digest продолжает работу в фоне без общего 10-секундного лимита, сохраняя таймауты отдельных HTTP-запросов и отмену при остановке сервиса.

## Context

- Files involved:
  - `internal/alib/parser.go`
  - `internal/alib/parser_test.go`
  - `internal/alib/client.go`
  - `internal/alib/client_test.go`
  - `internal/slink/client.go`
  - `internal/slink/client_test.go`
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/digest/render_internal_test.go`
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/app/service_photos_test.go`
  - `internal/store/store.go`
  - `internal/store/store_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/process/callbacks.go`
  - `internal/process/callbacks_test.go`
  - `internal/process/runner.go`
  - `internal/process/runner_test.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - `internal/alib.Client` уже сохраняет частичный успех отдельных страниц.
  - `digest.RenderSendable` уже умеет пропускать слишком длинные книги.
  - `digestRunner` уже выполняет refresh в отдельной goroutine под общим lock.
- Established causes:
  - новые книги записываются в БД до подготовки Slink и проверки рендеринга;
  - `slink.Client` скрывает ошибки отдельных фото и оставляет книгу частично обработанной;
  - общий refresh deadline отменяет активный Slink POST и создаёт HTTP 499;
  - текущий `slink.photo_failed` не различает download, META parsing и upload, поэтому 403/502 невозможно локализовать;
  - локальные исходники Slink требуют API key формата `sk_...`, multipart-поле `image`, массив `tagIds[]`, возвращают `url` при успешном external upload и используют `Origin` в сгенерированной ShareX-конфигурации;
  - публичная доступность возвращённой ссылки требует включённого Slink external-upload auto-publish, а тег должен принадлежать владельцу API key.
- Dependencies:
  - новые Go-зависимости не нужны;
  - `../slink` используется как источник контракта и не изменяется.

## Development Approach

- **Testing approach**: TDD — сначала регрессионный тест каждого поведения, затем минимальная реализация.
- Завершать каждую задачу полностью перед следующей.
- После каждой задачи выполнять `make test`; следующую задачу начинать только после успеха.
- Ошибки контекста и общие ошибки Fetch, bbolt или Telegram остаются терминальными. Только ошибки, однозначно относящиеся к одной книге, изолируются.
- Каждая сбойная книга считается один раз, даже если у неё несколько фотографий или она встретилась на нескольких настроенных страницах.
- Каждая задача содержит новые или обновлённые тесты.

## Implementation Steps

### Task 1: Возвращать частичный результат разбора книг

**Files:**

- Modify: `internal/alib/parser.go`
- Modify: `internal/alib/parser_test.go`
- Modify: `internal/alib/client.go`
- Modify: `internal/alib/client_test.go`
- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`

- [x] Добавить результат fetch/parse с успешно разобранными книгами и числом ошибок конкретных объявлений.
- [x] Отличать посторонний `<p>` от похожего на объявление, которое не удалось разобрать; malformed-объявление пропускать без ошибки всей страницы.
- [x] Сохранить `ErrNoBooks` для пустой или структурно изменившейся страницы, не распознанной как корректная выдача.
- [x] Дедуплицировать ошибки по разрешённому `BuyURL`; если та же книга успешно разобрана на другой странице, не считать её сбойной.
- [x] Сохранить порядок, межстраничную дедупликацию и существующую политику частичного успеха страниц.
- [x] Добавить тесты смешанной страницы, страницы только со сбойными объявлениями, дублей ошибок и успешного дубля после ошибки.
- [x] Выполнить `make test`; исправить все ошибки до Task 2.

### Task 2: Привести Slink-клиент к реальному контракту и возвращать ошибки фото

**Files:**

- Modify: `internal/slink/client.go`
- Modify: `internal/slink/client_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [x] Добавить валидацию `SLINK_API_KEY` по обязательному префиксу `sk_`, сохранив имя переменной в ошибке и исключив значение ключа из логов.
- [x] Реализовать точный контракт Slink: `POST /api/external/upload`, Bearer auth, multipart `image`, `tagIds[]`, `Origin` из настроенного Slink base URL и разбор поля `url`.
- [x] Для загрузки исходных фотографий передавать стабильный User-Agent и безопасный Referer страницы книги.
- [x] Возвращать первую ошибку download, HTTP/META redirect, распознавания изображения или Slink upload как ошибку всей книги; корректный non-image сохранять исходной ссылкой.
- [x] Сохранить терминальность отмены контекста и гарантированную очистку временного каталога.
- [x] Сделать `slink.photo_failed` диагностичным без утечки URL и ключа: добавить этап, HTTP status и безопасную категорию ошибки, различающие source 403/502 и Slink 403/502.
- [x] Добавить тесты заголовков, API-key validation, multipart-контракта, ошибок каждого этапа, безопасных логов и очистки после частичного выполнения.
- [x] Выполнить `make test`; исправить все ошибки до Task 3.

### Task 3: Добавить число сбойных книг в последнюю часть дайджеста

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`
- Modify: `internal/digest/render_internal_test.go`

- [x] Передавать renderer число ошибок предыдущих этапов и прибавлять книги, пропущенные из-за `ErrMessageTooLong`.
- [x] Если итоговое число больше нуля, добавлять в конец последней части дайджеста секцию `Не удалось обработать книг: N`, отделённую от секций книг через `<hr/>`.
- [x] Не добавлять секцию ошибок при нулевом числе сбоев.
- [x] При отсутствии успешно обработанных книг формировать digest из заголовка и секции ошибок.
- [x] Учитывать секцию и разделитель в text/block limits; при необходимости формировать дополнительную последнюю часть с сохранением секции.
- [x] Сохранить media limits, атомарность книг, порядок chunks и список `BuyURL` книг, пропущенных renderer.
- [x] Добавить тесты одного и нескольких сбоев, нулевого числа сбоев, summary-only digest, split на границе лимита и oversized-книги.
- [x] Выполнить `make test`; исправить все ошибки до Task 4.

### Task 4: Полностью обрабатывать новые книги до записи в bbolt

**Files:**

- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Modify: `internal/app/service_photos_test.go`
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [x] Добавить read-only проверку текущих `BuyURL`, чтобы выделять новые книги без предварительной записи в БД и сохранять source order.
- [x] Последовательно подготовить фотографии каждой новой книги и очистить её временный каталог до перехода к следующей.
- [x] При book-specific ошибке накопить один сбой и продолжить с остальными книгами; отмену контекста и общую ошибку состояния завершать глобальной ошибкой.
- [x] До записи выполнить индивидуальную проверку рендеринга и исключить слишком длинные книги.
- [x] Передавать в `RecordDiscovered` только полностью подготовленные и renderable книги.
- [x] При обработке набора для отправки исключать книгу с локальной ошибкой только из текущего дайджеста и не прерывать остальные операции.
- [x] Отправлять подготовленные chunks и подтверждать только книги соответствующего успешно отправленного chunk.
- [x] Добавить `Failed` в `app.Result` и поле `failed` в `digest.completed`; book-specific ошибки не должны создавать `digest.failed`.
- [x] Добавить тесты смешанного успеха, отсутствия новой сбойной книги в БД, её повторного обнаружения на следующем запуске, корректного `New/Failed/Sent`, Telegram failure и отмены контекста.
- [x] Выполнить `make test`; исправить все ошибки до Task 5.

### Task 5: Убрать общий timeout refresh-digest

**Files:**

- Modify: `internal/process/callbacks.go`
- Modify: `internal/process/callbacks_test.go`
- Modify: `internal/process/runner.go`
- Modify: `internal/process/runner_test.go`

- [ ] Удалить `refreshCallbackTimeout` и создание 10-секундного контекста.
- [ ] После успешного захвата runner lock сразу отвечать callback текстом `Формирование дайджеста запущено`.
- [ ] Продолжать digest в фоне на контексте жизненного цикла сервиса; сохранить отмену при shutdown и ожидание refresh goroutine.
- [ ] Не отправлять второй callback-answer после завершения digest; итог и ошибки оставлять в `digest.completed`/`digest.failed`.
- [ ] Сохранить ответы для чужого чата и уже выполняющегося digest, polling во время фоновой работы и удаление старой кнопки только перед первой новой отправкой.
- [ ] Добавить тесты немедленного ответа, работы дольше прежних 10 секунд, ошибки фонового digest, duplicate callback и shutdown cancellation.
- [ ] Выполнить `make test`; исправить все ошибки до Task 6.

### Task 6: Обновить контракт и проверить acceptance criteria

**Files:**

- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `README.md`
- Modify: `AGENTS.md`

- [ ] Добавить end-to-end regression test: одна новая книга успешно проходит Slink и Telegram, другая получает 403/502 на source или upload, не записывается в БД и учитывается в секции ошибок последней части дайджеста.
- [ ] Обновить runtime flow, invariants, формат digest, `Result`/logging и семантику немедленного refresh-answer.
- [ ] Описать требования Slink: ключ `sk_...`, тег того же владельца, включённый external-upload auto-publish и использование `HTTP_TIMEOUT` только для отдельных запросов.
- [ ] Выполнить `make verify`, включая format check, linter, race-enabled tests и build.
- [ ] Выполнить `make coverage` и подтвердить итоговое покрытие не ниже 80%.
- [ ] Проверить отсутствие ключей, полных error bodies и иных секретов в тестовых и рабочих логах.

## Post-Completion

- Рабочие HTTP-запросы Slink остаются ограничены `HTTP_TIMEOUT`; общий refresh-digest больше не имеет отдельного deadline.
- HTTP 403 после валидации означает несовместимый API key/tag либо отказ source-host; новые stage/status-поля покажут точный контур.
- HTTP 502 считается ошибкой конкретной книги. Автоматический retry POST не добавляется, поскольку Slink external upload не предоставляет idempotency contract.
