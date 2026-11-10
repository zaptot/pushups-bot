package bot

import (
	"fmt"
	"time"

	"pushups-bot/internal/config"
	"pushups-bot/internal/store"
)

const (
	windowDays = store.WindowDays
	topSize    = 15
	doneMin    = 10 // меньше за раз записать через /done нельзя

	// Dota 2
	minMatchSeconds = 10 * 60       // более короткие матчи (ремейки, ливы) не считаются
	partyMinLinked  = 2             // матч считается, только если в нём минимум столько привязанных игроков
	soloDelay       = 2 * time.Hour // соло-матч засчитывается не раньше, чем через столько после его конца
	steam64Base     = 76561197960265728

	// PUBG
	pubgSafePoints = 300 // набрал столько очков — без отжиманий
	pubgPushups    = 10

	// государство
	citizenMaxDebt    = 100 // чтобы стать гражданином, долг должен быть меньше
	freezeDebt        = 300 // при долге больше — привилегии гражданина заморожены
	vouchesNeeded     = 2
	citizenMinDays    = 14 // ручаться можно только за тех, кто на сервере не меньше стольких дней
	presidentTermDays = 30 // минимальный срок президента до перевыборов
	rankMinGames      = 10 // для ролей по итогам нужно больше стольких игр за период
	lawHours          = 24
	electionHours     = 24 // и выдвижение, и голосование
)

var (
	roleNames = map[string]string{
		"citizen":   "Гражданин",
		"president": "Президент",
		"ruiner":    "Топ руинер",
		"decent":    "Порядочный гражданин",
		"marginal":  "Маргинал",
	}
	constitution = []string{
		"Каждый гражданин обязан делать отжимания.",
		fmt.Sprintf("Если у человека накопилось более %d отжиманий, все привилегии гражданства теряются "+
			"(замораживаются, пока долг не станет меньше).", freezeDebt),
		"Президента нельзя забанить.",
	}

	pubgMatchTypes = map[string]bool{"official": true, "competitive": true} // обычные и рейтинговые

	opendotaBase = "https://api.opendota.com/api"
	pubgBase     string // задаётся в Start по PUBG_SHARD
)

// cfg — настройки из окружения, заполняются config.Load.
var cfg = &config.Values
