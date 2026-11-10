package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5"

	"pushups-bot/internal/shared"
	"pushups-bot/internal/store"
)

var (
	pubgMu          sync.Mutex
	errPubgNotFound = errors.New("игрок PUBG не найден")
)

// pubgScore: очки = 100 за килл + 50 за ассист + 1 за единицу урона → очки и отжимания.
func pubgScore(kills, assists int, damage float64) (points, pushups int) {
	points = kills*100 + assists*50 + int(math.RoundToEven(damage))
	if points >= pubgSafePoints {
		return points, 0
	}
	return points, pubgPushups
}

func pubgAPI(path string, params url.Values, out any) error {
	u := pubgBase + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.PubgKey)
	req.Header.Set("Accept", "application/vnd.api+json")
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errPubgNotFound
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("PUBG API %d для %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type pubgLink struct {
	GuildID, UserID int64
	AccountID, Name string
	LinkedAt        time.Time
}

type pubgRef struct {
	ID string `json:"id"`
}

type pubgPlayers struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name string `json:"name"`
		} `json:"attributes"`
		Relationships struct {
			Matches struct {
				Data []pubgRef `json:"data"`
			} `json:"matches"`
		} `json:"relationships"`
	} `json:"data"`
}

type pubgStats struct {
	PlayerID string  `json:"playerId"`
	Kills    int     `json:"kills"`
	Assists  int     `json:"assists"`
	Damage   float64 `json:"damageDealt"`
}

// читаем только нужные поля: ответы с матчами большие, память экономим
type pubgMatch struct {
	Data struct {
		Attributes struct {
			CreatedAt string `json:"createdAt"`
			MatchType string `json:"matchType"`
			GameMode  string `json:"gameMode"`
		} `json:"attributes"`
	} `json:"data"`
	Included []struct {
		Type       string `json:"type"`
		ID         string `json:"id"`
		Attributes struct {
			Stats pubgStats `json:"stats"`
		} `json:"attributes"`
		Relationships struct {
			Participants struct {
				Data []pubgRef `json:"data"`
			} `json:"participants"`
		} `json:"relationships"`
	} `json:"included"`
}

type pubgResult struct {
	GuildID, UserID                int64
	Kills, Assists, Damage, Points int
	Pushups                        int
	Mode                           string
	PlayedAt                       time.Time
}

type pubgMember struct {
	link  pubgLink
	stats pubgStats
}

// processNewPubgMatches находит новые пати-матчи PUBG и начисляет отжимания. guildID=0 — все серверы.
//
// Матч засчитывается игроку, если он начался после привязки аккаунта и в его команде было минимум partyMinLinked
// участников одного сервера, привязанных к началу матча.
func processNewPubgMatches(guildID int64) (results []pubgResult, failed []int64, err error) {
	if cfg.PubgKey == "" {
		return nil, nil, nil
	}
	pubgMu.Lock()
	defer pubgMu.Unlock()
	sql := "SELECT guild_id, user_id, account_id, name, linked_at FROM pubg_links"
	var args []any
	if guildID != 0 {
		sql += " WHERE guild_id = $1"
		args = append(args, guildID)
	}
	links, err := store.Fetch[pubgLink](sql, args...)
	if err != nil {
		return nil, nil, err
	}
	// один аккаунт может быть привязан на разных серверах
	byAccount := map[string][]pubgLink{}
	var accounts []string
	for _, l := range links {
		if _, ok := byAccount[l.AccountID]; !ok {
			accounts = append(accounts, l.AccountID)
		}
		byAccount[l.AccountID] = append(byAccount[l.AccountID], l)
	}

	// недавние матчи игроков: до 10 аккаунтов за запрос (лимит — 10 запросов в минуту)
	matchGuilds := map[string]map[int64]bool{}
	for chunk := range slices.Chunk(accounts, 10) {
		var data pubgPlayers
		if err := pubgAPI("/players", url.Values{"filter[playerIds]": {strings.Join(chunk, ",")}}, &data); err != nil {
			log.Printf("Не удалось получить матчи PUBG: %v", err)
			for _, acc := range chunk {
				for _, l := range byAccount[acc] {
					failed = append(failed, l.UserID)
				}
			}
			continue
		}
		for _, p := range data.Data {
			for _, m := range p.Relationships.Matches.Data {
				if matchGuilds[m.ID] == nil {
					matchGuilds[m.ID] = map[int64]bool{}
				}
				for _, l := range byAccount[p.ID] {
					matchGuilds[m.ID][l.GuildID] = true
				}
			}
		}
	}

	for matchID, guilds := range matchGuilds {
		ids := make([]int64, 0, len(guilds))
		for g := range guilds {
			ids = append(ids, g)
		}
		checked, err := store.Column[int64]("SELECT guild_id FROM pubg_checked WHERE match_id = $1 AND guild_id = ANY($2::bigint[])", matchID, ids)
		if err != nil {
			return results, failed, err
		}
		todo := slices.DeleteFunc(ids, func(g int64) bool { return slices.Contains(checked, g) })
		if len(todo) == 0 {
			continue
		}
		var match pubgMatch
		if err := pubgAPI("/matches/"+matchID, nil, &match); err != nil { // этот эндпоинт не лимитируется
			log.Printf("Не удалось получить матч PUBG %s: %v", matchID, err)
			continue
		}
		attrs := match.Data.Attributes
		playedAt, err := time.Parse(time.RFC3339, attrs.CreatedAt)
		if err != nil {
			log.Printf("Странная дата матча PUBG %s: %v", matchID, err)
			continue
		}
		stats := map[string]pubgStats{}
		for _, inc := range match.Included {
			if inc.Type == "participant" {
				stats[inc.ID] = inc.Attributes.Stats
			}
		}
		for _, gid := range todo {
			if pubgMatchTypes[attrs.MatchType] {
				for _, inc := range match.Included {
					if inc.Type != "roster" {
						continue
					}
					var members []pubgMember
					users := map[int64]bool{}
					for _, ref := range inc.Relationships.Participants.Data {
						s, ok := stats[ref.ID]
						if !ok {
							continue
						}
						for _, l := range byAccount[s.PlayerID] {
							if l.GuildID == gid && !l.LinkedAt.After(playedAt) {
								members = append(members, pubgMember{l, s})
								users[l.UserID] = true
							}
						}
					}
					if len(users) < partyMinLinked {
						continue
					}
					for _, m := range members {
						res, err := savePubgMatch(gid, matchID, attrs.GameMode, playedAt, m)
						if err != nil {
							return results, failed, err
						}
						if res != nil {
							results = append(results, *res)
						}
					}
				}
			}
			if err := store.Exec("INSERT INTO pubg_checked (guild_id, match_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, matchID); err != nil {
				return results, failed, err
			}
		}
	}
	slices.SortFunc(results, func(a, b pubgResult) int { return a.PlayedAt.Compare(b.PlayedAt) })
	return results, failed, nil
}

func savePubgMatch(guildID int64, matchID, mode string, playedAt time.Time, m pubgMember) (*pubgResult, error) {
	points, pushups := pubgScore(m.stats.Kills, m.stats.Assists, m.stats.Damage)
	damage := int(math.RoundToEven(m.stats.Damage))
	ctx, cancel := store.Ctx()
	defer cancel()
	tx, err := store.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var inserted string
	err = tx.QueryRow(ctx, `INSERT INTO pubg_matches (guild_id, user_id, match_id, kills, assists, damage, points, pushups, played_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING RETURNING match_id`,
		guildID, m.link.UserID, matchID, m.stats.Kills, m.stats.Assists, damage, points, pushups, playedAt).Scan(&inserted)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pushups > 0 {
		reason := fmt.Sprintf("PUBG %d кил., %d асс., %d урона = %d очк.", m.stats.Kills, m.stats.Assists, damage, points)
		if _, err := tx.Exec(ctx, `INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source)
			VALUES ($1, $2, 'owe', $3, $4, $5, 'pubg')`, guildID, m.link.UserID, pushups, botUserID, reason); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &pubgResult{guildID, m.link.UserID, m.stats.Kills, m.stats.Assists, damage, points, pushups, shared.OrEmpty(mode, "?"), playedAt}, nil
}

func pubgReportEmbeds(results []pubgResult) []*discordgo.MessageEmbed {
	lines := make([]string, 0, len(results))
	for _, r := range results {
		verdict := "без отжиманий 😎"
		if r.Pushups > 0 {
			verdict = fmt.Sprintf("**%d** отжиманий", r.Pushups)
		}
		lines = append(lines, fmt.Sprintf("%s — %d кил., %d асс., %d урона = %d очков → %s (%s)",
			mention(r.UserID), r.Kills, r.Assists, r.Damage, r.Points, verdict, r.Mode))
	}
	return chunkEmbeds("🔫 Новые матчи PUBG", colorDarkOrange, lines)
}

func pubgLoop() {
	results, failed, err := processNewPubgMatches(0)
	if err != nil {
		log.Printf("Ошибка автопроверки PUBG: %v", err)
		return
	}
	log.Printf("Автопроверка PUBG: новых результатов %d, ошибок %d, память %s", len(results), len(failed), memoryUsage())
	guild, ok := channelGuild(cfg.DotaChannelID)
	if len(results) == 0 || !ok {
		return
	}
	results = slices.DeleteFunc(results, func(r pubgResult) bool { return r.GuildID != guild })
	sendEmbeds(cfg.DotaChannelID, pubgReportEmbeds(results))
}

const pubgNotConfigured = "PUBG не настроен: не задан PUBG_API_KEY."

func linkPubgHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	if cfg.PubgKey == "" {
		return r.replyHidden(pubgNotConfigured)
	}
	user := r.userOption("user")
	nickname := r.stringOption("nickname")
	if err := r.deferReply(); err != nil {
		return err
	}
	var data pubgPlayers
	err := pubgAPI("/players", url.Values{"filter[playerNames]": {strings.TrimSpace(nickname)}}, &data)
	if errors.Is(err, errPubgNotFound) || (err == nil && len(data.Data) == 0) {
		return r.followup(fmt.Sprintf("Игрок `%s` не найден в PUBG (%s). Ник чувствителен к регистру.", nickname, cfg.PubgShard), nil, false)
	}
	if err != nil {
		return r.followup("PUBG API сейчас не отвечает, попробуй через минуту.", nil, false)
	}
	player := data.Data[0]
	var linkedAt time.Time
	// перепривязка того же аккаунта не сдвигает начало учёта
	if _, err := store.FetchOne(`INSERT INTO pubg_links (guild_id, user_id, account_id, name, linked_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (guild_id, user_id) DO UPDATE SET account_id = EXCLUDED.account_id, name = EXCLUDED.name,
		linked_by = EXCLUDED.linked_by,
		linked_at = CASE WHEN pubg_links.account_id = EXCLUDED.account_id THEN pubg_links.linked_at ELSE now() END
		RETURNING linked_at`, []any{r.guildID(), user.ID, player.ID, player.Attributes.Name, r.userID()}, &linkedAt); err != nil {
		return err
	}
	return r.followup(fmt.Sprintf("🔗 %s ↔ **%s** (PUBG)\nСчитаются пати-матчи с <t:%d:f>.", user.mention(), player.Attributes.Name, linkedAt.Unix()), nil, false)
}

func unlinkPubgHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	user := r.userOption("user")
	var name string
	found, err := store.FetchOne("DELETE FROM pubg_links WHERE guild_id = $1 AND user_id = $2 RETURNING name", []any{r.guildID(), user.ID}, &name)
	if err != nil {
		return err
	}
	if !found {
		return r.replyHidden(fmt.Sprintf("У %s нет привязки PUBG.", user.mention()))
	}
	return r.reply(fmt.Sprintf("✂️ %s отвязан от PUBG-аккаунта **%s**.", user.mention(), name))
}

func pubgLinksHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	rows, err := store.Fetch[struct {
		UserID int64
		Name   string
	}]("SELECT user_id, name FROM pubg_links WHERE guild_id = $1 ORDER BY linked_at", r.guildID())
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(rows))
	for _, l := range rows {
		lines = append(lines, fmt.Sprintf("%s — **%s**", mention(l.UserID), l.Name))
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: "🔗 Привязанные PUBG-аккаунты", Description: shared.OrEmpty(strings.Join(lines, "\n"), "Пока никого."), Color: colorDarkOrange,
		Footer: footer(fmt.Sprintf("Автопроверка каждые %d мин · только пати-игры", cfg.DotaCheckMinutes)),
	}, true)
}

func calculatePubgHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	if cfg.PubgKey == "" {
		return r.replyHidden(pubgNotConfigured)
	}
	if err := r.deferReply(); err != nil {
		return err
	}
	results, failed, err := processNewPubgMatches(r.guildID())
	if err != nil {
		return err
	}
	note := failedNote("Не удалось получить матчи (возможно, лимит PUBG API — попробуй через минуту): ", failed)
	if len(results) == 0 {
		return r.followup(strings.TrimRight("Новых пати-матчей PUBG нет.\n"+note, "\n"), nil, false)
	}
	return r.followup(note, pubgReportEmbeds(results), false)
}
