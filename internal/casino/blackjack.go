package casino

import (
	"encoding/json"
	"errors"
	"log"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"pushups-bot/internal/shared"
)

const (
	blackjackIdle   = 2 * time.Minute
	blackjackDecks  = 6
	blackjackHidden = "??"
	blackjackRanks  = "A23456789TJQK"
	blackjackSuits  = "shdc"
)

var (
	errBadAction   = errors.New("Так сейчас нельзя")
	blackjackMu    sync.Mutex
	blackjackHands = map[[2]int64]*blackjackHand{}
)

type card struct{ Rank, Suit int }

func (c card) String() string {
	return string(blackjackRanks[c.Rank-1]) + string(blackjackSuits[c.Suit])
}

type blackjackHand struct {
	ID             string
	p              player
	Stake          int
	deck           []card
	Player, Dealer []card
	Seq            int
	Status         string
	Mult           float64
	Payout         int
	timer          *time.Timer
}

type blackjackView struct {
	ID          string   `json:"id"`
	Seq         int      `json:"seq"`
	Stake       int      `json:"stake"`
	Player      []string `json:"player"`
	Dealer      []string `json:"dealer"`
	PlayerTotal int      `json:"playerTotal"`
	DealerTotal int      `json:"dealerTotal"`
	Status      string   `json:"status"`
	CanDouble   bool     `json:"canDouble"`
	Multiplier  float64  `json:"multiplier"`
	Payout      int      `json:"payout"`
}

func shuffledDeck() []card {
	deck := make([]card, 0, 52*blackjackDecks)
	for range blackjackDecks {
		for suit := range 4 {
			for rank := 1; rank <= 13; rank++ {
				deck = append(deck, card{rank, suit})
			}
		}
	}
	rand.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })
	return deck
}

func handTotal(cards []card) (int, bool) {
	total, aces := 0, false
	for _, c := range cards {
		total += min(c.Rank, 10)
		aces = aces || c.Rank == 1
	}
	if aces && total+10 <= 21 {
		return total + 10, true
	}
	return total, false
}

func isNatural(cards []card) bool {
	t, _ := handTotal(cards)
	return len(cards) == 2 && t == 21
}

func newBlackjackHand(id string, p player, stake int, deck []card) *blackjackHand {
	h := &blackjackHand{ID: id, p: p, Stake: stake, deck: deck, Status: "playing"}
	h.Player = append(h.Player, h.draw())
	h.Dealer = append(h.Dealer, h.draw())
	h.Player = append(h.Player, h.draw())
	h.Dealer = append(h.Dealer, h.draw())
	if isNatural(h.Player) || isNatural(h.Dealer) {
		h.resolve()
	}
	return h
}

func (h *blackjackHand) draw() card {
	c := h.deck[0]
	h.deck = h.deck[1:]
	return c
}

func (h *blackjackHand) canDouble() bool {
	if h.Status != "playing" || len(h.Player) != 2 || h.Stake*2 > stakeMax {
		return false
	}
	t, _ := handTotal(h.Player)
	return t >= 9 && t <= 11
}

func (h *blackjackHand) act(action string) error {
	if h.Status != "playing" {
		return errBadAction
	}
	switch action {
	case "hit":
		h.Player = append(h.Player, h.draw())
		if t, _ := handTotal(h.Player); t >= 21 {
			h.resolve()
		}
	case "stand":
		h.resolve()
	case "double":
		if !h.canDouble() {
			return errBadAction
		}
		h.Stake *= 2
		h.Player = append(h.Player, h.draw())
		h.resolve()
	default:
		return errBadAction
	}
	h.Seq++
	return nil
}

func (h *blackjackHand) resolve() {
	pt, _ := handTotal(h.Player)
	pn, dn := isNatural(h.Player), isNatural(h.Dealer)
	if pt > 21 {
		h.Status, h.Mult = "bust", 0
		return
	}
	if !pn && !dn {
		for {
			if dt, soft := handTotal(h.Dealer); dt > 17 || (dt == 17 && !soft) {
				break
			}
			h.Dealer = append(h.Dealer, h.draw())
		}
	}
	dt, _ := handTotal(h.Dealer)
	switch {
	case pn && dn, !pn && !dn && pt == dt:
		h.Status, h.Mult = "push", 1
	case pn:
		h.Status, h.Mult = "blackjack", 2.2
	case dn, dt <= 21 && dt > pt:
		h.Status, h.Mult = "lose", 0
	default:
		h.Status, h.Mult = "win", 2
	}
}

func (h *blackjackHand) view() blackjackView {
	v := blackjackView{
		ID: h.ID, Seq: h.Seq, Stake: h.Stake, Status: h.Status, CanDouble: h.canDouble(),
		Multiplier: h.Mult, Payout: h.Payout,
	}
	for _, c := range h.Player {
		v.Player = append(v.Player, c.String())
	}
	v.PlayerTotal, _ = handTotal(h.Player)
	dealer := h.Dealer
	if h.Status == "playing" {
		dealer = h.Dealer[:1]
	}
	for _, c := range dealer {
		v.Dealer = append(v.Dealer, c.String())
	}
	v.DealerTotal, _ = handTotal(dealer)
	if h.Status == "playing" {
		v.Dealer = append(v.Dealer, blackjackHidden)
	}
	return v
}

func blackjackStep(h *blackjackHand) (json.RawMessage, error) {
	if h.Status == "playing" {
		h.timer.Reset(blackjackIdle)
		return json.Marshal(h.view())
	}
	h.timer.Stop()
	delete(blackjackHands, playerKey(h.p))
	h.Payout = roundPayout(h.Stake, h.Mult)
	log.Printf("Блэкджек %d: ставка %d, %s, выигрыш %d", h.p.UserID, h.Stake, h.Status, h.Payout)
	return settle(bet{
		ID: h.ID, GuildID: h.p.GuildID, UserID: h.p.UserID, Name: h.p.Name, Game: "blackjack",
		Stake: h.Stake, Multiplier: h.Mult, Payout: h.Payout, Result: h.view(),
	}, false)
}

func blackjackTimeout(key [2]int64, id string) {
	defer shared.Protect("автозавершении блэкджека")
	blackjackMu.Lock()
	defer blackjackMu.Unlock()
	h := blackjackHands[key]
	if h == nil || h.ID != id {
		return
	}
	if err := h.act("stand"); err != nil {
		log.Printf("Блэкджек %d: не удалось завершить по таймеру: %v", h.p.UserID, err)
		return
	}
	if _, err := blackjackStep(h); err != nil {
		log.Printf("Блэкджек %d: не удалось записать партию по таймеру: %v", h.p.UserID, err)
	}
}

func handleBlackjackState(w http.ResponseWriter, _ *http.Request, p player) {
	blackjackMu.Lock()
	defer blackjackMu.Unlock()
	h := blackjackHands[playerKey(p)]
	if h == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	raw, err := json.Marshal(h.view())
	respondBet(w, raw, err, p)
}

func handleBlackjackStart(w http.ResponseWriter, r *http.Request, p player) {
	var req struct {
		Stake int `json:"stake"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := validStake(req.Stake); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	key := playerKey(p)
	blackjackMu.Lock()
	defer blackjackMu.Unlock()
	if blackjackHands[key] != nil {
		writeError(w, http.StatusConflict, "Сначала доиграй текущую партию")
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
	h := newBlackjackHand(id, p, req.Stake, shuffledDeck())
	h.timer = time.AfterFunc(blackjackIdle, func() { blackjackTimeout(key, h.ID) })
	if h.Status == "playing" {
		blackjackHands[key] = h
	}
	raw, err := blackjackStep(h)
	respondBet(w, raw, err, p)
}

func handleBlackjackAction(w http.ResponseWriter, r *http.Request, p player) {
	var req struct {
		ID     string `json:"id"`
		Seq    int    `json:"seq"`
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	blackjackMu.Lock()
	defer blackjackMu.Unlock()
	h := blackjackHands[playerKey(p)]
	if h == nil || h.ID != req.ID {
		raw, found, err := replay(req.ID, p.UserID)
		if !found && err == nil {
			writeError(w, http.StatusNotFound, "Партия не найдена, начни новую")
			return
		}
		respondBet(w, raw, err, p)
		return
	}
	if req.Seq != h.Seq {
		raw, err := json.Marshal(h.view())
		respondBet(w, raw, err, p)
		return
	}
	if req.Action == "double" && h.canDouble() {
		if err := hold(p, h.ID, "stake2", h.Stake); err != nil {
			respondBet(w, nil, err, p)
			return
		}
	}
	if err := h.act(req.Action); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	raw, err := blackjackStep(h)
	respondBet(w, raw, err, p)
}
