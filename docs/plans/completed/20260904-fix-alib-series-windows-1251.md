# Исправление кодировки ALIB_SERIES

## Overview

Кодировать значения `ALIB_SERIES` в Windows-1251 перед percent-encoding query-параметра `seria`. Сейчас
используется UTF-8, поэтому Alib.ru получает `отцы основатели` как `рссс рсррррсррре`.
Ответы страниц уже корректно декодируются по `charset`; менять парсер не требуется.

## Context

- Files involved:
  - `internal/config/config.go`
  - `internal/config/config_test.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - `buildAlibURLs` формирует endpoints после разбора CSV.
  - `golang.org/x/text/encoding/charmap` уже используется проектом и входит в прямые зависимости.
  - Полный `RawQuery` сохраняется при подмене host в интеграционных тестах и попадает в page logs.
- Confirmed behavior:
  - UTF-8 query отображается Alib.ru как `рссс рсррррсррре`.
  - Windows-1251 query отображается как `отцы основатели` и возвращает соответствующие объявления.
- Dependencies:
  - Новые зависимости не нужны.

## Development Approach

- **Testing approach**: Regular — минимальное исправление, затем регрессионные тесты.
- Преобразовывать только значения `ALIB_SERIES`; категории и декодирование ответов не менять.
- Сначала кодировать строку в Windows-1251, затем применять стандартный `url.QueryEscape` к полученным байтам.
- Отклонять непредставимые в Windows-1251 символы через `config.ErrInvalid` с указанием `ALIB_SERIES`, не заменяя
  и не теряя символы.
- Сохранить CSV-семантику, порядок, повторы, `lday=7` и защиту от query injection.
- Complete each task fully before moving to the next.
- **CRITICAL: every task that changes code must include new or updated tests.**
- **CRITICAL: all tests must pass before starting the next task.**

## Implementation Steps

### Task 1: Исправить формирование URL серий и регрессионные тесты

**Files:**

- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/alib-fetcher/main_test.go`

- [x] В `buildAlibURLs` кодировать каждое значение серии через существующий Windows-1251 encoder до вызова
  `url.QueryEscape`.
- [x] Оборачивать ошибку непредставимого символа в `config.ErrInvalid`, называя `ALIB_SERIES` и проблемный
  элемент.
- [x] Сохранить существующее формирование URL, порядок категорий и серий, повторы, CSV-значения и `lday=7`.
- [x] Добавить регрессионный случай `отцы основатели` с ожидаемым query
  `seria=%EE%F2%F6%FB+%EE%F1%ED%EE%E2%E0%F2%E5%EB%E8&lday=7`.
- [x] Обновить ожидаемые URL для существующих кириллических серий; сохранить проверки пробелов, запятых, `&`,
  `/` и ASCII.
- [x] Добавить проверку отказа для символа вне Windows-1251 без частично сформированной конфигурации.
- [x] Обновить интеграционный тест запуска: проверить Windows-1251 `RawQuery`, корректное смысловое значение после
  декодирования и полный URL в page logs.
- [x] Выполнить `make test`; весь набор тестов должен пройти до Task 2.

### Task 2: Обновить контракт конфигурации

**Files:**

- Modify: `README.md`
- Modify: `AGENTS.md`

- [x] Уточнить, что `ALIB_SERIES` вводится как обычный Unicode CSV, но каждое значение должно представляться в
  Windows-1251.
- [x] Описать, что query-параметр `seria` содержит percent-encoded байты Windows-1251, поскольку этого ожидает
  Alib.ru.
- [x] Зафиксировать ошибку конфигурации для непредставимых символов.
- [x] Не менять документацию категорий, порядка endpoints, повторов и фиксированного окна `lday=7`.
- [x] Не обновлять `CLAUDE.md`: файл отсутствует, новые внутренние компоненты не вводятся.

### Task 3: Verify acceptance criteria

- [x] Выполнить `make verify`; форматирование, strict lint, race-enabled shuffled tests и build должны пройти.
- [x] Выполнить `make coverage`; statement coverage должна остаться не ниже 80%.
- [x] Проверить task-related diff: production-код не формирует UTF-8 query для кириллических `ALIB_SERIES`.
- [x] Подтвердить тестами точный Windows-1251 query для `отцы основатели`, прежнее поведение ASCII и спецсимволов,
  а также отказ для непредставимых символов.
- [x] Подтвердить отсутствие изменений в `ALIB_CATEGORIES`, CSV-разборе, порядке endpoints, логировании и
  декодировании HTML-ответов.
