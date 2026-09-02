# Корректный лимит Telegram Rich Message и регрессия книги Беляева

## Overview

Исправить подсчёт длины дайджеста: учитывать руны отображаемого Rich Message текста, а не исходного HTML с тегами и длинными `href`. Увеличить стандартный `MESSAGE_LIMIT` с 4000 до безопасных 32000 символов при официальном максимуме 32768. Сокращать только `Book.Content`, когда отображаемый текст действительно превышает лимит. Добавить стабильный регрессионный тест с проблемной книгой Беляева.

## Context

- Files involved: `internal/config/config.go`, `internal/config/config_test.go`, `docker-compose.yml`, `internal/digest/render.go`, `internal/digest/render_internal_test.go`, `internal/digest/render_test.go`, `internal/app/service_test.go`, `internal/digest/testdata/belyaev-long-photo-listing.html`, `README.md`, `AGENTS.md`.
- Root cause:
  - объявление содержит 16 длинных ссылок на фотографии;
  - текущий HTML объявления содержит 6177 рун, из которых 4169 остаются даже без описания;
  - отображаемый текст полного объявления содержит только 2705 рун;
  - текущий код ошибочно считает теги и значения `href`, поэтому сокращение одного `Content` не может устранить ошибку при лимите 4000.
- Telegram Bot API подтверждает лимит Rich Message до 32768 UTF-8 символов именно в тексте сообщения: [Rich Message Limits](https://core.telegram.org/bots/api#rich-message-limits).
- `sendRichMessage` принимает `InputRichMessage`, включая HTML, уведомления и reply markup: [sendRichMessage](https://core.telegram.org/bots/api#sendrichmessage).
- Используемая библиотека `github.com/go-telegram/bot` v1.23.0 уже содержит `SendRichMessage`, `SendRichMessageParams` и `models.InputRichMessage.HTML`; текущий адаптер уже использует этот путь. Обновление зависимости или транспортного кода не требуется.
- Related patterns:
  - `digest.render` централизованно проверяет отдельные книги, формирует чанки и вызывается из `Render` и `RenderSendable`;
  - `truncateContent` бинарным поиском выбирает максимальный префикс исходного `Content`;
  - `golang.org/x/net/html` уже является прямой зависимостью проекта;
  - текущий безопасный стандартный лимит оставляет запас относительно официального максимума: 4000 против 4096. Аналогично новый default будет 32000 при hard limit 32768.
- Dependencies: новые зависимости не нужны; `go-telegram/bot` остаётся на v1.23.0.
- Сохраняемые ограничения:
  - разрешено сокращать только `Book.Content`;
  - исходная книга в `Chunk.Books` не изменяется;
  - `ErrMessageTooLong` сохраняется, если отображаемые обязательные поля вместе с минимальным описанием не помещаются;
  - чанки разделяются только между целыми книгами;
  - пользовательский `MESSAGE_LIMIT` остаётся доступен в диапазоне 64..32768.

## Development Approach

- **Testing approach**: TDD.
- Полностью завершать каждую задачу перед следующей.
- Использовать единый внутренний счётчик отображаемых рун для всех проверок лимита.
- После каждой задачи запускать канонический `make test`.
- **CRITICAL: every task that changes code MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Увеличить конфигурационный лимит Rich Message

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `docker-compose.yml`

- [x] Обновить тест стандартной конфигурации: `MESSAGE_LIMIT` по умолчанию равен 32000.
- [x] Добавить граничные тесты: значение 32768 принимается, 32769 и значения ниже 64 отклоняются с ошибкой, называющей `MESSAGE_LIMIT`.
- [x] Изменить `defaultMessageLimit` на 32000 и `telegramHardMessageLimit` на официальный максимум 32768.
- [x] Обновить Compose default до 32000, сохранив возможность пользовательского переопределения.
- [x] Запустить `make test`; команда должна пройти до Task 2.

### Task 2: Считать длину отображаемого Rich Message текста

**Files:**

- Modify: `internal/digest/render.go`
- Create: `internal/digest/render_internal_test.go`
- Modify: `internal/digest/render_test.go`
- Modify: `internal/app/service_test.go`

- [x] Добавить тесты счётчика: HTML-сущность считается как одна руна, текст ссылки учитывается, `href` и форматирующие теги не учитываются, `<br/>` считается переносом, `<hr/>` не добавляет текст.
- [x] Реализовать один закрытый helper на базе `golang.org/x/net/html`, считающий декодированные текстовые руны и переносы в сгенерированном Rich HTML.
- [x] Заменить сырой `utf8.RuneCountInString` этим helper во всех проверках заголовка, отдельной книги, текущего чанка и кандидатов бинарного поиска.
- [x] Сохранить запас `MESSAGE_LIMIT - 1` для сокращённого описания и максимальный подходящий префикс перед `…`.
- [x] Обновить существующие граничные тесты чанкинга и сокращения, чтобы их лимиты задавались в рунах отображаемого текста.
- [x] Проверить, что слишком длинные обязательные текстовые поля по-прежнему дают `ErrMessageTooLong`, а другие книги отправляются.
- [x] Запустить `make test`; команда должна пройти до Task 3.

### Task 3: Добавить регрессионный сценарий книги Беляева

**Files:**

- Create: `internal/digest/testdata/belyaev-long-photo-listing.html`
- Modify: `internal/digest/render_test.go`

- [x] Сохранить минимальный UTF-8 HTML-фрагмент только проблемного объявления, без полной скачанной страницы.
- [x] Разобрать fixture существующим `alib.Parse` и проверить точный `BuyURL` и наличие 16 фотографий.
- [x] Отрендерить книгу с лимитом 4000 и проверить отсутствие `ErrMessageTooLong`; это более строгое условие, чем новый default 32000.
- [x] Проверить, что сырой HTML длиннее 4000 рун, но отображаемый Rich Message текст не превышает лимит.
- [x] Проверить сохранение полного описания, всех обязательных полей, всех ссылок на фотографии и исходной книги в `Chunk.Books`.
- [x] Дополнительно отрендерить книгу с новым стандартным лимитом 32000, подтверждая штатный производственный сценарий.
- [x] Запустить `make test`; команда должна пройти до Task 4.

### Task 4: Verify acceptance criteria

- [x] Запустить полный `make verify`.
- [x] Запустить `make coverage` и подтвердить общее покрытие не ниже 80%.
- [x] Проверить `docker compose config` с новым стандартным `MESSAGE_LIMIT`.
- [x] Подтвердить регрессионным тестом, что книга Беляева отправляема при лимитах 4000 и 32000.
- [x] Подтвердить, что реальное превышение отображаемого лимита сокращает только `Content`.
- [x] Подтвердить сохранение `ErrMessageTooLong` для непомещающихся обязательных текстовых полей.

### Task 5: Update documentation

**Files:**

- Modify: `README.md`
- Modify: `AGENTS.md`

- [ ] Обновить стандартный `MESSAGE_LIMIT` до 32000 и допустимый диапазон до 64..32768.
- [ ] Уточнить, что лимит применяется к рунам отображаемого Rich Message текста после разбора HTML.
- [ ] Зафиксировать, что теги и URL в атрибутах не расходуют текстовый лимит, а переносы строк расходуют.
- [ ] Сохранить правило сокращения только `Content` до максимального префикса с `…`.
- [ ] Уточнить условие `ErrMessageTooLong` через отображаемую длину обязательных полей.
- [ ] Повторно запустить `make verify`, `make coverage` и `docker compose config`.
- [ ] Проверить итоговый diff, включить только относящиеся к задаче файлы и создать Conventional Commit.
