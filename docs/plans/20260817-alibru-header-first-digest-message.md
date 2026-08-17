# Заголовок digest только в первом Telegram-сообщении

## Overview

Изменить chunking Telegram digest: заголовок `Новые книги на Alib.ru` добавляется только в первый
chunk пачки. Одиночное сообщение сохраняет заголовок. Последующие chunks начинаются
непосредственно с книги.

## Context

- Files involved:
  - `internal/digest/render.go`
  - `internal/digest/render_test.go`
  - `internal/app/service_test.go`
  - `README.md`
- Related patterns:
  - `digest.Render` создаёт chunks и сейчас инициализирует каждый новый chunk значением `header`.
  - Разделитель `<hr/>` добавляется только между книгами внутри одного chunk.
  - Лимит считается в Unicode-рунах по фактическому HTML каждого chunk.
  - `internal/app.Service` отправляет готовые chunks без изменения текста; только последний chunk
    получает звук и кнопку `Обновить`.
- Dependencies:
  - Новые зависимости не нужны.

## Development Approach

- **Testing approach**: TDD — сначала изменить focused regression tests, затем реализацию.
- Complete each task fully before moving to the next.
- Сохранить chunking только между целыми книгами, порядок книг и привязку `Chunk.Books`.
- Считать заголовок в лимите только первого chunk; последующие chunks проверять по
  фактическому тексту без заголовка.
- Сохранить `<hr/>` только между книгами одного chunk, без разделителей по краям.
- Использовать только Makefile targets для проверок.
- **CRITICAL: every task MUST include new/updated tests**
- **CRITICAL: all tests must pass before starting next task**

## Implementation Steps

### Task 1: Изменить формирование chunks

**Files:**

- Modify: `internal/digest/render.go`
- Modify: `internal/digest/render_test.go`

- [x] Обновить split-тест: первый chunk содержит `Новые книги на Alib.ru`, второй и последующие
  chunks не содержат заголовок.
- [x] Добавить утверждение, что во всей многочастной пачке заголовок встречается
  ровно один раз и находится в первом chunk.
- [x] Сохранить тест одиночного chunk с заголовком.
- [x] Обновить `digest.Render`: инициализировать заголовком только первый `Chunk`, а после split
  создавать пустой `Chunk`.
- [x] Проверять `MESSAGE_LIMIT` по фактическому тексту: с заголовком для первого chunk, без
  него для последующих.
- [x] Сохранить корректные `Chunk.Books`, rune-based limits и отсутствие `<hr/>` на границах chunks.
- [x] Выполнить `make test`; исправить все ошибки до Task 2.

### Task 2: Закрепить сервисное поведение и документацию

**Files:**

- Modify: `internal/app/service_test.go`
- Modify: `README.md`

- [ ] Обновить многочастный service-тест: заголовок присутствует только в первом
  отправленном сообщении.
- [ ] Сохранить проверки порядка книг, per-chunk acknowledgement, silent-флагов и кнопки `Обновить`
  только на последнем chunk.
- [ ] Обновить README: при разбиении digest заголовок находится только в первом
  Telegram-сообщении.
- [ ] Не менять конфигурацию, Telegram transport и формат отдельных книг.
- [ ] Выполнить `make test`; исправить все ошибки до Task 3.

### Task 3: Verify acceptance criteria

**Files:**

- Modify: none expected

- [ ] Выполнить `make verify`.
- [ ] Выполнить `make coverage`.
- [ ] Подтвердить statement coverage не ниже 80%.
- [ ] Подтвердить тестами: single-chunk digest содержит заголовок; multi-chunk digest содержит его
  ровно один раз в первом chunk; последующие chunks укладываются в лимит без заголовка.
- [ ] Проверить итоговый diff, исключить посторонние изменения и подготовить Conventional Commit.

### Task 4: Update documentation

- [ ] Убедиться, что `README.md` точно описывает новое расположение заголовка.
- [ ] Не обновлять `CLAUDE.md`: внутренние архитектурные паттерны не меняются.
