package main

import (
	"log"
	"math"
	"math/rand/v2"
	"net/http"
)

const (
	plinkoRowsMin = 8
	plinkoRowsMax = 16
	plinkoRTP     = 0.99
)

var (
	plinkoGrowth = map[string]float64{"low": 1.45, "medium": 1.9, "high": 2.6}
	plinkoTables = buildPlinkoTables()
)

type plinkoRequest struct {
	ID    string `json:"id"`
	Stake int    `json:"stake"`
	Rows  int    `json:"rows"`
	Risk  string `json:"risk"`
}

type plinkoResult struct {
	Rows       int     `json:"rows"`
	Risk       string  `json:"risk"`
	Path       []int   `json:"path"`
	Slot       int     `json:"slot"`
	Multiplier float64 `json:"multiplier"`
	Payout     int     `json:"payout"`
}

func plinkoOdds(n int) []float64 {
	odds := make([]float64, n+1)
	c := 1.0
	for k := range n + 1 {
		odds[k] = c / math.Pow(2, float64(n))
		c = c * float64(n-k) / float64(k+1)
	}
	return odds
}

func buildPlinkoTables() map[int]map[string][]float64 {
	out := map[int]map[string][]float64{}
	for n := plinkoRowsMin; n <= plinkoRowsMax; n++ {
		odds := plinkoOdds(n)
		out[n] = map[string][]float64{}
		for risk, g := range plinkoGrowth {
			table := make([]float64, n+1)
			ev := 0.0
			for k := range table {
				table[k] = math.Pow(g, math.Abs(float64(k)-float64(n)/2))
				ev += odds[k] * table[k]
			}
			for k := range table {
				table[k] = math.Round(table[k]*plinkoRTP/ev*100) / 100
			}
			out[n][risk] = table
		}
	}
	return out
}

func plinkoDrop(rows int) ([]int, int) {
	path := make([]int, rows)
	slot := 0
	for i := range path {
		path[i] = rand.IntN(2)
		slot += path[i]
	}
	return path, slot
}

func handlePlinkoTables(w http.ResponseWriter, _ *http.Request, _ player) {
	writeJSON(w, http.StatusOK, plinkoTables)
}

func handlePlinko(w http.ResponseWriter, r *http.Request, p player) {
	var req plinkoRequest
	if !readJSON(w, r, &req) || replayed(w, req.ID, p) {
		return
	}
	if err := validStake(req.Stake); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	table, ok := plinkoTables[req.Rows][req.Risk]
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "Неверные ряды или риск")
		return
	}
	path, slot := plinkoDrop(req.Rows)
	mult := table[slot]
	payout := roundPayout(req.Stake, mult)
	log.Printf("Plinko %d: %d, %d рядов, %s, лунка %d (x%.2f), засчитано %d", p.UserID, req.Stake, req.Rows, req.Risk, slot, mult, payout)
	raw, err := settle(bet{
		ID: req.ID, GuildID: p.GuildID, UserID: p.UserID, Game: "plinko",
		Stake: req.Stake, Multiplier: mult, Payout: payout,
		Result: plinkoResult{Rows: req.Rows, Risk: req.Risk, Path: path, Slot: slot, Multiplier: mult, Payout: payout},
	})
	respondBet(w, raw, err, p)
}
