// Package bot — Discord-бот: отжимания, матчи Dota 2 и PUBG, гражданство, суд, законы и роли.
package bot

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"pushups-bot/internal/shared"
)

var (
	startLoops       sync.Once
	dotaRequestPause = time.Second
)

// every запускает fn сразу и затем с заданным интервалом; паника не роняет бота.
func every(interval time.Duration, name string, fn func()) {
	run := func() {
		defer shared.Protect(name)
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
		every(time.Hour, "ролях по итогам игр", civicLoop)
	})
}

// Start подключает бота к Discord, регистрирует команды и запускает фоновые проверки.
// База (store.DB) должна быть уже подключена.
func Start() (*discordgo.Session, error) {
	pubgBase = "https://api.pubg.com/shards/" + cfg.PubgShard
	var err error
	session, err = discordgo.New("Bot " + cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("не удалось создать сессию Discord: %w", err)
	}
	// боту нужны только серверы и каналы: команды и кнопки приходят с полными данными участника
	session.Identify.Intents = discordgo.IntentsGuilds
	session.Client = shared.NewHTTPClient()
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
		return nil, fmt.Errorf("не удалось подключиться к Discord: %w", err)
	}
	botUserID = shared.ParseID(session.State.User.ID)
	// без GUILD_ID команды регистрируются глобально, это может занять до часа
	if _, err := session.ApplicationCommandBulkOverwrite(session.State.User.ID, cfg.GuildID, defs); err != nil {
		session.Close()
		return nil, fmt.Errorf("не удалось зарегистрировать команды: %w", err)
	}
	return session, nil
}
