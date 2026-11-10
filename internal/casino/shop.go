package casino

import (
	"errors"
	"log"
	"net/http"

	"pushups-bot/internal/economy"
)

type shopItem struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Price int    `json:"price"`
}

func handleBalance(w http.ResponseWriter, _ *http.Request, p player) {
	bal, err := economy.Balance(p.GuildID, p.UserID)
	if err != nil {
		respondBet(w, nil, err, p)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"balance": bal})
}

func handleShop(w http.ResponseWriter, _ *http.Request, _ player) {
	items := make([]shopItem, 0, len(economy.Items))
	for _, it := range economy.Items {
		items = append(items, shopItem{it.Key, it.Title, it.Price})
	}
	writeJSON(w, http.StatusOK, items)
}

func handleBuy(w http.ResponseWriter, r *http.Request, p player) {
	var req struct {
		Item string `json:"item"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	item, ok := economy.FindItem(req.Item)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "Такого товара нет")
		return
	}
	if tooFast(w, p) {
		return
	}
	orderID, bal, err := economy.Buy(p.GuildID, p.UserID, item)
	if errors.Is(err, economy.ErrNotEnough) {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err != nil {
		respondBet(w, nil, err, p)
		return
	}
	log.Printf("Магазин: %d купил %s, заказ %d", p.UserID, item.Key, orderID)
	if session != nil && cfg.CivicChannelID != "" {
		if _, err := session.ChannelMessageSend(cfg.CivicChannelID, economy.OrderText(p.UserID, orderID, item, bal)); err != nil {
			log.Printf("Магазин: не удалось объявить заказ %d: %v", orderID, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"balance": bal, "order": orderID})
}
