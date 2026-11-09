// Discord-бот для учёта отжиманий: споры, матчи Dota 2 и PUBG, гражданство, суд, законы и награды.
// Данные — в Postgres (Supabase).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	memoryLimit = 70 << 20 // мягкий лимит для сборщика мусора: на хостинге всего 100 МБ
)

var (
	startLoops       sync.Once
	dotaRequestPause = time.Second
)

// every запускает fn сразу и затем с заданным интервалом; паника не роняет бота.
func every(interval time.Duration, name string, fn func()) {
	run := func() {
		defer protect(name)
		fn()
	}
	go func() {
		run()
		for range time.Tick(interval) {
			run()
		}
	}()
}

var (
	vmRSS = regexp.MustCompile(`VmRSS:\s+(\d+) kB`)
	vmHWM = regexp.MustCompile(`VmHWM:\s+(\d+) kB`)
)

// memoryUsage читает текущую и пиковую память процесса (только Linux).
func memoryUsage() string {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "неизвестно"
	}
	mb := func(re *regexp.Regexp) string {
		m := re.FindSubmatch(data)
		if m == nil {
			return "?"
		}
		var kb int
		fmt.Sscan(string(m[1]), &kb)
		return fmt.Sprint(kb / 1024)
	}
	return mb(vmRSS) + " МБ, пик " + mb(vmHWM) + " МБ"
}

func onReady(s *discordgo.Session, ready *discordgo.Ready) {
	log.Printf("Запущен как %s (id %s), память %s", ready.User.Username, ready.User.ID, memoryUsage())
	startLoops.Do(func() {
		every(time.Duration(cfg.DotaCheckMinutes)*time.Minute, "автопроверке Dota", dotaLoop)
		every(time.Duration(cfg.DotaCheckMinutes)*time.Minute, "автопроверке PUBG", pubgLoop)
		every(time.Minute, "подведении итогов", pollsLoop)
		every(time.Hour, "ролях и наградах", civicLoop)
	})
}

func main() {
	debug.SetMemoryLimit(memoryLimit)
	loadConfig()
	if cfg.Token == "" {
		log.Fatal("Не задан DISCORD_TOKEN (см. .env.example)")
	}
	if cfg.DatabaseURL == "" {
		log.Fatal("Не задан DATABASE_URL (см. .env.example)")
	}

	pool, err := connectDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Не удалось подключиться к базе: %v", err)
	}
	defer pool.Close()
	db = pool
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := pool.Exec(ctx, schema); err != nil {
		log.Fatalf("Не удалось подготовить базу: %v", err)
	}

	session, err = discordgo.New("Bot " + cfg.Token)
	if err != nil {
		log.Fatalf("Не удалось создать сессию Discord: %v", err)
	}
	// боту нужны только серверы и каналы: команды и кнопки приходят с полными данными участника
	session.Identify.Intents = discordgo.IntentsGuilds
	session.Client = newHTTPClient()
	session.State.MaxMessageCount = 0
	session.State.TrackEmojis = false
	session.State.TrackMembers = false
	session.State.TrackThreadMembers = false
	session.State.TrackVoice = false
	session.State.TrackPresences = false
	session.State.TrackThreads = false
	session.AddHandler(onReady)
	session.AddHandler(onInteraction)

	commands := commandList()
	defs := make([]*discordgo.ApplicationCommand, 0, len(commands))
	for _, c := range commands {
		handlers[c.def.Name] = c.handler
		defs = append(defs, c.def)
	}
	if err := session.Open(); err != nil {
		log.Fatalf("Не удалось подключиться к Discord: %v", err)
	}
	defer session.Close()
	botUserID = parseID(session.State.User.ID)
	// без GUILD_ID команды регистрируются глобально, это может занять до часа
	if _, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, cfg.GuildID, defs); err != nil {
		log.Fatalf("Не удалось зарегистрировать команды: %v", err)
	}
	if cfg.ActivityPort != "" {
		go serveActivity()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("Завершаю работу")
}
