package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
	_ "time/tzdata" // часовые пояса внутри бинарника, чтобы не зависеть от хостинга

	"github.com/joho/godotenv"
)

const (
	windowDays = 365 // период для лидербордов и остатков
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
	awardMinGames     = 10 // для ролей и наград нужно больше стольких игр за период
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
	months = []string{"январь", "февраль", "март", "апрель", "май", "июнь",
		"июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"}

	pubgMatchTypes = map[string]bool{"official": true, "competitive": true} // обычные и рейтинговые

	opendotaBase = "https://api.opendota.com/api"
	pubgBase     string
)

// config читается из окружения (на Discloud) или из файла .env (локально).
type config struct {
	Token, GuildID, DatabaseURL     string
	DotaChannelID, CivicChannelID   string
	OpenDotaKey, PubgKey, PubgShard string
	DotaCheckMinutes                int
	Location                        *time.Location
}

var cfg config

func loadConfig() {
	_ = godotenv.Load()
	cfg = config{
		Token:         os.Getenv("DISCORD_TOKEN"),
		GuildID:       os.Getenv("GUILD_ID"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		DotaChannelID: os.Getenv("DOTA_CHANNEL_ID"),
		OpenDotaKey:   os.Getenv("OPENDOTA_API_KEY"),
		PubgKey:       os.Getenv("PUBG_API_KEY"),
		PubgShard:     envOr("PUBG_SHARD", "steam"),
	}
	cfg.CivicChannelID = envOr("CIVIC_CHANNEL_ID", cfg.DotaChannelID)
	cfg.DotaCheckMinutes, _ = strconv.Atoi(envOr("DOTA_CHECK_MINUTES", "30"))
	if cfg.DotaCheckMinutes <= 0 {
		cfg.DotaCheckMinutes = 30
	}
	loc, err := time.LoadLocation(envOr("TIMEZONE", "Europe/Berlin"))
	if err != nil {
		log.Fatalf("Неверный TIMEZONE: %v", err)
	}
	cfg.Location = loc
	pubgBase = "https://api.pubg.com/shards/" + cfg.PubgShard
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
