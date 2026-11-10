// Discord-бот для учёта отжиманий и казино на фантики. Код — в internal/, здесь только запуск.
// Discloud собирает проект из корня репозитория, поэтому main.go лежит здесь.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"pushups-bot/internal/bot"
	"pushups-bot/internal/casino"
	"pushups-bot/internal/config"
	"pushups-bot/internal/store"
)

const memoryLimit = 70 << 20 // мягкий лимит для сборщика мусора: на хостинге всего 100 МБ

func main() {
	log.SetOutput(os.Stdout)
	log.SetPrefix("[GO] ") // чтобы в логах хостинга было видно, какая версия работает
	log.Print("Программа стартовала")
	debug.SetMemoryLimit(memoryLimit)
	config.Load()
	cfg := &config.Values
	if cfg.Token == "" {
		log.Fatal("Не задан DISCORD_TOKEN (см. .env.example)")
	}
	if cfg.DatabaseURL == "" {
		log.Fatal("Не задан DATABASE_URL (см. .env.example)")
	}
	log.Printf("Настройки: длина DISCORD_TOKEN %d, GUILD_ID задан: %v, DATABASE_URL задан: %v", len(cfg.Token), cfg.GuildID != "", cfg.DatabaseURL != "")

	log.Print("Подключаюсь к базе")
	pool, err := store.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Не удалось подключиться к базе: %v", err)
	}
	defer pool.Close()
	store.DB = pool
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := pool.Exec(ctx, store.Schema); err != nil {
		log.Fatalf("Не удалось подготовить базу: %v", err)
	}

	log.Print("База готова, подключаюсь к Discord")
	session, err := bot.Start()
	if err != nil {
		log.Fatal(err)
	}
	defer session.Close()
	if cfg.ActivityPort != "" {
		go casino.Serve(session)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("Завершаю работу")
}
