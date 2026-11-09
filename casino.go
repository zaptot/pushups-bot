package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
)

const (
	stakeMin = doneMin
	stakeMax = 10000
	betIDMax = 64
)

var (
	gameTitles = map[string]string{
		"roulette":  "рулетка",
		"plinko":    "plinko",
		"blackjack": "блэкджек",
		"rocket":    "рокетка",
	}
	errBetOwner = errors.New("ставка с таким id принадлежит другому игроку")
)

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
		return fmt.Errorf("Ставка — от %d до %d отжиманий", stakeMin, stakeMax)
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
	found, err := fetchOne(`SELECT user_id, result FROM casino_bets WHERE id = $1`, []any{id}, &owner, &result)
	if err != nil || !found {
		return nil, false, err
	}
	if owner != userID {
		return nil, true, errBetOwner
	}
	return result, true, nil
}

func settle(b bet) (json.RawMessage, error) {
	result, err := json.Marshal(b.Result)
	if err != nil {
		return nil, err
	}
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO casino_bets (id, guild_id, user_id, game, stake, multiplier, payout, result)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
		b.ID, b.GuildID, b.UserID, b.Game, b.Stake, b.Multiplier, b.Payout, result)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if err := tx.Rollback(ctx); err != nil {
			return nil, err
		}
		stored, _, err := replay(b.ID, b.UserID)
		return stored, err
	}
	reason := fmt.Sprintf("🎰 лудомания: %d × %.2f · %s", b.Stake, b.Multiplier, gameTitles[b.Game])
	if _, err := tx.Exec(ctx, `INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source)
		VALUES ($1, $2, 'done', $3, $2, $4, 'manual')`, b.GuildID, b.UserID, b.Payout, reason); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
