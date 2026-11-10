// Package shared — мелкие общие функции бота и казино.
package shared

import (
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
)

func ParseID(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func IDStr(v int64) string { return strconv.FormatInt(v, 10) }

func OrEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func DisplayName(m *discordgo.Member, u *discordgo.User) string {
	if m.Nick != "" {
		return m.Nick
	}
	if u != nil {
		return u.DisplayName()
	}
	return ""
}

// Protect не даёт панике в обработчике уронить весь бот. Вызывать только через defer.
func Protect(name string) {
	if v := recover(); v != nil {
		log.Printf("Паника в %s: %v\n%s", name, v, debug.Stack())
	}
}

func NewHTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
