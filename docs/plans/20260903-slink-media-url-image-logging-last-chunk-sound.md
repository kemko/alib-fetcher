# Исправление Slink media URL, логирования изображений и звука дайджеста

## Overview

Исправить три связанных поведения:

- Только последний Telegram chunk отправляется со звуком.
- В `tg-slideshow` используется прямой URL изображения, а не crawler-страница Slink.
- Успешные и ошибочные этапы обработки фотографий видны в структурированных логах.

## Context

- Files involved:
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/slink/client.go`
  - `internal/slink/client_test.go`
  - `internal/app/service_photos_test.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
  - `AGENTS.md`
- Current sound logic: `silent := index > 0`, поэтому звучит первый chunk.
- Current Slink logic сохраняет поле `url` из `POST /api/external/upload` непосредственно в `Photo.SlinkURL`.
- Практическое воспроизведение:
  - Upload тестовой PNG вернул `201` и URL вида `i/…`.
  - Этот URL для crawler User-Agent возвращает `200 text/html`.
  - Для `alib-fetcher/1.0` HEAD возвращает `302` на `/image/{id}.png`.
  - Прямой `Location` возвращает `200 image/png` любому проверенному User-Agent.
  - `/api/image/public/{id}.png` возвращает `404`: изображение приватное, доступ к прямому `/image/…` обеспечивает опубликованный share.
  - `RICH_MESSAGE_PHOTO_NO_MEDIA_FOUND` возникает потому, что Telegram получает HTML вместо media.
- Existing logs contain only `slink.photo_failed`; successful collection and upload are invisible.
- Dependencies: новые зависимости не нужны; достаточно `net/http` и существующего `slog`.

## Development Approach

- **Testing approach**: TDD.
- Завершать каждую задачу и выполнять `make test` до перехода к следующей.
- Сохранить последовательную обработку фотографий, изоляцию ошибок книги, временные каталоги и существующий Slink profile.
- Не записывать API key, исходные photo URL, response body или временные пути в логи.
- **CRITICAL: every task MUST include new/updated tests**.
- **CRITICAL: all tests must pass before starting next task**.

## Implementation Steps

### Task 1: Перенести звук на последний Telegram chunk

**Files:**

- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] Обновить app-level тесты для одного, двух и трёх chunks: ожидаемые silent-флаги `false, true` заменить на `true, false`; для трёх chunks ожидать `true, true, false`.
- [x] Проверить retry: повтор одного chunk сохраняет его silent-флаг, а звук получает только финальный chunk текущей отправки.
- [x] Сохранить `attachRefresh` только на последнем chunk и acknowledgement после успешной отправки.
- [x] Изменить вычисление `silent` на `index < len(chunks)-1`.
- [x] Обновить end-to-end проверку `disable_notification` в `main_test.go`.
- [x] Выполнить `make test`; исправить все ошибки до Task 2.

### Task 2: Преобразовывать Slink share URL в проверенный прямой media URL

**Files:**

- Modify: `internal/slink/client.go`
- Modify: `internal/slink/client_test.go`

- [x] Добавить тест, где upload возвращает `i/{code}`, HEAD с User-Agent `alib-fetcher/1.0` перенаправляется на `/image/{id}.png`, а итоговый `Photo.SlinkURL` содержит прямой URL.
- [x] После upload выполнять HEAD по возвращённому URL через копию Slink HTTP client, соблюдая `HTTP_TIMEOUT`.
- [x] Разрешать redirects только внутри настроенного Slink origin, валидировать HTTP(S), отсутствие userinfo, конечный 2xx status и `Content-Type: image/*`.
- [x] Сохранять конечный `response.Request.URL` как `Photo.SlinkURL`; короткий `i/…` URL не должен попадать в `tg-slideshow`.
- [x] Аналогично проверять сохранённые результаты активного profile. Старый `i/…` URL должен преобразоваться без повторного скачивания исходника и повторного upload; прямой URL проверяется на доступность.
- [x] Сохранить cache повторяющихся source URL внутри книги, чтобы download, upload и media resolution выполнялись один раз.
- [x] Классифицировать ошибки проверки как book-specific stage `slink_media`, чтобы недоступное изображение оставалось pending и не приводило к `digest.failed` от Telegram.
- [x] Добавить тесты direct URL, short redirect, HTML вместо изображения, 4xx/5xx, отсутствующего или off-origin `Location` и отмены context.
- [x] Выполнить `make test`; исправить все ошибки до Task 3.

### Task 3: Добавить структурированные lifecycle-логи фотографий

**Files:**

- Modify: `internal/slink/client.go`
- Modify: `internal/slink/client_test.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] Добавить INFO event `slink.photo_started` с `buy_url`, `index` и `total`.
- [x] Добавить INFO event `slink.photo_completed` с `buy_url`, `index`, `total`, `outcome` и, для изображения, конечным `media_url`.
- [x] Различать outcomes `uploaded`, `reused`, `duplicate` и `source_link`.
- [x] Дополнить `slink.photo_failed` полем `buy_url` и новым stage `slink_media`; сохранить `stage`, `error_category` и необязательный `http_status`.
- [x] Добавить тесты событий для upload, persisted reuse, duplicate, non-image и media-resolution failure.
- [x] Проверить отсутствие API key, source photo URL, приватного response body и временного пути во всех логах.
- [x] Выполнить `make test`; исправить все ошибки до Task 4.

### Task 4: Закрепить полный сценарий и обновить документацию

**Files:**

- Modify: `internal/app/service_photos_test.go`
- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `README.md`
- Modify: `AGENTS.md`

- [ ] Обновить fake Slink: upload возвращает short URL, HEAD раскрывает прямой media `Location`, прямой URL отвечает `image/*`.
- [ ] Проверить, что state сохраняет прямой URL и повторный цикл не выполняет новый upload.
- [ ] Добавить regression для pending записи со старым `i/…` URL: URL обновляется перед rendering и Telegram получает прямой `src`.
- [ ] Проверить смешанный сценарий: недоступное media исключает только свою книгу, остальные книги и failure summary отправляются.
- [ ] В main end-to-end тесте проверить прямой URL внутри `tg-slideshow`, lifecycle-логи и отсутствие short URL/API key.
- [ ] Обновить `README.md`: только последний chunk звучит; Slink share URL разрешается и проверяется перед Telegram.
- [ ] Обновить `AGENTS.md`: исправить sound invariant и перечислить новые стабильные Slink events.
- [ ] Выполнить `make test`; исправить все ошибки до Task 5.

### Task 5: Verify acceptance criteria

**Files:**

- Modify: none

- [ ] Выполнить `make verify`.
- [ ] Выполнить `make coverage` и подтвердить общий statement coverage не ниже 80%.
- [ ] Проверить чистый task-related diff и отсутствие временного токена, тестовых файлов и credentials.
- [ ] Подготовить отдельные focused commits только после полного успеха проверок.

## Post-Completion

- Временный Slink API key следует отозвать.
- Созданное при исследовании тестовое изображение следует удалить через Slink UI.
