package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v5"
)

var (
	apiClient = &http.Client{Timeout: 20 * time.Second}
	dotaMu    sync.Mutex // чтобы ручной и автоматический подсчёт не шли одновременно
	digitsRe  = regexp.MustCompile(`\d+`)
)

// kdaPushups: KDA = (убийства + ассисты) / смерти (смерти не меньше 1) → сколько отжиманий.
func kdaPushups(kills, deaths, assists int) (float64, int) {
	kda := float64(kills+assists) / float64(max(deaths, 1))
	switch {
	case kda >= 4:
		return kda, 0
	case kda >= 3:
		return kda, 10
	case kda >= 2:
		return kda, 20
	case kda >= 1:
		return kda, 30
	case kda > 0:
		return kda, 50
	}
	return kda, 70
}

// parseAccountID принимает Dota ID, Steam64 ID или ссылку на профиль Dotabuff/OpenDota/Steam (с числовым ID).
func parseAccountID(text string) (int64, bool) {
	numbers := digitsRe.FindAllString(text, -1)
	if len(numbers) == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(numbers[len(numbers)-1], 10, 64)
	if err != nil {
		return 0, false
	}
	if n >= steam64Base {
		n -= steam64Base
	}
	return n, n > 0 && n < 1<<32
}

func opendota(method, path string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if cfg.OpenDotaKey != "" {
		params.Set("api_key", cfg.OpenDotaKey)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, opendotaBase+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("OpenDota %d для %s", resp.StatusCode, path)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type dotaLink struct {
	GuildID, UserID, AccountID int64
	LinkedAt                   time.Time
	SoloSince                  *time.Time
}

type dotaMatch struct {
	MatchID   int64 `json:"match_id"`
	StartTime int64 `json:"start_time"`
	Duration  int64 `json:"duration"`
	Kills     int   `json:"kills"`
	Deaths    int   `json:"deaths"`
	Assists   int   `json:"assists"`
}

type dotaResult struct {
	GuildID, UserID, MatchID int64
	K, D, A, Pushups         int
	KDA                      float64
	Solo                     bool
}

type dotaPlay struct {
	link  dotaLink
	match dotaMatch
}

// processNewMatches находит новые матчи привязанных игроков и начисляет отжимания. guildID=0 — все серверы.
//
// Для каждого игрока считаются только матчи, начатые после привязки его аккаунта. Матч засчитывается, только если
// в нём играли минимум partyMinLinked участников одного сервера, привязанных к началу матча (или включено соло).
func processNewMatches(guildID int64) (results []dotaResult, failed []int64, err error) {
	dotaMu.Lock()
	defer dotaMu.Unlock()
	sql := "SELECT guild_id, user_id, account_id, linked_at, solo_since FROM dota_links"
	var args []any
	if guildID != 0 {
		sql += " WHERE guild_id = $1"
		args = append(args, guildID)
	}
	links, err := fetch[dotaLink](sql, args...)
	if err != nil {
		return nil, nil, err
	}
	type key struct{ guild, match int64 }
	byMatch := map[key][]dotaPlay{}
	for _, link := range links {
		// просим OpenDota подтянуть свежие матчи (появятся к следующей проверке)
		_ = opendota("POST", fmt.Sprintf("/players/%d/refresh", link.AccountID), nil, nil)
		days := int(math.Ceil(time.Since(link.LinkedAt).Hours()/24)) + 1
		// significant=0 — иначе OpenDota скрывает «незначимые» режимы, например Турбо
		params := url.Values{"date": {strconv.Itoa(days)}, "significant": {"0"}}
		var matches []dotaMatch
		if err := opendota("GET", fmt.Sprintf("/players/%d/matches", link.AccountID), params, &matches); err != nil {
			log.Printf("Не удалось получить матчи %d: %v", link.AccountID, err)
			failed = append(failed, link.UserID)
			continue
		}
		for _, m := range matches {
			if m.StartTime >= link.LinkedAt.Unix() && m.Duration >= minMatchSeconds {
				k := key{link.GuildID, m.MatchID}
				byMatch[k] = append(byMatch[k], dotaPlay{link, m})
			}
		}
		time.Sleep(dotaRequestPause) // бережём лимиты OpenDota
	}

	keys := make([]key, 0, len(byMatch))
	for k := range byMatch {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b key) int {
		return int(byMatch[a][0].match.StartTime - byMatch[b][0].match.StartTime)
	})
	for _, k := range keys {
		plays := byMatch[k]
		party := map[int64]bool{}
		for _, p := range plays {
			party[p.link.UserID] = true
		}
		isParty := len(party) >= partyMinLinked
		if !isParty {
			// соло-игра (или напарник ещё не появился на OpenDota). Считается только тем, кто включил соло, после
			// включения — и не раньше чем через soloDelay после конца матча, чтобы не записать пати-игру как соло.
			plays = slices.DeleteFunc(slices.Clone(plays), func(p dotaPlay) bool {
				return p.link.SoloSince == nil || p.match.StartTime < p.link.SoloSince.Unix() ||
					p.match.StartTime+p.match.Duration+int64(soloDelay.Seconds()) > time.Now().Unix()
			})
		}
		for _, p := range plays {
			res, err := saveDotaMatch(k.guild, p, isParty)
			if err != nil {
				return results, failed, err
			}
			if res != nil {
				results = append(results, *res)
			}
		}
	}
	return results, failed, nil
}

func saveDotaMatch(guildID int64, p dotaPlay, isParty bool) (*dotaResult, error) {
	m := p.match
	kda, pushups := kdaPushups(m.Kills, m.Deaths, m.Assists)
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var inserted int64
	err = tx.QueryRow(ctx, `INSERT INTO dota_matches
		(guild_id, user_id, match_id, kills, deaths, assists, pushups, played_at, is_party)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING RETURNING match_id`,
		guildID, p.link.UserID, m.MatchID, m.Kills, m.Deaths, m.Assists, pushups, time.Unix(m.StartTime, 0).UTC(), isParty).Scan(&inserted)
	if err == pgx.ErrNoRows {
		return nil, nil // уже обработан
	}
	if err != nil {
		return nil, err
	}
	if pushups > 0 {
		reason := fmt.Sprintf("Dota %d/%d/%d, матч %d", m.Kills, m.Deaths, m.Assists, m.MatchID)
		if !isParty {
			reason += " (соло)"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source)
			VALUES ($1, $2, 'owe', $3, $4, $5, 'dota')`, guildID, p.link.UserID, pushups, botUserID, reason); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &dotaResult{guildID, p.link.UserID, m.MatchID, m.Kills, m.Deaths, m.Assists, pushups, kda, !isParty}, nil
}

func dotaReportEmbeds(results []dotaResult) []*discordgo.MessageEmbed {
	lines := make([]string, 0, len(results))
	for _, r := range results {
		verdict := "без отжиманий 😎"
		if r.Pushups > 0 {
			verdict = fmt.Sprintf("**%d** отжиманий", r.Pushups)
		}
		line := fmt.Sprintf("%s — %d/%d/%d, KDA %.2f → %s ([матч](https://www.opendota.com/matches/%d))",
			mention(r.UserID), r.K, r.D, r.A, r.KDA, verdict, r.MatchID)
		if r.Solo {
			line += " · соло"
		}
		lines = append(lines, line)
	}
	return chunkEmbeds("🎮 Новые матчи Dota 2", colorRed, lines)
}

func dotaLoop() {
	results, failed, err := processNewMatches(0)
	if err != nil {
		log.Printf("Ошибка автопроверки Dota: %v", err)
		return
	}
	log.Printf("Автопроверка Dota: новых результатов %d, ошибок %d, память %s", len(results), len(failed), memoryUsage())
	guild, ok := channelGuild(cfg.DotaChannelID)
	if len(results) == 0 || cfg.DotaChannelID == "" {
		return
	}
	if !ok {
		log.Printf("Канал DOTA_CHANNEL_ID=%s не найден", cfg.DotaChannelID)
		return
	}
	results = slices.DeleteFunc(results, func(r dotaResult) bool { return r.GuildID != guild })
	sendEmbeds(cfg.DotaChannelID, dotaReportEmbeds(results))
}

func sendEmbeds(channelID string, embeds []*discordgo.MessageEmbed) {
	for _, e := range embeds {
		if _, err := session.ChannelMessageSendEmbed(channelID, e); err != nil {
			log.Printf("Не удалось отправить отчёт: %v", err)
		}
	}
}

func linkDotaHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	user := r.userOption("user")
	accountID, ok := parseAccountID(r.stringOption("account"))
	if !ok {
		return r.replyHidden("Не понял ID. Нужен числовой Dota ID / Steam64 ID или ссылка с ним.")
	}
	if err := r.deferReply(); err != nil {
		return err
	}
	var player struct {
		Profile *struct {
			Name string `json:"personaname"`
		} `json:"profile"`
	}
	if err := opendota("GET", fmt.Sprintf("/players/%d", accountID), nil, &player); err != nil {
		return r.followup("OpenDota сейчас не отвечает, попробуй позже.", nil, false)
	}
	if player.Profile == nil {
		return r.followup(fmt.Sprintf("Не нашёл игрока `%d` на OpenDota. Проверь ID и что в Dota 2 включено "+
			"«Открыть общий доступ к данным матчей» (Expose Public Match Data).", accountID), nil, false)
	}
	var linkedAt time.Time
	// перепривязка того же аккаунта не сдвигает начало учёта
	if _, err := fetchOne(`INSERT INTO dota_links (guild_id, user_id, account_id, linked_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (guild_id, user_id) DO UPDATE SET account_id = EXCLUDED.account_id, linked_by = EXCLUDED.linked_by,
		linked_at = CASE WHEN dota_links.account_id = EXCLUDED.account_id THEN dota_links.linked_at ELSE now() END
		RETURNING linked_at`, []any{r.guildID(), user.ID, accountID, r.userID()}, &linkedAt); err != nil {
		return err
	}
	return r.followup(fmt.Sprintf("🔗 %s ↔ **%s** (`%d`)\nСчитаются пати-матчи с <t:%d:f>.",
		user.mention(), orEmpty(player.Profile.Name, "?"), accountID, linkedAt.Unix()), nil, false)
}

func unlinkDotaHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	user := r.userOption("user")
	var accountID int64
	found, err := fetchOne("DELETE FROM dota_links WHERE guild_id = $1 AND user_id = $2 RETURNING account_id",
		[]any{r.guildID(), user.ID}, &accountID)
	if err != nil {
		return err
	}
	if !found {
		return r.replyHidden(fmt.Sprintf("У %s нет привязки.", user.mention()))
	}
	return r.reply(fmt.Sprintf("✂️ %s отвязан от `%d`.", user.mention(), accountID))
}

func dotaLinksHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	rows, err := fetch[struct {
		UserID, AccountID int64
		SoloSince         *time.Time
	}]("SELECT user_id, account_id, solo_since FROM dota_links WHERE guild_id = $1 ORDER BY linked_at", r.guildID())
	if err != nil {
		return err
	}
	var lines []string
	for _, l := range rows {
		line := fmt.Sprintf("%s — [`%d`](https://www.opendota.com/players/%d)", mention(l.UserID), l.AccountID, l.AccountID)
		if l.SoloSince != nil {
			line += " · 🎯 соло"
		}
		lines = append(lines, line)
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: "🔗 Привязанные аккаунты", Description: orEmpty(strings.Join(lines, "\n"), "Пока никого."), Color: colorRed,
		Footer: footer(fmt.Sprintf("Автопроверка каждые %d мин · только пати-игры", cfg.DotaCheckMinutes)),
	}, true)
}

func failedNote(prefix string, failed []int64) string {
	if len(failed) == 0 {
		return ""
	}
	return prefix + mentions(failed)
}

func calculateDotaHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	if err := r.deferReply(); err != nil {
		return err
	}
	results, failed, err := processNewMatches(r.guildID())
	if err != nil {
		return err
	}
	note := failedNote("Не удалось получить матчи: ", failed)
	if len(results) == 0 {
		return r.followup(strings.TrimRight("Новых пати-матчей нет.\n"+note, "\n"), nil, false)
	}
	return r.followup(note, dotaReportEmbeds(results), false)
}

func setDotaSolo(r *request, enabled bool) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	var since time.Time
	found, err := fetchOne(`UPDATE dota_links SET solo_since = CASE WHEN $3 THEN COALESCE(solo_since, now()) ELSE NULL END
		WHERE guild_id = $1 AND user_id = $2 RETURNING COALESCE(solo_since, 'epoch'::timestamptz)`,
		[]any{r.guildID(), r.userID(), enabled}, &since)
	if err != nil {
		return err
	}
	switch {
	case !found:
		return r.replyHidden("Сначала привяжи Dota-аккаунт: `/linkdota`.")
	case enabled:
		return r.reply(fmt.Sprintf("🎯 %s теперь получает отжимания и за **соло**-игры в Dota (с <t:%d:f>).", r.userMention(), since.Unix()))
	}
	return r.reply(fmt.Sprintf("👥 %s снова получает отжимания только за пати-игры в Dota.", r.userMention()))
}
