# Логирование полных URL страниц Alib

## Overview

Убрать очистку query-параметров из URL в событиях Alib. Все четыре события страницы и связанные ошибки будут содержать полный настроенный URL, включая GET-параметры. Остальное поведение клиента не меняется.

## Context

- Files involved:
  - `internal/alib/client.go`
  - `internal/alib/client_test.go`
  - `cmd/alib-fetcher/main_test.go`
  - `README.md`
  - `AGENTS.md`
- Related patterns:
  - `endpointForLog` сейчас удаляет query и fragment перед записью `url` и формированием ошибок страницы.
  - `alib.page_downloaded`, `alib.page_download_failed`, `alib.page_parsed` и `alib.page_parse_failed` используют этот helper.
  - `ALIB_URL` уже сохраняет GET-параметры при HTTP-запросе, а userinfo отклоняется при конфигурации.
  - Защиту внутренних `url.Error` и проверку запрещённого userinfo менять не требуется.
- Dependencies:
  - Новые зависимости не нужны.

## Development Approach

- **Testing approach**: Regular — сначала минимальное изменение кода, затем обновление тестов.
- Использовать исходный `endpoint.String()` без отдельной очистки для логов и контекстных ошибок страницы.
- Не менять имена событий, набор атрибутов, порядок скачивания и парсинга либо обработку частичного успеха.
- Complete each task fully before moving to the next.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Передавать полный URL в события и ошибки Alib

**Files:**

- Modify: `internal/alib/client.go`
- Modify: `internal/alib/client_test.go`

- [x] Заменить использование `endpointForLog` на полный `endpoint.String()` во всех download/parse событиях и контекстных ошибках страницы.
- [x] Удалить ставший ненужным `endpointForLog`.
- [x] Сохранить отдельную обработку `url.Error` и валидацию userinfo без изменений.
- [x] Заменить тесты сокрытия query-параметров при download/parse failure тестами наличия полного URL.
- [x] Обновить общий тест исходов страниц: проверить query-параметры в успешных и ошибочных download/parse событиях.
- [x] Проверить, что агрегированная ошибка неуспешного fetch содержит URL соответствующих страниц с GET-параметрами.
- [x] Выполнить `make test`; все тесты должны пройти до Task 2.

### Task 2: Закрепить контракт на уровне запуска сервиса и документации

**Files:**

- Modify: `cmd/alib-fetcher/main_test.go`
- Modify: `README.md`
- Modify: `AGENTS.md`

- [x] Обновить интеграционные проверки JSON-логов: каждый Alib event должен содержать исходный URL вместе с его query-параметрами.
- [x] Удалить проверки отсутствия `scope`, `topic` и других GET-параметров в логах.
- [x] Обновить README: атрибут `url` содержит полный настроенный адрес, включая GET-параметры.
- [x] Обновить репозиторный контракт в AGENTS.md и убрать утверждения об исключении query-параметров из Alib-логов и ошибок.
- [x] Выполнить `make test`; все тесты должны пройти до Task 3.

### Task 3: Verify acceptance criteria

- [x] Выполнить `make verify`; форматирование, lint, race-enabled tests и build должны пройти.
- [x] Выполнить `make coverage`; statement coverage должно остаться не ниже 80%.
- [x] Проверить task-related diff: нет новых зависимостей и изменений вне логирования URL, тестов и документации.
- [x] Подтвердить тестами полный URL с GET-параметрами для всех четырёх Alib-событий.
