# Удаление Slink-обработки с сохранением Rich Message

## Overview

Удалить скачивание, проверку, загрузку и публикацию изображений через Slink. Все
фотоссылки из объявления всегда выводить только в блоке `Смотрите` с исходными
адресами, подписями, порядком и повторами. Сохранить Telegram `SendRichMessage`, лимит
отображаемого текста `MESSAGE_LIMIT` с default `32000`, HTML-экранирование, сокращение длинного
описания с `…`, разбиение сообщений и текущую семантику доставки.

## Context

- Files involved:
  - `internal/alib/book.go`
  - `internal/alib/book_test.go`
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/app/service_photos_test.go` (delete)
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/store/store.go`
  - `internal/store/store_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/slink/client.go` (delete)
  - `internal/slink/client_test.go` (delete)
  - `cmd/alib-fetcher/main.go`
  - `cmd/alib-fetcher/main_test.go`
  - `go.mod`
  - `go.sum`
  - `docker-compose.yml`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - `internal/alib` уже извлекает все ссылки `foto.php4`, разрешает относительные URL и сохраняет
    исходный порядок, повторы и подписи.
  - `internal/digest` уже формирует блок `Смотрите`, считает отображаемые Unicode-руны после
    разбора HTML и сокращает только `Content` до максимально допустимого префикса с `…`.
  - Длинный `Content` не является ошибкой книги: сокращённая книга должна записываться и
    отправляться. `digest.ErrMessageTooLong` остаётся только для случая, когда обязательные поля не
    помещаются даже без описания.
  - `internal/telegram` уже отправляет `rich_message.html` через SDK `SendRichMessage`; transport не меняется.
  - Старые bbolt-записи могут содержать `slink_url`, `slink_profile` и `non_image`; после удаления полей
    они должны читаться по исходным `url` и `caption`, а следующая запись очистит устаревшие
    поля.
- Dependencies:
  - Новые зависимости не требуются.
  - Удалить `github.com/c-robinson/iplib/v2` и ставшие неиспользуемыми транзитивные зависимости.
  - `golang.org/x/net/html` остаётся нужен для DOM-разбора и подсчёта отображаемых рун.

## Development Approach

- **Testing approach**: Regular — сначала минимальное изменение кода, затем регрессионные тесты
  задачи.
- Полностью завершать каждую задачу и выполнять `make test` до перехода к следующей.
- Не менять парсинг фотоссылок: сохранять абсолютные исходные URL, подписи, порядок и
  повторы.
- Сохранить `MESSAGE_LIMIT` с default `32000`, диапазоном `64..32768`, сокращением только `Content` и
  лимитом Rich Message в 500 блоков.
- Сохранить порядок pending-книг, chunk acknowledgement, refresh-кнопку, flood-control retry и звук только
  последнего chunk.
- Каждая задача с изменением кода включает новые или обновлённые тесты.
- Все тесты должны проходить перед началом следующей задачи.

## Implementation Steps

### Task 1: Переключить digest на единственный формат исходных ссылок

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`
- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Delete: `internal/app/service_photos_test.go`
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] Удалить `SlinkProfile` из renderer options, генерацию `<tg-slideshow>`, `<img>` и `<figcaption>`, а также media-limit
  расчёты.
- [x] Всегда выводить каждую `Book.Photos` как исходную ссылку в единственном блоке
  `Смотрите`, сохраняя подпись, порядок и повторы; пустую подпись заменять на `фото`.
- [x] Упростить подсчёт Rich Message blocks до обычных listing-блоков и `<hr/>`, сохранив границу 500
  блоков и максимум 250 объявлений в chunk.
- [x] Сохранить расчёт `MESSAGE_LIMIT` по отображаемым Unicode-рунам, default `32000`, failure summary и
  разбиение только между книгами.
- [x] Сохранить существующее сокращение длинного `Content`: выбрать максимально
  допустимый префикс и завершить его `…`, чтобы книга поместилась в сообщение.
- [x] Не считать длинный `Content` ошибкой подготовки: новая книга должна записываться,
  отправляться в сокращённом виде и не увеличивать счётчик сбоев.
- [x] Оставить `digest.ErrMessageTooLong` только для книги, обязательные отображаемые поля
  которой превышают лимит даже при полностью удалённом `Content`.
- [x] Удалить `PhotoProcessor`, подготовку фотографий, временную очистку и `SavePrepared` из
  app-интерфейса и digest lifecycle.
- [x] Удалить создание Slink-клиента из bootstrap; `run` должен напрямую собирать сервис без
  фабрики обработки фотографий.
- [x] Обновить renderer/service tests: все исходные ссылки присутствуют в `Смотрите`, slideshow
  отсутствует, длинное описание заканчивается `…`, книга сохраняется и отправляется,
  итоговый chunk не превышает лимит и сбой не учитывается.
- [x] Обновить bootstrap tests, подтверждающие вызов `/sendRichMessage`, передачу HTML через `rich_message.html`
  и отсутствие `<tg-slideshow>`.
- [x] Выполнить `make test`; все тесты должны пройти до Task 2.

### Task 2: Удалить Slink-код, состояние и конфигурацию

**Files:**

- Modify: `internal/alib/book.go`
- Modify: `internal/alib/book_test.go`
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Delete: `internal/slink/client.go`
- Delete: `internal/slink/client_test.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `docker-compose.yml`

- [x] Сократить `alib.Photo` до исходных `URL` и `Caption`; удалить `SlinkURL`, `SlinkProfile`, `NonImage` и перенос
  результатов обработки при rediscovery.
- [x] Сохранить чтение legacy `photo_urls` и добавить регрессию чтения текущих bbolt-записей с
  устаревшими Slink-полями: исходные URL и подписи не теряются, неизвестные поля не мешают
  открытию БД.
- [x] Удалить `Store.SavePrepared`; при rediscovery записывать свежие исходные фотоссылки и подписи,
  сохраняя sent status, timestamps и queue order.
- [x] Полностью удалить package `internal/slink` и относящиеся к нему test doubles и fixtures.
- [x] Удалить `SLINK_URL`, `SLINK_API_KEY`, `SLINK_TAG_ID` и их валидацию из конфигурации.
- [x] Удалить Slink-переменные и ставший ненужным `/tmp` tmpfs из Compose.
- [x] Удалить `iplib` и неиспользуемые транзитивные записи из `go.mod` и `go.sum`.
- [x] Обновить model/store/config tests для новой схемы и удаления Slink-контракта.
- [x] Выполнить `make test`; все тесты должны пройти до Task 3.

### Task 3: Закрепить публичный контракт и документацию

**Files:**

- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `README.md`
- Modify: `AGENTS.md`

- [x] Добавить или обновить end-to-end regression test: Alib-объявление с несколькими фотоссылками
  отправляется через `/sendRichMessage`, все исходные ссылки и подписи находятся в `Смотрите`,
  а slideshow/media отсутствуют.
- [x] Добавить end-to-end случай с описанием длиннее лимита: описание сокращается с `…`,
  книга записывается и отправляется, `failed` не увеличивается, отображаемый текст не
  превышает лимит.
- [x] Зафиксировать тестом default `MESSAGE_LIMIT=32000` и разбиение Rich Message без перехода на
  обычный Telegram `sendMessage`.
- [x] Удалить из README конфигурацию, сетевой поток, временные файлы, persistence и
  эксплуатационные требования Slink.
- [x] Описать единственный фотоформат: блок `Смотрите` со всеми исходными ссылками,
  подписями, порядком и повторами.
- [x] Описать сокращение только длинного описания с `…` при сохранении и отправке
  книги.
- [x] Обновить AGENTS.md: runtime flow, invariants, модель состояния, digest limits, repository map, configuration contract,
  logging и deployment notes без Slink.
- [x] Выполнить `make test`; все тесты должны пройти до Task 4.

### Task 4: Verify acceptance criteria

- [x] Выполнить `make verify`: format check, строгий lint, race-enabled shuffled tests и build должны пройти.
- [x] Выполнить `make coverage`; общее statement coverage должно быть не ниже 80%.
- [x] Выполнить `docker compose config`.
- [x] Проверить поиском, что production-код, конфигурация и Compose больше не содержат Slink,
  `tg-slideshow`, image upload/download и `iplib`.
- [x] Проверить автоматическими тестами, что все фотоссылки остаются в `Смотрите`,
  сообщения отправляются через `SendRichMessage`, а отображаемый текст каждого chunk не
  превышает настроенный лимит с default `32000`.
- [x] Проверить автоматическими тестами, что длинный `Content` сокращается с `…`, а книга
  записывается, отправляется и не учитывается как сбой.
- [x] Проверить task-only diff и отсутствие секретов, временных файлов, баз данных и
  бинарников.
- [x] Создать сфокусированный Conventional Commit, например `feat: verify Slink removal`.
