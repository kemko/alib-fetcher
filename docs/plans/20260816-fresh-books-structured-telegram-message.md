# Свежесть книг и структурированное Telegram-сообщение

## Overview

Парсер преобразует HTML объявления в семантические поля через DOM, без regex-разбора HTML.
Telegram-сообщение получит маркеры `🔥`/`✨`, отдельные абзацы, структурированные данные
продавца и цены, содержание перед остальными сведениями и ссылку `Купить` внизу.

Необязательная настройка `FRESH_BOOKS` поддержит:

- пустое или отсутствующее значение — маркер `✨` отключён, работает только логика `🔥`;
- `age:5` — свежими считаются книги от `текущий год - 5` включительно;
- `since:2021` — свежими считаются книги от 2021 года включительно.

Текущий год всегда получает `🔥`; в январе предыдущий год также получает `🔥`, даже
при отключённом `FRESH_BOOKS` или нахождении вне порога. Остальные годы внутри
настроенного порога получают `✨`. Будущие и нераспознанные годы остаются без emoji.

## Context

- Files involved:
  - `internal/alib/book.go`
  - `internal/alib/parser.go`
  - `internal/alib/parser_test.go`
  - `internal/alib/client_test.go`
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/app/service.go`
  - `internal/app/service_test.go`
  - `internal/store/store_test.go`
  - `internal/process/*_test.go`
  - `cmd/alib-fetcher/main.go`
  - `cmd/alib-fetcher/main_test.go`
  - `docker-compose.yml`
  - `Makefile`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - `golang.org/x/net/html` и обход DOM через `Descendants`;
  - распознавание объявления по `<p>`, `<b>`, seller-anchor и ссылке `Купить`;
  - `BuyURL` как постоянный идентификатор;
  - полный `alib.Book` внутри JSON-записи bbolt;
  - HTML escaping и rune-based Telegram limits;
  - фиксируемые часы через `app.Dependencies.Now`;
  - timezone из `config.Config.Location`;
  - Makefile как единственный интерфейс обычной проверки.
- Parsing rules:
  - DOM разбивается на логические строки по `<br>`;
  - seller, purchase и photo sections распознаются по соответствующим anchor-узлам;
  - фиксированные текстовые метки вроде `Цена:` и `Состояние:` разбираются строковыми операциями;
  - regex для HTML и библиографического года не используется;
  - год ищется только в основной библиографической строке как четырёхзначное число с
    суффиксом `г` или `г.`.
- Persistence constraint:
  - старые pending-записи содержат поля `text_before_seller`, `text_before_buy`, `text_after_buy`;
  - они должны остаться отправляемыми после обновления, поэтому новая модель получает
    узкий JSON compatibility decoder;
  - текстовый fallback допустим только для сохранённых записей, где исходный DOM уже недоступен.
- Dependencies:
  - новые внешние зависимости не нужны;
  - тесты используют минимальные локальные HTML fixtures, не live Alib и не локальный `tnew7.txt`.

## Development Approach

- **Testing approach**: TDD — сначала регрессионные тесты, затем минимальная реализация.
- Завершать каждую задачу полностью до перехода к следующей.
- После каждой кодовой задачи выполнять `make test`; все тесты должны пройти.
- Парсер строить вокруг DOM-узлов и их порядка.
- Не менять deduplication, pending ordering, acknowledgement, retention и refresh semantics.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Добавить необязательную конфигурацию свежести

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `docker-compose.yml`

- [x] Добавить тесты отсутствующего и пустого `FRESH_BOOKS`: конфигурация успешно
  загружается, порог `✨` отключён.
- [x] Добавить типизированную необязательную настройку `FRESH_BOOKS`.
- [x] Для `age:N` принимать неотрицательное целое; нижняя включительная граница равна
  `текущий год - N`.
- [x] Для `since:YYYY` принимать четырёхзначный включительный нижний год.
- [x] Не назначать `age:5` по умолчанию: без указанного порога действует только логика `🔥`.
- [x] Добавить тесты режимов `age:N`, `since:YYYY`, `age:0` и включительных границ.
- [x] Добавить Compose passthrough `${FRESH_BOOKS:-}` без автоматического порога.
- [x] Выполнить `make test`; все тесты должны пройти до Task 2.

### Task 2: Разобрать объявление в семантическую DOM-модель

**Files:**

- Modify: `internal/alib/book.go`
- Modify: `internal/alib/parser.go`
- Modify: `internal/alib/parser_test.go`
- Modify: `internal/alib/client_test.go`

- [x] Добавить parser-тесты объявлений с содержанием, без содержания, без продавца, с
  фото, ISBN, `2026г.`, `1970 г.`, дубликатом и относительными URL.
- [x] Заменить три несемантических текстовых фрагмента полями: заголовок,
  библиография, год издания, содержание, продавец, seller URL, местоположение, цена,
  состояние/прочие сведения, purchase URL и наличие фото.
- [x] Построить логические строки обходом DOM; `<br>` задаёт границы, вложенные
  text/anchor-узлы сохраняют порядок.
- [x] Основным блоком считать заголовок и библиографический текст до строки продажи;
  ISBN оставить в этом блоке.
- [x] Разобрать строку продажи по seller-anchor, `Цена:` и purchase-anchor; поддержать объявления
  без seller-anchor.
- [x] Удалить только служебный префикс `BS - ` из отображаемого имени, сохранив seller URL.
- [x] После purchase-anchor считать строки до первой `Состояние:` содержанием; `Состояние:` и
  последующий непропущенный текст — остальными сведениями.
- [x] Исключить `Смотрите:`/photo anchors из текста и сохранить `HasPhotos`.
- [x] Извлекать последний валидный год из основной строки сканированием цифр и
  суффикса `г`/`г.`, без regex; ISBN и годы внутри содержания игнорировать.
- [x] Сохранить deduplication по разрешённому `BuyURL` и `alib.ErrNoBooks`.
- [x] Выполнить `make test`; все тесты должны пройти до Task 3.

### Task 3: Сохранить читаемость существующих pending-записей

**Files:**

- Modify: `internal/alib/book.go`
- Create: `internal/alib/book_test.go`
- Modify: `internal/store/store_test.go`
- Modify only if required by tests: `internal/store/store.go`

- [x] Зафиксировать тестом новую JSON-схему семантического `Book`.
- [x] Добавить тест декодирования старой записи с `text_before_seller`, `text_before_buy` и `text_after_buy`.
- [x] Преобразовывать старую форму в новую модель через custom JSON unmarshal и узкие
  `strings`-операции; regex и повторный разбор HTML не использовать.
- [x] Не переписывать валидные записи при `store.Open`; переводить их в новую схему только
  при последующей обычной записи.
- [x] Проверить сохранение title, bibliography, content, seller/location, price, condition,
  photo flag и `BuyURL`.
- [x] Сохранить транзакционную проверку JSON, совпадение `Book.BuyURL` с bbolt key и marker-only migration.
- [x] Проверить, что sent status, queue order и timestamps не теряются.
- [x] Выполнить `make test`; все тесты должны пройти до Task 4.

### Task 4: Реализовать emoji и новый формат объявления

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`

- [x] Добавить точные HTML-тесты для текущего года, января, предыдущего года,
  отключённого порога, обоих threshold-режимов, включительной нижней границы, старой
  книги, будущего и отсутствующего года.
- [x] Передавать рендереру limit, локальное время цикла и необязательную
  нормализованную нижнюю границу года через единые render options.
- [x] Добавлять `🔥 ` перед `<b>...</b>` для текущего года и предыдущего года в январе.
- [x] При настроенном пороге добавлять `✨ ` для остальных годов между включительной
  нижней границей и текущим годом.
- [x] При отключённом пороге не добавлять `✨`.
- [x] Применять январское правило раньше проверки настроенного порога.
- [x] Формировать объявление в порядке:
  1. emoji, bold title и библиография;
  2. пустая строка;
  3. содержание, если есть;
  4. пустая строка после содержания;
  5. продавец, цена, состояние/прочие сведения и фото;
  6. пустая строка;
  7. ссылка `Купить`.
- [x] Форматировать продавца как `Продавец: <a href="...">BotSad</a>, Москва.` и цену как
  `Цена: 3900 руб.` на отдельных строках.
- [x] При отсутствии содержания отделять основную строку пустой строкой непосредственно
  от seller/price/details блока.
- [x] Не создавать лишние пустые абзацы при отсутствии необязательных полей.
- [x] Сохранить HTML escaping текста и URL, полный source text, rune-based limits и
  разбиение только между книгами.
- [x] Обновить chunking и `ErrMessageTooLong` тесты с учётом emoji и новых переносов.
- [x] Выполнить `make test`; все тесты должны пройти до Task 5.

### Task 5: Передать freshness policy, время и timezone через use-case

**Files:**

- Modify: `internal/app/service.go`
- Modify: `internal/app/service_test.go`
- Modify: `cmd/alib-fetcher/main.go`
- Modify: `cmd/alib-fetcher/main_test.go`
- Modify as required: `internal/process/*_test.go`

- [ ] Добавить service-тесты с фиксированным cycle time, отключённым порогом и границей
  января в настроенной timezone.
- [ ] Передать необязательную freshness policy и `Location` из `config.Config` через bootstrap
  в `app.Dependencies`.
- [ ] Получать `cycleTime` один раз; использовать тот же момент для retention, persistence,
  обеих render phases и acknowledgement.
- [ ] Для классификации emoji преобразовывать `cycleTime` в настроенную `TIMEZONE`.
- [ ] Передавать одинаковые render options в проверку отдельной книги и финальный chunking.
- [ ] Обновить тестовые dependencies, сохранив oversized-skip, delivery order, retry,
  acknowledgement и refresh-button behavior.
- [ ] Расширить `-once` integration test: проверить отсутствие обязательности `FRESH_BOOKS`, оба
  настроенных режима, emoji, seller link, цену, содержание, абзацы и последнюю ссылку `Купить`.
- [ ] Выполнить `make test`; все тесты должны пройти до Task 6.

### Task 6: Verify acceptance criteria

**Files:**

- Modify: `Makefile`

- [ ] Добавить canonical `make coverage` target, создающий `coverage.out` и проверяющий порог не ниже 80%.
- [ ] Добавить или обновить недостающие автоматические тесты для выявленных граничных случаев.
- [ ] Выполнить `make fmt`, затем убедиться, что повторный format check не находит изменений.
- [ ] Выполнить полный `make verify`: `fmt-check`, strict lint, race/shuffle/no-cache tests и build должны пройти.
- [ ] Выполнить `make coverage`; покрытие должно быть не ниже 80%.
- [ ] Выполнить `docker compose config --quiet` с фиктивными обязательными Telegram variables.
- [ ] Автоматическими тестами подтвердить отключённый порог, оба режима `FRESH_BOOKS`,
  январское исключение, DOM parsing, порядок абзацев, seller link, положение `Купить`,
  HTML escaping и message limits.
- [ ] Проверить diff: regex не используется для HTML; `tnew7.txt`, credentials, DB и binaries не добавлены.

### Task 7: Update documentation

**Files:**

- Modify: `README.md`
- Modify: `AGENTS.md`

- [ ] Описать необязательный `FRESH_BOOKS`: отсутствие значения отключает только `✨`, а `🔥`
  продолжает работать.
- [ ] Документировать синтаксис `age:N`/`since:YYYY`, включительные границы и январское правило.
- [ ] Описать значения `🔥`/`✨` и новый порядок блоков Telegram-сообщения.
- [ ] Обновить описание сохраняемой модели `Book`, JSON compatibility и DOM-first parsing.
- [ ] Документировать `make coverage`.
- [ ] Добавить credential-free Compose/runtime override example.
- [ ] Повторно выполнить `make verify` и `make coverage`.
- [ ] Повторно проверить Compose configuration и финальный diff.
- [ ] Stage только относящиеся к задаче файлы и создать один сфокусированный Conventional Commit.
