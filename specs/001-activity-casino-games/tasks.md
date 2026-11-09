---

description: "Tasks for Казино-игры в Discord Activity"
---

# Tasks: Казино-игры в Discord Activity

**Input**: Design documents from `/specs/001-activity-casino-games/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/http-api.md, quickstart.md

**Tests**: Включены, потому что спецификация требует проверить средний возврат (SC-003) и единственность начисления (SC-006). Все тесты — в одном файле `casino_test.go` (research.md §12).

**Organization**: Задачи сгруппированы по пользовательским историям. US5 (режим разработки) идёт первой: без неё ни одну игру нельзя проверить в браузере.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Можно делать параллельно (другие файлы, нет зависимостей от незавершённых задач)
- **[Story]**: К какой истории относится (US1–US5)

## Path Conventions

- Сервер: Go-файлы в корне репозитория, пакет `main`
- Клиент: `activity/` (Vite + TypeScript)
- Правила кода: без комментариев, `log.Printf`/`console.log` — объекты или поля по ключам, без новых Go-зависимостей

---

## Phase 1: Setup

**Purpose**: Каркас фронтенда и конфигурация

- [X] T001 [P] Создать `activity/package.json` (скрипты `dev`, `build`; зависимости `@discord/embedded-app-sdk`, dev — `vite`, `typescript`), `activity/tsconfig.json` (strict, `noImplicitAny`, target ES2022, moduleResolution bundler) и `activity/vite.config.ts` с прокси `/api` → `http://localhost:${ACTIVITY_PORT ?? 8080}`
- [X] T002 [P] Добавить в `.gitignore` строки `activity/node_modules/` и `activity/dist/`
- [X] T003 [P] Добавить в `.env.example` блок «Казино (Discord Activity)»: `ACTIVITY_PORT`, `ACTIVITY_DEV_USER_ID`, `DISCORD_CLIENT_SECRET`, `ACTIVITY_DIR` с пояснениями на русском, как у остальных переменных
- [X] T004 Добавить в `config.go` поля `ActivityPort`, `ActivityDevUserID int64`, `ClientSecret`, `ActivityDir` (по умолчанию `activity/dist` через `envOr`) и их чтение в `loadConfig()`

---

## Phase 2: Foundational

**Purpose**: Общая основа для всех игр: запись ставок, HTTP-сервер, сессии

**⚠️ CRITICAL**: Ни одна игра не начинается до завершения этой фазы

- [X] T005 Добавить в `schema` в `db.go` таблицу `casino_bets` (поля и типы из data-model.md: `id TEXT PRIMARY KEY`, `guild_id`, `user_id`, `game`, `stake`, `multiplier NUMERIC(8,2)`, `payout`, `result JSONB`, `created_at`) через `CREATE TABLE IF NOT EXISTS`
- [X] T006 Создать `casino.go`: константы `stakeMin = doneMin`, `stakeMax = 10000`; `validStake(n int) error`; `roundPayout(stake int, mult float64) int` со случайным округлением (research.md §5); тип `bet` (поля из data-model.md, `result any`); `settle(b bet) (json.RawMessage, error)` — одна транзакция через `db.Begin`: `INSERT INTO casino_bets ... ON CONFLICT (id) DO NOTHING`, при вставке — `INSERT INTO entries` (`kind='done'`, `source='manual'`, `author_id=user_id`, `reason = fmt.Sprintf("🎰 лудомания: %d × %.2f · %s", stake, mult, gameTitle)`), при конфликте — вернуть сохранённый `result`; `replay(id string, userID int64) (json.RawMessage, bool, error)` — сохранённый результат, если ставка этого игрока уже есть
- [X] T007 Создать `activity.go`: `serveActivity()` на `http.ServeMux` с маршрутами вида `"POST /api/roulette"`; `writeJSON(w, status, v)` и `writeError(w, status, text)` с телом `{ "error": ... }`; сессии `map[string]session` под `sync.Mutex` (токен 32 байта из `crypto/rand`, hex, срок 24 ч); middleware `auth(func(w, r, s session))` по `Authorization: Bearer`, `401` при отсутствии/истечении; `GET /api/config` → `{clientId: session.State.User.ID, dev: cfg.ActivityDevUserID != 0}`; раздача `cfg.ActivityDir` через `http.FileServer` на `/`, если папка существует; лог старта объектом полей
- [X] T008 В `activity.go` добавить `POST /api/session` для режима разработки: если `cfg.ActivityDevUserID != 0`, выдать сессию этому пользователю на сервере `cfg.GuildID`; иначе — путь Discord (T034), а до его реализации `401`
- [X] T009 Добавить в `activity.go` функцию `remaining(guildID, userID int64) int` через существующий `userStats(...).remaining()` для поля `remaining` всех итоговых ответов
- [X] T010 В `main.go` после регистрации команд запускать `go serveActivity()`, если `cfg.ActivityPort != ""`, с `defer protect(...)` как у остальных фоновых задач
- [X] T011 [P] Создать `activity/src/api.ts`: `inDiscord = new URLSearchParams(location.search).has('frame_id')`; `base = inDiscord ? '/.proxy/api' : '/api'`; токен в памяти; типизированная `request<T>(method, path, body?)` с `Authorization`, разбор `{error}` в `Error`; интерфейсы ответов из contracts/http-api.md (`RouletteResult`, `PlinkoResult`, `PlinkoTables`, `BlackjackState`, `RocketStart`, `RocketResult`, `Session`, `AppConfig`)
- [X] T012 [P] Создать `activity/src/style.css`: тёмная тема в цветах колеса (`#232428` фон, `#facc15` жёлтый, `#dc2626` красный), меню из четырёх карточек, панель ставки, адаптив под узкий экран мобильного Discord

**Checkpoint**: `go build ./...` проходит, `npm run build` в `activity/` проходит, бот без `ACTIVITY_PORT` работает как раньше

---

## Phase 3: User Story 5 — Опробовать игры вне Discord (Priority: P1)

**Goal**: Страница открывается в обычном браузере и входит как тестовый участник

**Independent Test**: С `ACTIVITY_DEV_USER_ID` страница показывает меню и имя игрока; без него `POST /api/session` без `code` даёт `401`

- [X] T013 [US5] Создать `activity/index.html` (контейнер `#app`, подключение `src/main.ts` и `src/style.css`) и `activity/src/main.ts`: `GET /api/config`; если `inDiscord` — путь SDK (заглушка до T035 бросает понятную ошибку), иначе `POST /api/session` без тела; затем меню из четырёх игр с переключением экранов; показ ошибок входа на экране
- [X] T014 [US5] Добавить в `casino_test.go` тест `TestCasinoDevSession`: через `httptest` с `cfg.ActivityDevUserID` сессия выдаётся, без него — `401`; запрос игры без токена — `401`

**Checkpoint**: `go run .` + `npm run dev` → в браузере меню с именем тестового игрока

---

## Phase 4: User Story 1 — Рулетка с зеро (Priority: P1) 🎯 MVP

**Goal**: Ставка в рулетку с честным результатом, записанным в статистику бота

**Independent Test**: Ставка 20 на красное → +40 или +0 в `/status`; повтор запроса с тем же `id` не создаёт второй записи

### Tests for User Story 1

- [X] T015 [P] [US1] В `casino_test.go` добавить `TestCasinoRoulettePayouts`: для каждого типа ставки и каждого из 37 чисел множитель по таблице research.md §6; средний возврат каждого типа ровно 36/37
- [X] T016 [P] [US1] В `casino_test.go` добавить `TestCasinoRoundPayout`: среднее `roundPayout(37, 0.5)` по 100 000 вызовам в пределах 18.5 ± 0.05, результат всегда 18 или 19; целые произведения не меняются
- [X] T017 [P] [US1] В `casino_test.go` добавить `TestCasinoReplay` (через `setup(t)` из `bot_test.go`): 10 одинаковых `POST /api/roulette` с одним `id` → одна строка в `entries` и в `casino_bets`, все ответы одинаковые; чужой пользователь с тем же `id` получает ошибку

### Implementation for User Story 1

- [X] T018 [US1] Создать `roulette.go`: множество красных чисел; `rouletteMultiplier(kind string, value, number int) (float64, error)` с проверкой `kind`/`value`; обработчик `POST /api/roulette` — `replay` → `validStake` → `rand.IntN(37)` → `roundPayout` → `settle` с `result` `{number, multiplier, payout, remaining}`; регистрация маршрута в `activity.go`
- [X] T019 [US1] Создать `activity/src/roulette.ts`: выбор ставки (поле 10–10 000) и типа (кнопки цветов, чёт/нечет, половины, дюжины, сетка 0–36); `crypto.randomUUID()` на каждую ставку; запрос; canvas-колесо в европейском порядке чисел с замедлением до числа из ответа (~4 с), затем показ выплаты и остатка; блокировка кнопки во время спина
- [X] T020 [US1] Подключить экран рулетки в меню `activity/src/main.ts`

**Checkpoint**: MVP — рулетка работает в браузере и пишет в общую статистику

---

## Phase 5: User Story 2 — Plinko (Priority: P2)

**Goal**: Шарик с выбором рядов и риска, экран совпадает с начислением

**Independent Test**: 16 рядов, высокий риск, ставка 37 → шарик в лунке из ответа, выплата ≈ 37 × множитель

### Tests for User Story 2

- [X] T021 [P] [US2] В `casino_test.go` добавить `TestCasinoPlinkoTables`: для всех 27 комбинаций `Σ C(n,k)/2^n · m_k` в пределах 0.99 ± 0.005; таблицы симметричны; при одинаковых рядах край высокого риска > края низкого, центр высокого < центра низкого

### Implementation for User Story 2

- [X] T022 [US2] Создать `plinko.go`: вычисление таблиц при инициализации пакета по формуле research.md §7 (`g` = 1.45 / 1.9 / 2.6, округление до сотых к ближайшему); `drop(rows) (path []int, slot int)`; обработчики `GET /api/plinko/tables` и `POST /api/plinko` (проверка `rows` 8–16 и `risk`, `replay`, `settle` с `{path, slot, multiplier, payout, remaining}`); регистрация маршрутов в `activity.go`
- [X] T023 [US2] Создать `activity/src/plinko.ts`: выбор рядов (8–16) и риска, загрузка таблиц один раз; canvas-пирамида штырьков и подписи лунок; анимация шарика по `path` — параболы между штырьками, ~0,15 с на ряд, подсветка лунки; затем выплата и остаток
- [X] T024 [US2] Подключить экран Plinko в меню `activity/src/main.ts`

**Checkpoint**: Рулетка и Plinko работают независимо

---

## Phase 6: User Story 3 — Блэкджек (Priority: P3)

**Goal**: Партия против дилера с «Ещё», «Хватит», «Удвоить» и автозавершением

**Independent Test**: Партия доигрывается, итог по правилам совпадает с начислением; закрытая карта дилера не видна в ответах до конца

### Tests for User Story 3

- [X] T025 [P] [US3] В `casino_test.go` добавить `TestCasinoBlackjackRules` на заданных колодах: подсчёт мягкого туза; дилер стоит на мягких 17; блэкджек x2.5, победа x2, ничья x1, перебор x0; «Удвоить» — одна карта и двойная ставка; ответ во время партии не содержит второй карты дилера и колоды; `seq` не совпал — состояние не меняется

### Implementation for User Story 3

- [X] T026 [US3] Создать `blackjack.go`: тип `card` и строковый вид `"Ah"`/`"??"`; `newDeck()` с тасовкой `rand.Shuffle`; `handTotal(cards) (total int, soft bool)`; тип `blackjackHand` (поля из data-model.md) с методами `hit`, `stand`, `double`, `dealerPlay`, `finish`, `view`; `map[[2]int64]*blackjackHand` под мьютексом; `time.AfterFunc(2*time.Minute, stand)` с перезапуском на каждом ходе; итог через `settle` с id партии, затем удаление из карты
- [X] T027 [US3] В `blackjack.go` добавить обработчики `GET /api/blackjack` (активная партия или `204`), `POST /api/blackjack` (`validStake`, `409` при активной партии, начальный блэкджек завершает сразу), `POST /api/blackjack/action` (проверка `id`, `seq`, `canDouble`); регистрация маршрутов в `activity.go`
- [X] T028 [US3] Создать `activity/src/blackjack.ts`: при открытии `GET /api/blackjack` для продолжения партии; стол на canvas — карты игрока и дилера с анимацией раздачи, суммы очков; кнопки «Ещё» / «Хватит» / «Удвоить» с передачей `seq`, блокировка на время запроса; экран итога с выплатой и остатком
- [X] T029 [US3] Подключить экран блэкджека в меню `activity/src/main.ts`

**Checkpoint**: Три игры работают независимо

---

## Phase 7: User Story 4 — Рокетка (Priority: P4)

**Goal**: Своя ракета у каждого игрока, вывод по часам сервера, автовывод

**Independent Test**: Ставка 50, вывод около x2 → ≈ 100; раунд с автовыводом x1.5 и закрытой вкладкой даёт итог в `/history`

### Tests for User Story 4

- [X] T030 [P] [US4] В `casino_test.go` добавить `TestCasinoRocket`: доля раундов с `crash ≥ m` для m ∈ {1.5, 2, 5, 10} на 200 000 моделированиях ≈ 0.99/m (± 1 п.п.); вывод при `m(t) ≥ crash` — проигрыш; автовывод ниже точки взрыва — выигрыш на `auto`; повторный `cashout` возвращает тот же итог

### Implementation for User Story 4

- [X] T031 [US4] Создать `rocket.go`: константы `rocketGrowth = 0.06`, `rocketMax = 100`; `crashPoint()` по формуле research.md §10; тип `rocketRound` (поля из data-model.md) с `cashout(now)` и `wait(ctx)`; `map` активных раундов под мьютексом, одна ракета на игрока; `time.AfterFunc` на момент `min(crash, auto)` завершает раунд; итог через `settle`; хранение завершённого раунда ещё 1 минуту для опроса
- [X] T032 [US4] В `rocket.go` добавить обработчики `POST /api/rocket` (`validStake`, проверка `auto`, `409` при активной ракете), `POST /api/rocket/cashout`, `GET /api/rocket/{id}` (ожидание до 25 с через `r.Context()` и `time.After`); регистрация маршрутов в `activity.go`
- [X] T033 [US4] Создать `activity/src/rocket.ts`: ставка и поле автовывода; после старта — canvas-график `e^(0.06·t)` с ракетой и крупным множителем через `requestAnimationFrame`; кнопка «Забрать»; параллельно цикл `GET /api/rocket/{id}` до итога; взрыв или выигрыш по ответу сервера; остановка анимации и опроса при уходе с экрана

**Checkpoint**: Все четыре игры работают в режиме разработки

---

## Phase 8: Polish & Cross-Cutting

- [X] T034 Реализовать вход через Discord в `POST /api/session` в `activity.go`: обмен `code` на токен (`POST https://discord.com/api/oauth2/token`, `client_id` = ID бота, `client_secret` = `cfg.ClientSecret`, `grant_type=authorization_code`), `GET https://discord.com/api/users/@me`, проверка членства `session.GuildMember(guildId, userID)`; `401`/`403` с понятным текстом; HTTP-клиент с таймаутом `newHTTPClient()`
- [X] T035 В `activity/src/main.ts` реализовать путь SDK: `new DiscordSDK(clientId)`, `ready()`, `commands.authorize({ client_id, response_type: 'code', state: '', prompt: 'none', scope: ['identify'] })`, `POST /session` с `{code, guildId: sdk.guildId}`, `commands.authenticate({ access_token })` не нужен — достаточно своей сессии
- [X] T036 Чистка сессий по истечении срока (при каждом новом входе; раунды рокетки и раздачи блэкджека уже удаляются своими таймерами), чтобы память не росла
- [ ] T037 Прогнать `go vet ./...`, `go test ./... -run Casino`, `npm run build` в `activity/` и сценарии из `specs/001-activity-casino-games/quickstart.md`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (1)** → **Foundational (2)** → **US5 (3)** → игры
- **US1, US2, US3, US4** зависят только от Phase 3 и друг от друга не зависят; каждая добавляет свой маршрут в `activity.go` и пункт меню в `main.ts`, поэтому эти правки делаются последовательно
- **Polish (8)**: T034–T035 можно делать в любой момент после Phase 2; T037 — в самом конце

### Within Each User Story

- Тест → логика на Go → обработчик → экран → пункт меню

### Parallel Opportunities

- Phase 1: T001, T002, T003 параллельно
- Phase 2: T011, T012 (фронтенд) параллельно с Go-задачами T005–T010
- Тесты [P] каждой истории параллельно с её Go-логикой (общий файл `casino_test.go` — добавлять функции, не трогая чужие)
- После Phase 3 разные игры можно вести параллельно, кроме правок `activity.go` и `main.ts`

## Parallel Example: User Story 1

```bash
Task: "T015 TestCasinoRoulettePayouts в casino_test.go"
Task: "T016 TestCasinoRoundPayout в casino_test.go"
Task: "T018 roulette.go — логика и обработчик"
```

---

## Implementation Strategy

### MVP First

1. Phase 1 + Phase 2 — основа
2. Phase 3 (US5) — вход в браузере
3. Phase 4 (US1) — рулетка
4. **STOP and VALIDATE**: сценарии 1, 2, 6 из quickstart.md

### Incremental Delivery

1. Рулетка → проверка
2. Plinko → проверка
3. Блэкджек → проверка
4. Рокетка → проверка
5. Вход через Discord (T034–T035) — когда решится хостинг

## Notes

- Коммит после каждой фазы, сообщения на английском
- Результат и время всегда определяет сервер; клиент только показывает
- В коде без комментариев; `ponytail:` — только для осознанных упрощений с известным потолком
