# HTTP API казино

Базовый путь: `/api` вне Discord, `/.proxy/api` внутри Discord. Тела — JSON. Все игровые запросы требуют `Authorization: Bearer <token>`.

Ошибки: `4xx/5xx` с телом `{ "error": "текст для игрока" }`. `401` — нужна новая сессия, `409` — уже есть активная партия/ракета, `422` — неверная ставка или параметры.

После каждого итога в ответе есть `multiplier` и `payout` (выигрыш в фантиках). `429` — слишком частые ставки (не больше одной новой ставки в секунду).

## Вход

### `GET /api/config`
→ `{ "clientId": "123", "dev": true }`

### `POST /api/session`
```json
{ "code": "oauth-code-from-sdk", "guildId": "456" }
```
В режиме разработки оба поля можно не передавать.
→ `{ "token": "…", "user": { "id": "789", "name": "kawasaki" } }`

## Рулетка

### `POST /api/roulette`
```json
{ "id": "uuid", "stake": 20, "bet": { "kind": "red" } }
```
`kind`: `red | black | even | odd | low | high | dozen | number`; `value` для `dozen` (1–3) и `number` (0–36).
→ `{ "number": 17, "multiplier": 0, "payout": 0 }`

Повтор с тем же `id` возвращает тот же ответ без нового начисления.

## Plinko

### `GET /api/plinko/tables`
→ `{ "8": { "low": [2.74, 1.89, …], "medium": […], "high": […] }, …, "16": {…} }`

### `POST /api/plinko`
```json
{ "id": "uuid", "stake": 37, "rows": 8, "risk": "low" }
```
→ `{ "path": [0,1,1,0,1,0,0,1], "slot": 4, "multiplier": 0.62, "payout": 23 }`

## Блэкджек

Карта — строка `"<ранг><масть>"`: ранги `2–9 T J Q K A`, масти `s h d c`; скрытая — `"??"`.

Состояние партии:
```json
{
  "id": "hand-id", "seq": 1, "stake": 20,
  "player": ["As", "7d"], "dealer": ["Kh", "??"],
  "playerTotal": 18, "status": "playing",
  "canDouble": true
}
```
`status`: `playing | blackjack | win | push | lose | bust`. При итоге добавляются `multiplier`, `payout`, и карты дилера раскрыты.

### `GET /api/blackjack` → состояние активной партии или `204`
### `POST /api/blackjack` `{ "stake": 20 }` → состояние (`409`, если партия уже идёт)
### `POST /api/blackjack/action` `{ "id": "hand-id", "seq": 1, "action": "hit" }` → состояние
Если `seq` не совпадает с текущим, партия не меняется и возвращается текущее состояние.

## Рокетка

### `POST /api/rocket`
```json
{ "stake": 50, "auto": 1.5 }
```
→ `{ "id": "round-id", "growth": 0.06, "maxMultiplier": 100 }`. Клиент анимирует `e^(growth·t)` от момента ответа.

### `POST /api/rocket/cashout` `{ "id": "round-id" }`
→ `{ "status": "won", "multiplier": 2.03, "crash": 3.41, "payout": 101 }`
или `{ "status": "crashed", "multiplier": 0, "crash": 1.87, "payout": 0 }`.
Повторный вывод возвращает тот же итог.

### `GET /api/rocket/{id}`
Ждёт до 25 с. Если раунд ещё летит → `{ "status": "flying" }`, иначе итог в том же формате, что у `cashout`.
