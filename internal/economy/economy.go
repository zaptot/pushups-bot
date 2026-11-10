// Package economy — кошелёк поинтов: 1 сделанное отжимание = 1 поинт, их тратят в казино и магазине.
//
// Баланс не хранится, а считается: стартовый бонус + отжимания из entries (kind = 'done') с начала
// экономики + сумма движений в wallet (ставки, выигрыши, покупки). Поэтому /done и /undo меняют баланс
// сами собой, а всё остальное пишется строками в wallet.
package economy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pushups-bot/internal/store"
)

const StartBonus = 100

// Start — понедельник 9 ноября 2026, 00:00 по Берлину: отжимания с этого момента дают поинты.
var Start = time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)

// ErrNotEnough — на балансе меньше, чем нужно.
var ErrNotEnough = errors.New("Не хватает поинтов")

type Item struct {
	Key, Title string
	Price      int
}

var Items = []Item{
	{"voucher", "Ваучер Steam 5 $", 1000},
	{"photo", "Фото Никиты в маске Инвокера без майки", 2000},
}

func FindItem(key string) (Item, bool) {
	for _, it := range Items {
		if it.Key == key {
			return it, true
		}
	}
	return Item{}, false
}

const balanceSQL = `SELECT $3::int
	+ COALESCE((SELECT SUM(amount) FROM entries WHERE guild_id = $1 AND user_id = $2 AND kind = 'done' AND created_at >= $4), 0)::int
	+ COALESCE((SELECT SUM(amount) FROM wallet WHERE guild_id = $1 AND user_id = $2), 0)::int`

func balance(ctx context.Context, q store.Querier, guildID, userID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, balanceSQL, guildID, userID, StartBonus, Start).Scan(&n)
	return n, err
}

// Balance — текущий баланс участника.
func Balance(guildID, userID int64) (int, error) {
	ctx, cancel := store.Ctx()
	defer cancel()
	return balance(ctx, store.DB, guildID, userID)
}

// Move — одно движение по кошельку. Ref уникален: повтор с тем же Ref ничего не меняет.
type Move struct {
	Amount int
	Ref    string
	BetID  string // для ставок казино: чтобы вернуть незавершённые после перезапуска
	Reason string
}

// Insert — запись, которая идёт в одной транзакции с движениями (например, итог ставки).
// Если строка не вставилась (ON CONFLICT DO NOTHING), Apply откатывает всё и возвращает Conflict.
type Insert struct {
	SQL      string
	Args     []any
	Conflict error
}

// Apply в одной транзакции (с блокировкой кошелька участника) проверяет, что после списаний баланс
// не уйдёт в минус, вызывает before (например, создание заказа) и записывает движения.
// Возвращает баланс после операции.
func Apply(guildID, userID int64, before func(ctx context.Context, tx pgx.Tx) error, moves ...Move) (int, error) {
	return apply(guildID, userID, before, nil, moves)
}

// ApplyInsert — как Apply, но вместо before одна вставка, которая уходит в базу одной пачкой с движениями.
func ApplyInsert(guildID, userID int64, ins Insert, moves ...Move) (int, error) {
	return apply(guildID, userID, nil, &ins, moves)
}

// apply обходится четырьмя обменами с базой: BEGIN, пачка «блокировка + баланс + уже записанные
// движения», пачка вставок, COMMIT. Каждый обмен с Supabase — десятки миллисекунд, поэтому запросы
// не идут по одному.
func apply(guildID, userID int64, before func(ctx context.Context, tx pgx.Tx) error, ins *Insert, moves []Move) (int, error) {
	ctx, cancel := store.Ctx()
	defer cancel()
	tx, err := store.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	refs := make([]string, 0, len(moves))
	for _, m := range moves {
		refs = append(refs, m.Ref)
	}
	var bal int
	done := map[string]bool{}
	read := &pgx.Batch{}
	read.Queue("SELECT pg_advisory_xact_lock($1)", userID).Exec(func(pgconn.CommandTag) error { return nil })
	read.Queue(balanceSQL, guildID, userID, StartBonus, Start).QueryRow(func(row pgx.Row) error { return row.Scan(&bal) })
	read.Queue("SELECT ref FROM wallet WHERE ref = ANY($1)", refs).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var ref string
			if err := rows.Scan(&ref); err != nil {
				return err
			}
			done[ref] = true
		}
		return rows.Err()
	})
	if err := tx.SendBatch(ctx, read).Close(); err != nil {
		return 0, err
	}

	// уже записанное движение (повтор запроса) второй раз не спишется — и в проверке баланса не участвует
	spend := 0
	for _, m := range moves {
		if m.Amount < 0 && !done[m.Ref] {
			spend -= m.Amount
		}
	}
	if spend > bal {
		return bal, fmt.Errorf("%w: на балансе %s", ErrNotEnough, Points(bal))
	}
	if before != nil {
		if err := before(ctx, tx); err != nil {
			return bal, err
		}
	}

	write := &pgx.Batch{}
	if ins != nil {
		write.Queue(ins.SQL, ins.Args...).Exec(func(tag pgconn.CommandTag) error {
			if tag.RowsAffected() == 0 {
				return ins.Conflict
			}
			return nil
		})
	}
	for _, m := range moves {
		if m.Amount == 0 {
			continue
		}
		var betID *string
		if m.BetID != "" {
			betID = &m.BetID
		}
		write.Queue(`INSERT INTO wallet (guild_id, user_id, amount, ref, bet_id, reason)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (ref) DO NOTHING`, guildID, userID, m.Amount, m.Ref, betID, m.Reason).
			Exec(func(tag pgconn.CommandTag) error {
				if tag.RowsAffected() == 1 {
					bal += m.Amount
				}
				return nil
			})
	}
	if write.Len() > 0 {
		if err := tx.SendBatch(ctx, write).Close(); err != nil {
			return bal, err
		}
	}
	return bal, tx.Commit(ctx)
}

// RefundUnsettled возвращает ставки, по которым нет итога в casino_bets (партия потерялась при перезапуске).
// Вызывать при старте, пока ни одна партия не идёт.
func RefundUnsettled() (int64, error) {
	ctx, cancel := store.Ctx()
	defer cancel()
	tag, err := store.DB.Exec(ctx, `INSERT INTO wallet (guild_id, user_id, amount, ref, bet_id, reason)
		SELECT guild_id, user_id, -SUM(amount), 'refund:' || bet_id, bet_id, 'возврат незавершённой ставки'
		FROM wallet w
		WHERE bet_id IS NOT NULL AND amount < 0
			AND NOT EXISTS (SELECT 1 FROM casino_bets b WHERE b.id = w.bet_id)
			AND NOT EXISTS (SELECT 1 FROM wallet r WHERE r.ref = 'refund:' || w.bet_id)
		GROUP BY guild_id, user_id, bet_id
		ON CONFLICT (ref) DO NOTHING`)
	return tag.RowsAffected(), err
}

// Buy покупает товар: списывает поинты и создаёт заказ, который выдаёт админ.
func Buy(guildID, userID int64, item Item) (orderID int64, bal int, err error) {
	ref := fmt.Sprintf("order:%d:%d", userID, time.Now().UnixNano())
	bal, err = Apply(guildID, userID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO shop_orders (guild_id, user_id, item, price) VALUES ($1, $2, $3, $4) RETURNING id`,
			guildID, userID, item.Key, item.Price).Scan(&orderID)
	}, Move{Amount: -item.Price, Ref: ref, Reason: "магазин: " + item.Title})
	return orderID, bal, err
}

// CanUndoDone: можно ли отменить запись о сделанных отжиманиях, не уводя баланс в минус.
func CanUndoDone(guildID, userID int64, amount int, createdAt time.Time) (bool, int, error) {
	if createdAt.Before(Start) {
		return true, 0, nil
	}
	bal, err := Balance(guildID, userID)
	return bal-amount >= 0, bal, err
}

// Points склоняет «поинт»: 1 поинт, 2 поинта, 5 поинтов.
func Points(n int) string {
	a := n
	if a < 0 {
		a = -a
	}
	word := "поинтов"
	switch {
	case a%100 >= 11 && a%100 <= 14:
	case a%10 == 1:
		word = "поинт"
	case a%10 >= 2 && a%10 <= 4:
		word = "поинта"
	}
	return fmt.Sprintf("%d %s", n, word)
}

// OrderText — объявление о покупке для канала: товар выдаёт админ.
func OrderText(userID, orderID int64, item Item, bal int) string {
	return fmt.Sprintf("🛒 <@%d> купил «%s» за %s (заказ №%d). Выдаёт админ. Осталось %s.",
		userID, item.Title, Points(item.Price), orderID, Points(bal))
}
