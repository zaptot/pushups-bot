package casino

import (
	"encoding/json"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"pushups-bot/internal/shared"
)

const (
	rocketGrowth  = 0.06
	rocketMax     = 100.0
	rocketAutoMin = 1.01
	rocketWait    = 25 * time.Second
	rocketKeep    = time.Minute
)

var (
	rocketMu     sync.Mutex
	rocketRounds = map[string]*rocketRound{}
	rocketActive = map[[2]int64]*rocketRound{}
)

type rocketRound struct {
	ID      string
	p       player
	Stake   int
	Auto    float64
	crash   float64
	started time.Time
	timer   *time.Timer
	done    chan struct{}
	settled bool
	result  json.RawMessage
	err     error
}

type rocketResult struct {
	Status     string  `json:"status"`
	Multiplier float64 `json:"multiplier"`
	Crash      float64 `json:"crash"`
	Payout     int     `json:"payout"`
}

func crashPoint(u float64) float64 {
	return min(rocketMax, max(1, math.Floor(99/(1-u))/100))
}

func rocketMultiplier(elapsed time.Duration) float64 {
	return math.Floor(math.Exp(rocketGrowth*elapsed.Seconds())*100) / 100
}

func rocketDelay(m float64) time.Duration {
	return time.Duration(math.Log(m) / rocketGrowth * float64(time.Second))
}

func rocketOutcome(crash, auto, m float64) (string, float64) {
	if auto > 0 && auto <= m {
		m = auto
	}
	if m < crash {
		return "won", m
	}
	return "crashed", 0
}

func (r *rocketRound) finish(m float64) {
	if r.settled {
		return
	}
	r.settled = true
	r.timer.Stop()
	status, mult := rocketOutcome(r.crash, r.Auto, m)
	payout := roundPayout(r.Stake, mult)
	log.Printf("Рокетка %d: ставка %d, взрыв x%.2f, %s x%.2f, выигрыш %d", r.p.UserID, r.Stake, r.crash, status, mult, payout)
	r.result, r.err = settle(bet{
		ID: r.ID, GuildID: r.p.GuildID, UserID: r.p.UserID, Game: "rocket",
		Stake: r.Stake, Multiplier: mult, Payout: payout,
		Result: rocketResult{Status: status, Multiplier: mult, Crash: r.crash, Payout: payout},
	}, false)
	delete(rocketActive, playerKey(r.p))
	close(r.done)
	time.AfterFunc(rocketKeep, func() {
		rocketMu.Lock()
		delete(rocketRounds, r.ID)
		rocketMu.Unlock()
	})
}

func handleRocketStart(w http.ResponseWriter, r *http.Request, p player) {
	var req struct {
		Stake int     `json:"stake"`
		Auto  float64 `json:"auto"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := validStake(req.Stake); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.Auto != 0 && (req.Auto < rocketAutoMin || req.Auto > rocketMax) {
		writeError(w, http.StatusUnprocessableEntity, "Автовывод — от x1.01 до x100")
		return
	}
	rocketMu.Lock()
	defer rocketMu.Unlock()
	if rocketActive[playerKey(p)] != nil {
		writeError(w, http.StatusConflict, "Твоя ракета ещё летит")
		return
	}
	if tooFast(w, p) {
		return
	}
	id := randomID(16)
	if err := hold(p, id, "stake", req.Stake); err != nil {
		respondBet(w, nil, err, p)
		return
	}
	round := &rocketRound{
		ID: id, p: p, Stake: req.Stake, Auto: math.Floor(req.Auto*100) / 100,
		crash: crashPoint(rand.Float64()), started: time.Now(), done: make(chan struct{}),
	}
	target := round.crash
	if round.Auto > 0 && round.Auto < target {
		target = round.Auto
	}
	round.timer = time.AfterFunc(rocketDelay(target), func() {
		defer shared.Protect("рокетке")
		rocketMu.Lock()
		defer rocketMu.Unlock()
		round.finish(target)
	})
	rocketRounds[round.ID] = round
	rocketActive[playerKey(p)] = round
	writeJSON(w, http.StatusOK, map[string]any{"id": round.ID, "growth": rocketGrowth, "maxMultiplier": rocketMax})
}

func rocketLookup(w http.ResponseWriter, id string, p player) *rocketRound {
	round := rocketRounds[id]
	if round != nil && round.p.UserID == p.UserID {
		return round
	}
	raw, found, err := replay(id, p.UserID)
	if !found && err == nil {
		writeError(w, http.StatusNotFound, "Раунд не найден")
		return nil
	}
	respondBet(w, raw, err, p)
	return nil
}

func handleRocketCashout(w http.ResponseWriter, r *http.Request, p player) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	rocketMu.Lock()
	defer rocketMu.Unlock()
	round := rocketLookup(w, req.ID, p)
	if round == nil {
		return
	}
	round.finish(rocketMultiplier(time.Since(round.started)))
	respondBet(w, round.result, round.err, p)
}

func handleRocketWait(w http.ResponseWriter, r *http.Request, p player) {
	rocketMu.Lock()
	round := rocketLookup(w, r.PathValue("id"), p)
	rocketMu.Unlock()
	if round == nil {
		return
	}
	select {
	case <-round.done:
		respondBet(w, round.result, round.err, p)
	case <-time.After(rocketWait):
		writeJSON(w, http.StatusOK, map[string]string{"status": "flying"})
	case <-r.Context().Done():
	}
}
