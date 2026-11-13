package casino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"pushups-bot/internal/economy"
	"pushups-bot/internal/store"
)

// Джекпот: с каждой ставки jackpotRate (1%) уходит в пул (это не списывается с игрока, а
// возвращается из дохода казино, который у игр составляет 2.7%–5%, поэтому общий RTP остаётся < 100%).
// Сорвать пул можно с любой ставки: шанс = min(jackpotMaxChance, ставка / jackpotDivisor) — он не
// зависит от размера пула, чтобы нельзя было выжидать горячий пул и снайпить его крупной ставкой.
// При jackpotDivisor = 50 000 и ставках ~20 поинтов пул срывается в среднем раз на ~2500 ставок, средний приз ~510.
const (
	jackpotSeed      = 50   // стартовый пул сервера (тот же, что в миграции в store.Schema)
	jackpotRate      = 0.01 // доля каждой ставки, которая идёт в пул
	jackpotKeep      = 0.10 // доля пула, которая остаётся затравкой следующего джекпота
	jackpotTarget    = 550  // около этого пул обычно срывается: по нему страница рисует «нагрев»
	jackpotMaxChance = 0.05 // потолок шанса с одной ставки
)

var (
	jackpotEnabled = true
	jackpotDivisor = 50_000.0
	jackpotRoll    = rand.Float64 // подменяется в тестах
)

// jackpotOutcome — что ставка сделала с джекпотом; уходит в ответ вместе с итогом игры.
type jackpotOutcome struct {
	Pool  int `json:"pool"`          // пул после ставки
	Added int `json:"added"`         // сколько ставка добавила в пул
	Won   int `json:"won,omitempty"` // сколько сорвал игрок (0 — не сорвал)
}

// jackpotContribution — доля ставки в пул; дробная часть округляется случайно, чтобы
// маленькие ставки тоже пополняли пул в среднем ровно на jackpotRate.
func jackpotContribution(stake int) int {
	return roundPayout(stake, jackpotRate)
}

func jackpotChance(stake int) float64 {
	return math.Min(jackpotMaxChance, float64(stake)/jackpotDivisor)
}

// jackpotPrize — сколько получает сорвавший; остаток (не меньше jackpotKeep пула) остаётся в пуле.
func jackpotPrize(pool int) int {
	return pool - int(math.Ceil(float64(pool)*jackpotKeep))
}

// withJackpot добавляет поле jackpot к JSON-итогу игры.
func withJackpot(result any, jp jackpotOutcome) (json.RawMessage, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields["jackpot"], err = json.Marshal(jp); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

// settle записывает итог ставки, начисляет выигрыш, пополняет и разыгрывает джекпот одной транзакцией.
// debit — списать ставку здесь же (для игр в один ход; у блэкджека и рокетки она списана в hold).
func settle(b bet, debit bool) (json.RawMessage, error) {
	added, roll := 0, 1.0
	if jackpotEnabled {
		added, roll = jackpotContribution(b.Stake), jackpotRoll()
	}
	var moves []economy.Move
	if debit {
		moves = append(moves, stakeMove(b.ID, "stake", b.Stake))
	}
	moves = append(moves, economy.Move{Amount: b.Payout, Ref: "win:" + b.ID, BetID: b.ID, Reason: "выигрыш в казино"})

	var result json.RawMessage
	var jp jackpotOutcome
	_, err := economy.Apply(b.GuildID, b.UserID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if !jackpotEnabled {
			result, err = json.Marshal(b.Result)
		} else {
			result, err = playJackpot(ctx, tx, b, added, roll, &jp)
		}
		if err != nil {
			return err
		}
		batch := &pgx.Batch{}
		batch.Queue(`INSERT INTO casino_bets (id, guild_id, user_id, game, stake, multiplier, payout, result)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb) ON CONFLICT (id) DO NOTHING`,
			b.ID, b.GuildID, b.UserID, b.Game, b.Stake, b.Multiplier, b.Payout, string(result)).
			Exec(func(tag pgconn.CommandTag) error {
				if tag.RowsAffected() == 0 {
					return errSettled
				}
				return nil
			})
		if jp.Won > 0 {
			batch.Queue(`UPDATE jackpot SET pool = $2 WHERE guild_id = $1`, b.GuildID, jp.Pool)
			batch.Queue(`INSERT INTO jackpot_wins (bet_id, guild_id, user_id, name, game, amount) VALUES ($1, $2, $3, $4, $5, $6)`,
				b.ID, b.GuildID, b.UserID, b.Name, b.Game, jp.Won)
			batch.Queue(`INSERT INTO wallet (guild_id, user_id, amount, ref, bet_id, reason) VALUES ($1, $2, $3, $4, $5, $6)`,
				b.GuildID, b.UserID, jp.Won, "jackpot:"+b.ID, b.ID, "джекпот казино")
		}
		return tx.SendBatch(ctx, batch).Close()
	}, moves...)
	// ставка с этим id уже записана (повтор запроса): отдаём сохранённый итог, ничего не списав
	if errors.Is(err, errSettled) {
		stored, _, err := replay(b.ID, b.UserID)
		return stored, err
	}
	if err != nil {
		return nil, err
	}
	if jp.Won > 0 {
		log.Printf("Джекпот: %d сорвал %d в %s (ставка %s)", b.UserID, jp.Won, b.Game, b.ID)
		announceJackpot(b, jp.Won)
	}
	return result, nil
}

// playJackpot пополняет пул (строка блокируется до конца транзакции, ставки разыгрывают его по очереди),
// разыгрывает срыв и возвращает итог игры с полем jackpot.
func playJackpot(ctx context.Context, tx pgx.Tx, b bet, added int, roll float64, jp *jackpotOutcome) (json.RawMessage, error) {
	var pool int
	// первая ставка сервера создаёт пул сразу со стартовой затравкой
	if err := tx.QueryRow(ctx, `INSERT INTO jackpot (guild_id, pool, seeded) VALUES ($1, $2::int + $3::int, true)
			ON CONFLICT (guild_id) DO UPDATE SET pool = jackpot.pool + $2::int RETURNING pool`,
		b.GuildID, added, jackpotSeed).Scan(&pool); err != nil {
		return nil, err
	}
	*jp = jackpotOutcome{Pool: pool, Added: added}
	if roll < jackpotChance(b.Stake) {
		jp.Won = jackpotPrize(pool)
		jp.Pool = pool - jp.Won
	}
	return withJackpot(b.Result, *jp)
}

var jackpotGames = map[string]string{
	"roulette":  "в рулетке",
	"plinko":    "в Plinko",
	"blackjack": "в блэкджеке",
	"rocket":    "в рокетке",
}

func jackpotText(userID int64, game string, won int) string {
	return fmt.Sprintf("💎 <@%d> сорвал джекпот казино %s: **%s**!", userID, jackpotGames[game], economy.Points(won))
}

func announceJackpot(b bet, won int) {
	if session == nil || cfg.CivicChannelID == "" {
		return
	}
	if _, err := session.ChannelMessageSend(cfg.CivicChannelID, jackpotText(b.UserID, b.Game, won)); err != nil {
		log.Printf("Джекпот: не удалось объявить срыв %s: %v", b.ID, err)
	}
}

type jackpotWin struct {
	Name   string    `json:"name"`
	Game   string    `json:"game"`
	Amount int       `json:"amount"`
	At     time.Time `json:"at"`
}

type jackpotState struct {
	Enabled   bool        `json:"enabled"`
	Pool      int         `json:"pool"`
	Divisor   float64     `json:"divisor"`
	MaxChance float64     `json:"maxChance"`
	Target    int         `json:"target"`
	Last      *jackpotWin `json:"last"`
}

func handleJackpot(w http.ResponseWriter, _ *http.Request, p player) {
	state := jackpotState{Enabled: jackpotEnabled, Divisor: jackpotDivisor, MaxChance: jackpotMaxChance, Target: jackpotTarget}
	found, err := store.FetchOne(`SELECT pool FROM jackpot WHERE guild_id = $1`, []any{p.GuildID}, &state.Pool)
	if err != nil {
		respondBet(w, nil, err, p)
		return
	}
	if !found {
		state.Pool = jackpotSeed // ставок ещё не было: пул создастся с затравкой на первой
	}
	var last jackpotWin
	found, err = store.FetchOne(`SELECT name, game, amount, created_at FROM jackpot_wins WHERE guild_id = $1
		ORDER BY created_at DESC LIMIT 1`, []any{p.GuildID}, &last.Name, &last.Game, &last.Amount, &last.At)
	if err != nil {
		respondBet(w, nil, err, p)
		return
	}
	if found {
		state.Last = &last
	}
	writeJSON(w, http.StatusOK, state)
}
