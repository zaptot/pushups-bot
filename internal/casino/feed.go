package casino

import (
	"net/http"
	"sync"
	"time"

	"pushups-bot/internal/shared"
	"pushups-bot/internal/store"
)

const (
	feedRecent  = 10 // сколько последних игр показывать
	feedTop     = 5  // сколько крупнейших выигрышей
	feedTopDays = 7  // за сколько дней топ
	nameTTL     = 10 * time.Minute
	nameUnknown = "Игрок"
)

// feedGame — одна сыгранная ставка в ленте. Выигрыш считается чистым: выплата + джекпот − ставка.
type feedGame struct {
	Name       string    `json:"name"`
	Game       string    `json:"game"`
	Stake      int       `json:"stake"`
	Payout     int       `json:"payout"`
	Multiplier float64   `json:"multiplier"`
	Jackpot    int       `json:"jackpot"`
	Profit     int       `json:"profit"`
	At         time.Time `json:"at"`
}

type feedRow struct {
	UserID     int64
	Game       string
	Stake      int
	Payout     int
	Multiplier float64
	Jackpot    int
	At         time.Time
}

const feedSelect = `SELECT b.user_id, b.game, b.stake, b.payout, b.multiplier::float8, COALESCE(j.amount, 0), b.created_at
	FROM casino_bets b LEFT JOIN jackpot_wins j ON j.bet_id = b.id
	WHERE b.guild_id = $1`

// имена берём из Discord (в casino_bets их нет) и кэшируем: на сервере всего десяток игроков
var (
	namesMu sync.Mutex
	names   = map[[2]int64]cachedName{}
)

type cachedName struct {
	name    string
	expires time.Time
}

// memberName — отображаемое имя участника; в тестах подменяется.
var memberName = func(guildID, userID int64) string {
	key := [2]int64{guildID, userID}
	namesMu.Lock()
	c, ok := names[key]
	namesMu.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.name
	}
	name := discordName(guildID, userID)
	if name == "" {
		if ok {
			return c.name // Discord не ответил — пусть будет старое имя
		}
		return nameUnknown
	}
	namesMu.Lock()
	names[key] = cachedName{name, time.Now().Add(nameTTL)}
	namesMu.Unlock()
	return name
}

func discordName(guildID, userID int64) string {
	if session == nil {
		return ""
	}
	guild, user := shared.IDStr(guildID), shared.IDStr(userID)
	m, err := session.State.Member(guild, user)
	if err != nil || m == nil || m.User == nil {
		if m, err = session.GuildMember(guild, user); err != nil || m == nil || m.User == nil {
			return ""
		}
	}
	return shared.DisplayName(m, m.User)
}

func feedGames(sql string, args ...any) ([]feedGame, error) {
	rows, err := store.Fetch[feedRow](sql, args...)
	if err != nil {
		return nil, err
	}
	games := make([]feedGame, 0, len(rows))
	for _, r := range rows {
		games = append(games, feedGame{
			Name: memberName(args[0].(int64), r.UserID), Game: r.Game, Stake: r.Stake, Payout: r.Payout,
			Multiplier: r.Multiplier, Jackpot: r.Jackpot, Profit: r.Payout + r.Jackpot - r.Stake, At: r.At,
		})
	}
	return games, nil
}

// handleFeed — последние игры сервера и топ чистых выигрышей за неделю.
func handleFeed(w http.ResponseWriter, _ *http.Request, p player) {
	recent, err := feedGames(feedSelect+` ORDER BY b.created_at DESC LIMIT $2`, p.GuildID, feedRecent)
	if err != nil {
		respondBet(w, nil, err, p)
		return
	}
	top, err := feedGames(feedSelect+` AND b.created_at >= now() - make_interval(days => $3)
		AND b.payout + COALESCE(j.amount, 0) > b.stake
		ORDER BY b.payout + COALESCE(j.amount, 0) - b.stake DESC, b.created_at DESC LIMIT $2`, p.GuildID, feedTop, feedTopDays)
	if err != nil {
		respondBet(w, nil, err, p)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recent": recent, "top": top, "topDays": feedTopDays})
}
