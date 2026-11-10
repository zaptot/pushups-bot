package casino

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"

	"pushups-bot/internal/economy"
	"pushups-bot/internal/store"
)

const (
	stakeMin = 10
	stakeMax = 10000
	betIDMax = 64
)

var errBetOwner = errors.New("ставка с таким id принадлежит другому игроку")

type bet struct {
	ID              string
	GuildID, UserID int64
	Game            string
	Stake           int
	Multiplier      float64
	Payout          int
	Result          any
}

func validStake(n int) error {
	if n < stakeMin || n > stakeMax {
		return fmt.Errorf("Ставка — от %d до %d поинтов", stakeMin, stakeMax)
	}
	return nil
}

func roundPayout(stake int, mult float64) int {
	x := math.Round(float64(stake)*mult*1e6) / 1e6
	n := math.Floor(x)
	if rand.Float64() < x-n {
		n++
	}
	return int(n)
}

func replay(id string, userID int64) (json.RawMessage, bool, error) {
	var owner int64
	var result []byte
	found, err := store.FetchOne(`SELECT user_id, result FROM casino_bets WHERE id = $1`, []any{id}, &owner, &result)
	if err != nil || !found {
		return nil, false, err
	}
	if owner != userID {
		return nil, true, errBetOwner
	}
	return result, true, nil
}

var errSettled = errors.New("ставка уже записана")

// stakeMove — списание ставки (ref уникален, повтор не спишет дважды).
func stakeMove(id, ref string, stake int) economy.Move {
	return economy.Move{Amount: -stake, Ref: ref + ":" + id, BetID: id, Reason: "ставка в казино"}
}

// hold списывает ставку при начале партии, итог которой будет позже (блэкджек, рокетка).
func hold(p player, id, ref string, stake int) error {
	_, err := economy.Apply(p.GuildID, p.UserID, nil, stakeMove(id, ref, stake))
	return err
}

// settle записывает итог ставки и начисляет выигрыш одной транзакцией.
// debit — списать ставку здесь же (для игр в один ход; у блэкджека и рокетки она списана в hold).
func settle(b bet, debit bool) (json.RawMessage, error) {
	result, err := json.Marshal(b.Result)
	if err != nil {
		return nil, err
	}
	var moves []economy.Move
	if debit {
		moves = append(moves, stakeMove(b.ID, "stake", b.Stake))
	}
	moves = append(moves, economy.Move{Amount: b.Payout, Ref: "win:" + b.ID, BetID: b.ID, Reason: "выигрыш в казино"})
	_, err = economy.ApplyInsert(b.GuildID, b.UserID, economy.Insert{
		SQL: `INSERT INTO casino_bets (id, guild_id, user_id, game, stake, multiplier, payout, result)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb) ON CONFLICT (id) DO NOTHING`,
		Args:     []any{b.ID, b.GuildID, b.UserID, b.Game, b.Stake, b.Multiplier, b.Payout, string(result)},
		Conflict: errSettled,
	}, moves...)
	// ставка с этим id уже записана (повтор запроса): отдаём сохранённый итог, ничего не списав
	if errors.Is(err, errSettled) {
		stored, _, err := replay(b.ID, b.UserID)
		return stored, err
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}
