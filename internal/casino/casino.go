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
	stakeMax = 1000
	betIDMax = 64
)

var errBetOwner = errors.New("ставка с таким id принадлежит другому игроку")

type bet struct {
	ID              string
	GuildID, UserID int64
	Name            string // для объявления о джекпоте
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
