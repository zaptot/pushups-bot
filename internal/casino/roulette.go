package casino

import (
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
)

var (
	redNumbers = map[int]bool{1: true, 3: true, 5: true, 7: true, 9: true, 12: true, 14: true, 16: true, 18: true,
		19: true, 21: true, 23: true, 25: true, 27: true, 30: true, 32: true, 34: true, 36: true}
	errBadBet = errors.New("Неверная ставка")
)

type rouletteRequest struct {
	ID    string `json:"id"`
	Stake int    `json:"stake"`
	Bet   struct {
		Kind  string `json:"kind"`
		Value int    `json:"value"`
	} `json:"bet"`
}

type rouletteResult struct {
	Number     int     `json:"number"`
	Multiplier float64 `json:"multiplier"`
	Payout     int     `json:"payout"`
}

func rouletteMultiplier(kind string, value, n int) (float64, error) {
	var win bool
	pay := 2.0
	switch kind {
	case "red":
		win = redNumbers[n]
	case "black":
		win = n != 0 && !redNumbers[n]
	case "even":
		win = n != 0 && n%2 == 0
	case "odd":
		win = n%2 == 1
	case "low":
		win = n >= 1 && n <= 18
	case "high":
		win = n >= 19
	case "dozen":
		if value < 1 || value > 3 {
			return 0, errBadBet
		}
		pay, win = 3, n != 0 && (n-1)/12+1 == value
	case "number":
		if value < 0 || value > 36 {
			return 0, errBadBet
		}
		pay, win = 36, n == value
	default:
		return 0, errBadBet
	}
	if !win {
		return 0, nil
	}
	return pay, nil
}

func handleRoulette(w http.ResponseWriter, r *http.Request, p player) {
	var req rouletteRequest
	if !readJSON(w, r, &req) || badBetID(w, req.ID) {
		return
	}
	if err := validStake(req.Stake); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	n := rand.IntN(37)
	mult, err := rouletteMultiplier(req.Bet.Kind, req.Bet.Value, n)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if tooFastBet(w, req.ID, p) {
		return
	}
	payout := roundPayout(req.Stake, mult)
	log.Printf("Рулетка %d: %d на %s %d, выпало %d, выигрыш %d", p.UserID, req.Stake, req.Bet.Kind, req.Bet.Value, n, payout)
	raw, err := settle(bet{
		ID: req.ID, GuildID: p.GuildID, UserID: p.UserID, Name: p.Name, Game: "roulette",
		Stake: req.Stake, Multiplier: mult, Payout: payout,
		Result: rouletteResult{Number: n, Multiplier: mult, Payout: payout},
	}, true)
	respondBet(w, raw, err, p)
}
