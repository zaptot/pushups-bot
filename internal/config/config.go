// Package config — настройки бота из переменных окружения.
package config

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
	_ "time/tzdata" // часовые пояса внутри бинарника, чтобы не зависеть от хостинга

	"github.com/joho/godotenv"
)

// Config читается из окружения (на Discloud) или из файла .env (локально).
type Config struct {
	Token, GuildID, DatabaseURL     string
	DotaChannelID, CivicChannelID   string
	OpenDotaKey, PubgKey, PubgShard string
	DotaCheckMinutes                int
	Location                        *time.Location
	ActivityPort, ActivityDir       string
	ClientID, ClientSecret          string
	ActivityDevUserID               int64
}

// Values — текущие настройки; Load заполняет их на месте, поэтому указатели на Values остаются верными.
var Values Config

// Load читает настройки из окружения и из файла .env (ищется вверх от текущей папки).
func Load() {
	if path := findDotEnv(); path != "" {
		_ = godotenv.Load(path)
	}
	cfg := &Values
	*cfg = Config{
		Token:         os.Getenv("DISCORD_TOKEN"),
		GuildID:       os.Getenv("GUILD_ID"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		DotaChannelID: os.Getenv("DOTA_CHANNEL_ID"),
		OpenDotaKey:   os.Getenv("OPENDOTA_API_KEY"),
		PubgKey:       os.Getenv("PUBG_API_KEY"),
		PubgShard:     envOr("PUBG_SHARD", "steam"),
		ActivityPort:  os.Getenv("ACTIVITY_PORT"),
		ActivityDir:   envOr("ACTIVITY_DIR", "activity/dist"),
		ClientID:      os.Getenv("DISCORD_CLIENT_ID"),
		ClientSecret:  os.Getenv("DISCORD_CLIENT_SECRET"),
	}
	cfg.ActivityDevUserID, _ = strconv.ParseInt(os.Getenv("ACTIVITY_DEV_USER_ID"), 10, 64)
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
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func findDotEnv() string {
	dir, err := os.Getwd()
	for err == nil {
		if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr == nil {
			return filepath.Join(dir, ".env")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
