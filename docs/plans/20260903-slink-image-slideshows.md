# Поддержка Slink и Telegram-слайдшоу для фотографий

## Overview

Добавить опциональную обработку фотоссылок объявлений: безопасно скачивать файлы
во временное хранилище, переходить по HTTP- и HTML META refresh-редиректам, отделять
изображения от остальных файлов, загружать изображения в Slink с тегом `alib` и выводить
опубликованные ссылки в `<tg-slideshow>`. Ошибки отдельных файлов не блокируют отправку
книги. Временные файлы книги удаляются после сохранения результатов её обработки в
bbolt.

## Context

- Files involved:
  - `internal/alib/book.go`
  - `internal/alib/book_test.go`
  - `internal/alib/parser.go`
  - `internal/alib/parser_test.go`
  - `internal/store/store.go`
  - `internal/store/store_test.go`
  - `internal/slink/client.go` (new)
  - `internal/slink/client_test.go` (new)
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `cmd/alib-fetcher/main.go`
  - `cmd/alib-fetcher/main_test.go`
  - `docker-compose.yml`
  - `README.md`
- Related patterns:
  - `internal/app` сохраняет orchestration и policy, сетевой протокол Slink размещается в отдельном adapter
    package.
  - Результаты успешной обработки сохраняются в bbolt до отправки Telegram, чтобы повторный
    digest не загружал уже обработанные изображения.
  - Временные файлы одной книги живут до завершения записи подготовленной книги в
    bbolt, затем удаляются до render/send.
  - Порядок исходных фотоссылок и повторы сохраняются.
  - При отключённом Slink форматирование остаётся прежним.
  - Ограничения `MESSAGE_LIMIT` и 500 Rich Message blocks продолжают рассчитываться по фактически
    сформированному HTML.
- Dependencies:
  - Новые Go-зависимости не требуются: использовать `net/http`, стандартные MIME-функции и
    уже подключённый `golang.org/x/net/html`.
  - Slink: `POST /api/external/upload`, Bearer API key, multipart-поле `image`, массив `tagIds`, ответ с полем `url`.
  - `SLINK_TAG_ID` указывает на заранее созданный тег `alib`; для пользователя API key включён
    `Auto-publish API uploads`.
  - Относительный `url` из ответа Slink разрешается относительно настроенного адреса
    сервера.

## Development Approach

- **Testing approach**: Regular — код, затем регрессионные тесты задачи.
- Каждая задача завершается полностью до перехода к следующей.
- Обработка книг и фотоссылок последовательная, с сохранением исходного порядка.
- Сбой отдельной ссылки логируется и оставляет исходную ссылку в секции `Смотрите`;
  отмена контекста прекращает digest.
- Файлы группируются во временном каталоге книги и не удаляются между успешной
  загрузкой в Slink и записью Slink URL в bbolt.
- После успешной записи подготовленной книги каталог удаляется до render/send; при
  ошибке или отмене выполняется аварийная очистка без отправки Telegram.
- Каждая задача включает новые или обновлённые тесты.
- `make test` должен проходить перед началом следующей задачи.

## Implementation Steps

### Task 1: Расширить модель фотоссылок и состояние bbolt

**Files:**

- Modify: `internal/alib/book.go`
- Modify: `internal/alib/book_test.go`
- Modify: `internal/alib/parser.go`
- Modify: `internal/alib/parser_test.go`
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] Заменить голые URL семантической структурой фотоссылки: исходный URL,
  нормализованная подпись `<a>`, Slink URL, профиль Slink и признак подтверждённого
  не-изображения.
- [ ] Сохранять подписи, порядок и повторы фотоссылок при DOM-разборе; для пустой
  подписи использовать `фото`.
- [ ] Поддержать чтение старого JSON-поля `photo_urls`, преобразуя его в новые структуры с
  подписью `фото`; запись выполнять только в новой схеме.
- [ ] При повторном обнаружении книги переносить результаты обработки для
  совпадающих исходных ссылок, сохраняя свежие подписи и текущие delivery metadata.
- [ ] Добавить транзакционное обновление подготовленной pending-книги без изменения
  `ObservedAt`, `QueueOrder`, `Sent` и `SentAt`.
- [ ] Обновить тесты parser/model/store: подписи, повторы, legacy JSON, rediscovery, сохранение
  метаданных и отказ при повреждённой записи.
- [ ] Запустить `make test`; все тесты должны пройти до Task 2.

### Task 2: Реализовать Slink adapter и обработку файлов

**Files:**

- Create: `internal/slink/client.go`
- Create: `internal/slink/client_test.go`

- [ ] Реализовать клиент с HTTP(S) base URL, API key, tag ID, timeout, logger и стабильным идентификатором
  профиля, не содержащим API key.
- [ ] Последовательно скачивать необработанные фотоссылки через обычные
  HTTP-редиректы и ограниченную цепочку HTML META refresh, учитывая регистр атрибутов и
  относительные URL.
- [ ] Разрешать только HTTP(S) и защищать загрузчик от SSRF: отклонять userinfo, loopback, private,
  link-local, multicast и unspecified адреса на каждом переходе.
- [ ] Ограничить каждый скачиваемый файл 15 MiB и сохранять файлы одной книги в
  отдельном каталоге, созданном через `os.MkdirTemp` в системном temp directory.
- [ ] Определять тип по содержимому, а не по расширению или одному серверному
  заголовку; подтверждённые не-изображения помечать для секции `Смотрите`.
- [ ] Загружать изображения в `POST /api/external/upload` как multipart `image` с `tagIds` для тега `alib`, Bearer API
  key и подходящим именем файла/Content-Type.
- [ ] Ограничить JSON-ответ Slink 1 MiB, проверить статус и `url`, разрешить относительную
  ссылку относительно Slink base URL и принять только HTTP(S).
- [ ] Повторно использовать результат для одинакового исходного URL внутри книги;
  успешные результаты сохранять в исходном порядке.
- [ ] Возвращать подготовленную книгу вместе с идемпотентной операцией очистки её
  временного каталога, не удаляя файлы успешных Slink uploads до решения вызывающего кода о
  сохранении в bbolt.
- [ ] Реализовать best effort: download/redirect/type/upload/response error логируется без секретов, исходная
  ссылка остаётся необработанной; context cancellation возвращается вызывающему коду.
- [ ] Покрыть тестами image/non-image, HTTP redirect, META refresh, циклы и повреждённый META, лимиты, SSRF,
  multipart/tag/auth, ответы Slink, относительные URL, порядок, повторы, ошибки, отмену и
  идемпотентную очистку каталога книги.
- [ ] Запустить `make test`; все тесты должны пройти до Task 3.

### Task 3: Добавить `<tg-slideshow>` в renderer

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`

- [ ] При активном профиле Slink оставить в `Смотрите` только не-изображения, неудачные
  загрузки и результаты другого профиля; при отключённом Slink выводить все исходные
  ссылки как раньше.
- [ ] Использовать исходные подписи ссылок вместо фиксированного текста `фото`.
- [ ] После details и перед финальной секцией `Купить` вставлять отделённый `<tg-slideshow>` со
  всеми успешно опубликованными изображениями в исходном порядке.
- [ ] Рендерить изображения как `<img src="..."/>`; добавить один `<figcaption>` с уникальными
  подписями в порядке появления, объединёнными через ` — `.
- [ ] HTML-экранировать подписи и URL, не добавлять literal CR/LF и не создавать пустые
  `Смотрите`, slideshow или caption.
- [ ] Учитывать текст figcaption в `MESSAGE_LIMIT`, не считать URL атрибутов и сохранить
  существующее усечение только `Content`.
- [ ] Заменить фиксированное предположение о двух blocks на книгу динамическим
  подсчётом Rich Message blocks для обычных и slideshow-книг, не превышая 500 blocks.
- [ ] Покрыть тестами полный/частичный slideshow, отключённый Slink, другой профиль, подписи
  и escaping, порядок и повторы, rune limit, content truncation и смешанную границу 500 blocks.
- [ ] Запустить `make test`; все тесты должны пройти до Task 4.

### Task 4: Встроить подготовку фотографий и очистку файлов в digest lifecycle

**Files:**

- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Modify: `internal/store/store.go`
- Modify: `internal/store/store_test.go`

- [ ] Добавить узкий интерфейс подготовки фотографий в `app.Dependencies` и расширить `State`
  операцией сохранения подготовленной pending-книги.
- [ ] После загрузки pending queue и сортировки, но до render, последовательно подготовить
  только pending-книги с фотоссылками.
- [ ] После каждого изменённого объявления сохранить результат в bbolt до формирования
  и отправки Telegram chunks.
- [ ] Только после успешной загрузки изображений в Slink и успешной записи
  подготовленной книги в bbolt удалить все временные файлы и каталог этой книги.
- [ ] Гарантировать аварийную очистку временного каталога при ошибке подготовки,
  ошибке записи или отмене контекста; при ошибке записи не переходить к Telegram send.
- [ ] Завершить очистку файлов книги до перехода к следующей книге и до render/send, не
  затрагивая временные файлы других книг.
- [ ] Передавать активный профиль Slink в renderer; `nil` processor должен полностью сохранять
  прежний путь.
- [ ] Сохранить существующие гарантии `BeforeDelivery`, refresh button, chunk acknowledgement, retry_after и
  `ErrMessageTooLong`.
- [ ] Покрыть тестами порядок prepare → Slink upload → state save → cleanup → render → send, наличие файлов
  во время state save, их отсутствие после него, повторное использование сохранённых Slink URL,
  no-op без processor, частичный успех, state error, cancellation и отсутствие преждевременного acknowledgement.
- [ ] Запустить `make test`; все тесты должны пройти до Task 5.

### Task 5: Добавить конфигурацию, wiring, tmpfs и документацию

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `docker-compose.yml`
- Modify: `README.md`

- [ ] Добавить `SLINK_URL`, `SLINK_API_KEY`, `SLINK_TAG_ID`; пустые значения отключают интеграцию, а
  частичная группа конфигурации завершается ошибкой с именем переменной.
- [ ] Проверять `SLINK_URL` как HTTP(S) base URL без userinfo/query/fragment и `SLINK_TAG_ID` как UUID; API key не включать
  в ошибки или логи.
- [ ] Создавать Slink processor в `main` только при полной конфигурации, используя `HTTP_TIMEOUT` и
  общий structured logger.
- [ ] Добавить интеграционный тест полного пути: Alib photo link → META refresh → image/non-image → Slink
  upload/tag → bbolt save → temp cleanup → Telegram HTML; проверить отсутствие API key в логах.
- [ ] Передать новые переменные через Compose без встраивания секретов в image и
  смонтировать защищённый writable tmpfs на `/tmp` при read-only root filesystem.
- [ ] Обновить README: переменные, обязательный тег `alib`, Auto-publish, best effort, META refresh, лимит 15 MiB,
  жизненный цикл временных файлов, persistence и формат slideshow/caption.
- [ ] Проверить Compose командой `docker compose config`.
- [ ] Запустить `make test`; все тесты должны пройти до Task 6.

### Task 6: Verify acceptance criteria

- [ ] Запустить `make fmt`.
- [ ] Запустить `make verify`: format check, полный lint, race/shuffle tests и build должны пройти.
- [ ] Запустить `make coverage`; общее statement coverage должно быть не ниже 80%.
- [ ] Повторно запустить `docker compose config`.
- [ ] Проверить автоматическими тестами, что без Slink вывод не меняется, а с Slink
  успешные изображения попадают в slideshow, не-изображения и сбои остаются в `Смотрите`.
- [ ] Проверить автоматическими тестами, что повторный digest использует сохранённые
  Slink URL и не выполняет повторную загрузку.
- [ ] Проверить автоматическими тестами, что файлы книги существуют при записи
  результата в bbolt и удалены до render/send, включая пути ошибок и отмены.
- [ ] Проверить task-only diff и отсутствие токенов, временных файлов, баз данных и
  бинарников.
- [ ] Создать Conventional Commit `feat: add Slink image slideshows`.

## Post-Completion

Перед включением функции оператор должен создать в Slink тег `alib`, получить его UUID,
создать API key, включить для пользователя `Auto-publish API uploads` и задать `SLINK_URL`, `SLINK_API_KEY`,
`SLINK_TAG_ID`.
