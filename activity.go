package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	playerTTL    = 24 * time.Hour
	requestLimit = 1 << 16
)

type player struct {
	UserID, GuildID int64
	Name            string
	Expires         time.Time
}

var (
	playersMu sync.Mutex
	players   = map[string]player{}
)

type playerHandler func(http.ResponseWriter, *http.Request, player)

func activityMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", handleConfig)
	mux.HandleFunc("POST /api/session", handleSession)
	mux.HandleFunc("POST /api/roulette", auth(handleRoulette))
	mux.HandleFunc("GET /api/plinko/tables", auth(handlePlinkoTables))
	mux.HandleFunc("POST /api/plinko", auth(handlePlinko))
	mux.HandleFunc("GET /api/blackjack", auth(handleBlackjackState))
	mux.HandleFunc("POST /api/blackjack", auth(handleBlackjackStart))
	mux.HandleFunc("POST /api/blackjack/action", auth(handleBlackjackAction))
	mux.HandleFunc("POST /api/rocket", auth(handleRocketStart))
	mux.HandleFunc("POST /api/rocket/cashout", auth(handleRocketCashout))
	mux.HandleFunc("GET /api/rocket/{id}", auth(handleRocketWait))
	if st, err := os.Stat(cfg.ActivityDir); err == nil && st.IsDir() {
		mux.Handle("/", http.FileServer(http.Dir(cfg.ActivityDir)))
	}
	return mux
}

func serveActivity() {
	defer protect("казино")
	srv := &http.Server{Addr: ":" + cfg.ActivityPort, Handler: activityMux(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("Казино: порт %s, режим разработки %v", cfg.ActivityPort, cfg.ActivityDevUserID != 0)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("Казино остановлено: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("Казино: не удалось отправить ответ: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, requestLimit)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "Неверный запрос")
		return false
	}
	return true
}

func randomID(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

func newPlayer(p player) string {
	token := randomID(32)
	now := time.Now()
	p.Expires = now.Add(playerTTL)
	playersMu.Lock()
	for t, old := range players {
		if now.After(old.Expires) {
			delete(players, t)
		}
	}
	players[token] = p
	playersMu.Unlock()
	return token
}

func playerKey(p player) [2]int64 { return [2]int64{p.GuildID, p.UserID} }

func auth(h playerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer protect("обработчике казино")
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		playersMu.Lock()
		p, ok := players[token]
		playersMu.Unlock()
		if !ok || time.Now().After(p.Expires) {
			writeError(w, http.StatusUnauthorized, "Сессия устарела, открой казино заново")
			return
		}
		h(w, r, p)
	}
}

func activityClientID() string {
	if cfg.ClientID == "" && session != nil && session.State.User != nil {
		return session.State.User.ID
	}
	return cfg.ClientID
}

func handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"clientId": activityClientID(), "dev": cfg.ActivityDevUserID != 0})
}

func handleSession(w http.ResponseWriter, r *http.Request) {
	defer protect("входе в казино")
	var req struct{ Code, GuildID string }
	if r.ContentLength != 0 && !readJSON(w, r, &req) {
		return
	}
	var p player
	if req.Code == "" {
		if cfg.ActivityDevUserID == 0 {
			writeError(w, http.StatusUnauthorized, "Открой казино из Discord")
			return
		}
		p = player{UserID: cfg.ActivityDevUserID, GuildID: parseID(cfg.GuildID), Name: "Тестовый игрок"}
		if p.GuildID == 0 {
			writeError(w, http.StatusInternalServerError, "Для режима разработки задай GUILD_ID")
			return
		}
	} else {
		var status int
		var err error
		if p, status, err = discordPlayer(req.Code, req.GuildID); err != nil {
			log.Printf("Казино: вход не удался: %v", err)
			writeError(w, status, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": newPlayer(p),
		"user":  map[string]string{"id": idStr(p.UserID), "name": p.Name},
	})
}

func discordPlayer(code, guild string) (player, int, error) {
	clientID := activityClientID()
	if cfg.ClientSecret == "" || clientID == "" || session == nil {
		return player{}, http.StatusUnauthorized, errors.New("Вход через Discord пока не настроен")
	}
	guildID := parseID(guild)
	if guildID == 0 {
		return player{}, http.StatusForbidden, errors.New("Казино работает только на сервере")
	}
	client := newHTTPClient()
	resp, err := client.PostForm("https://discord.com/api/oauth2/token", url.Values{
		"client_id":     {clientID},
		"client_secret": {cfg.ClientSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
	})
	if err != nil {
		return player{}, http.StatusBadGateway, errors.New("Discord не отвечает, попробуй ещё раз")
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(resp.Body).Decode(&token)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || token.AccessToken == "" {
		return player{}, http.StatusUnauthorized, errors.New("Discord не подтвердил вход")
	}
	req, _ := http.NewRequest(http.MethodGet, "https://discord.com/api/users/@me", nil)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err = client.Do(req)
	if err != nil {
		return player{}, http.StatusBadGateway, errors.New("Discord не отвечает, попробуй ещё раз")
	}
	var user struct{ ID string }
	err = json.NewDecoder(resp.Body).Decode(&user)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || parseID(user.ID) == 0 {
		return player{}, http.StatusUnauthorized, errors.New("Discord не подтвердил вход")
	}
	m, err := session.GuildMember(guild, user.ID)
	if err != nil {
		return player{}, http.StatusForbidden, errors.New("Ты не участник этого сервера")
	}
	return player{UserID: parseID(user.ID), GuildID: guildID, Name: orEmpty(displayName(m, m.User), user.ID)}, 0, nil
}

func replayed(w http.ResponseWriter, id string, p player) bool {
	if id == "" || len(id) > betIDMax {
		writeError(w, http.StatusUnprocessableEntity, "Неверный id ставки")
		return true
	}
	raw, found, err := replay(id, p.UserID)
	if errors.Is(err, errBetOwner) {
		writeError(w, http.StatusConflict, "Эта ставка уже занята")
		return true
	}
	if err != nil || found {
		respondBet(w, raw, err, p)
		return true
	}
	return false
}

func respondBet(w http.ResponseWriter, raw json.RawMessage, err error, p player) {
	if err != nil {
		log.Printf("Казино: ошибка ставки %d: %v", p.UserID, err)
		writeError(w, http.StatusInternalServerError, "Что-то пошло не так, попробуй ещё раз")
		return
	}
	writeJSON(w, http.StatusOK, raw)
}
