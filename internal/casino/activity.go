package casino

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"pushups-bot/internal/config"
	"pushups-bot/internal/economy"
	"pushups-bot/internal/shared"
	"pushups-bot/internal/store"
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

	betInterval = time.Second
	lastBetMu   sync.Mutex
	lastBet     = map[[2]int64]time.Time{}
)

type playerHandler func(http.ResponseWriter, *http.Request, player)

const notCitizen = "Играть могут только граждане"

// isCitizen проверяет гражданство по базе бота; в тестах без базы подменяется.
var isCitizen = func(p player) (bool, error) {
	var one int
	return store.FetchOne("SELECT 1 FROM citizens WHERE guild_id = $1 AND user_id = $2", []any{p.GuildID, p.UserID}, &one)
}

// citizenTTL — сколько помнить подтверждённое гражданство. Запрос в базу на каждую кнопку — заметная
// задержка, а отнятое гражданство перестаёт пускать в казино не позже чем через это время.
// Отказ не запоминается: только что принятый гражданин может играть сразу.
const citizenTTL = 30 * time.Second

var (
	citizensMu   sync.Mutex
	citizensSeen = map[[2]int64]time.Time{}
)

func citizenCached(p player) (bool, error) {
	key := playerKey(p)
	now := time.Now()
	citizensMu.Lock()
	seen, ok := citizensSeen[key]
	citizensMu.Unlock()
	if ok && now.Sub(seen) < citizenTTL {
		return true, nil
	}
	citizen, err := isCitizen(p)
	citizensMu.Lock()
	defer citizensMu.Unlock()
	if citizen && err == nil {
		citizensSeen[key] = now
	} else {
		delete(citizensSeen, key)
	}
	return citizen, err
}

func forgetCitizens() {
	citizensMu.Lock()
	clear(citizensSeen)
	citizensMu.Unlock()
}

func activityMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", handleConfig)
	mux.HandleFunc("POST /api/session", handleSession)
	mux.HandleFunc("GET /api/balance", auth(handleBalance))
	mux.HandleFunc("GET /api/shop", auth(handleShop))
	mux.HandleFunc("POST /api/shop/buy", auth(handleBuy))
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
	} else if cfg.ActivityPort != "" {
		log.Printf("Казино: нет папки страницы %s, открыт только API", cfg.ActivityDir)
	}
	return mux
}

const discordFrameAncestors = "frame-ancestors https://discord.com https://*.discord.com https://*.discordsays.com"

func allowDiscordFrame(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", discordFrameAncestors)
		next.ServeHTTP(w, r)
	})
}

var (
	cfg     = &config.Values
	session *discordgo.Session
)

// Serve запускает HTTP-сервер казино на ACTIVITY_PORT; s нужна для проверки участников сервера.
func Serve(s *discordgo.Session) {
	session = s
	defer shared.Protect("казино")
	// партии блэкджека и рокетки живут в памяти: после перезапуска возвращаем их ставки
	if n, err := economy.RefundUnsettled(); err != nil {
		log.Printf("Казино: не удалось вернуть незавершённые ставки: %v", err)
	} else if n > 0 {
		log.Printf("Казино: возвращено незавершённых ставок: %d", n)
	}
	logDBLatency()
	srv := &http.Server{Addr: ":" + cfg.ActivityPort, Handler: allowDiscordFrame(activityMux()), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("Казино: порт %s, режим разработки %v", cfg.ActivityPort, cfg.ActivityDevUserID != 0)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("Казино остановлено: %v", err)
	}
}

// logDBLatency пишет в лог время обмена с базой: ставка — около четырёх таких обменов,
// так что по этой цифре видно, сколько задержки добавляет расстояние до Supabase.
func logDBLatency() {
	best := time.Duration(0)
	for range 3 {
		start := time.Now()
		if err := store.Exec("SELECT 1"); err != nil {
			return
		}
		if d := time.Since(start); best == 0 || d < best {
			best = d
		}
	}
	log.Printf("Казино: обмен с базой занимает %v", best.Round(time.Millisecond))
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

// codeNotEnough — по этому коду страница показывает картинку «ты слишком беден».
const codeNotEnough = "not_enough"

func writeNotEnough(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "code": codeNotEnough})
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
		defer shared.Protect("обработчике казино")
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		playersMu.Lock()
		p, ok := players[token]
		playersMu.Unlock()
		if !ok || time.Now().After(p.Expires) {
			writeError(w, http.StatusUnauthorized, "Сессия устарела, открой казино заново")
			return
		}
		// гражданство проверяется на каждый запрос: его могут отнять, пока казино открыто
		if citizen, err := citizenCached(p); err != nil {
			log.Printf("Казино: не удалось проверить гражданство: %v", err)
			writeError(w, http.StatusInternalServerError, "Ошибка базы, попробуй ещё раз")
			return
		} else if !citizen {
			writeError(w, http.StatusForbidden, notCitizen)
			return
		}
		h(w, r, p)
	}
}

const tooFastText = "Слишком часто, подожди секунду"

func tooFast(w http.ResponseWriter, p player) bool {
	if limited(p) {
		writeError(w, http.StatusTooManyRequests, tooFastText)
		return true
	}
	return false
}

// tooFastBet — tooFast для ставок с id от клиента. Повтор ставки (ответ потерялся, клиент прислал
// тот же id) обычно приходит сразу, поэтому при отказе сначала ищем сохранённый итог. В обычном
// случае базу заранее не спрашиваем: повтор поймает settle по конфликту id.
func tooFastBet(w http.ResponseWriter, id string, p player) bool {
	if !limited(p) {
		return false
	}
	if !replayed(w, id, p) {
		writeError(w, http.StatusTooManyRequests, tooFastText)
	}
	return true
}

func limited(p player) bool {
	now := time.Now()
	lastBetMu.Lock()
	defer lastBetMu.Unlock()
	if now.Sub(lastBet[playerKey(p)]) < betInterval {
		return true
	}
	for k, t := range lastBet {
		if now.Sub(t) > time.Minute {
			delete(lastBet, k)
		}
	}
	lastBet[playerKey(p)] = now
	return false
}

func isLocal(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback()
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
	defer shared.Protect("входе в казино")
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
		if !isLocal(r) {
			writeError(w, http.StatusForbidden, "Режим разработки работает только с этого компьютера")
			return
		}
		p = player{UserID: cfg.ActivityDevUserID, GuildID: shared.ParseID(cfg.GuildID), Name: "Тестовый игрок"}
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
	user := map[string]string{"id": shared.IDStr(p.UserID), "name": p.Name}
	citizen, err := isCitizen(p)
	if err != nil {
		log.Printf("Казино: не удалось проверить гражданство: %v", err)
		writeError(w, http.StatusInternalServerError, "Ошибка базы, попробуй ещё раз")
		return
	}
	if !citizen {
		// не гражданину токен не выдаём: страница покажет заглушку
		writeJSON(w, http.StatusOK, map[string]any{"citizen": false, "user": user})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"citizen": true, "token": newPlayer(p), "user": user})
}

func discordPlayer(code, guild string) (player, int, error) {
	clientID := activityClientID()
	if cfg.ClientSecret == "" || clientID == "" || session == nil {
		return player{}, http.StatusUnauthorized, errors.New("Вход через Discord пока не настроен")
	}
	guildID := shared.ParseID(guild)
	if guildID == 0 {
		return player{}, http.StatusForbidden, errors.New("Казино работает только на сервере")
	}
	client := shared.NewHTTPClient()
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
	if err != nil || resp.StatusCode != http.StatusOK || shared.ParseID(user.ID) == 0 {
		return player{}, http.StatusUnauthorized, errors.New("Discord не подтвердил вход")
	}
	m, err := session.GuildMember(guild, user.ID)
	if err != nil {
		return player{}, http.StatusForbidden, errors.New("Ты не участник этого сервера")
	}
	return player{UserID: shared.ParseID(user.ID), GuildID: guildID, Name: shared.OrEmpty(shared.DisplayName(m, m.User), user.ID)}, 0, nil
}

func badBetID(w http.ResponseWriter, id string) bool {
	if id == "" || len(id) > betIDMax {
		writeError(w, http.StatusUnprocessableEntity, "Неверный id ставки")
		return true
	}
	return false
}

// replayed отвечает сохранённым итогом, если ставка с этим id уже есть (или с ней что-то не так).
func replayed(w http.ResponseWriter, id string, p player) bool {
	raw, found, err := replay(id, p.UserID)
	if err != nil || found {
		respondBet(w, raw, err, p)
		return true
	}
	return false
}

func respondBet(w http.ResponseWriter, raw json.RawMessage, err error, p player) {
	if errors.Is(err, economy.ErrNotEnough) {
		writeNotEnough(w, err)
		return
	}
	if errors.Is(err, errBetOwner) {
		writeError(w, http.StatusConflict, "Эта ставка уже занята")
		return
	}
	if err != nil {
		log.Printf("Казино: ошибка ставки %d: %v", p.UserID, err)
		writeError(w, http.StatusInternalServerError, "Что-то пошло не так, попробуй ещё раз")
		return
	}
	writeJSON(w, http.StatusOK, raw)
}
