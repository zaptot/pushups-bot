package main

import (
	"fmt"
	"log"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// ---------- гражданство: проверки ----------

func citizenIDs(guildID int64) ([]int64, error) {
	return column[int64]("SELECT user_id FROM citizens WHERE guild_id = $1 ORDER BY joined_at", guildID)
}

func isCitizen(guildID, userID int64) (bool, error) {
	var one int
	return fetchOne("SELECT 1 FROM citizens WHERE guild_id = $1 AND user_id = $2", []any{guildID, userID}, &one)
}

// activeCitizens: граждане без заморозки (долг не больше freezeDebt).
func activeCitizens(guildID int64) ([]int64, error) {
	stats, err := guildStats(guildID)
	if err != nil {
		return nil, err
	}
	ids, err := citizenIDs(guildID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ids, func(id int64) bool { return stats[id].remaining() > freezeDebt }), nil
}

type president struct {
	UserID    int64
	ElectedAt time.Time
}

func currentPresident(guildID int64) (*president, error) {
	var p president
	found, err := fetchOne("SELECT user_id, elected_at FROM presidents WHERE guild_id = $1 ORDER BY elected_at DESC LIMIT 1",
		[]any{guildID}, &p.UserID, &p.ElectedAt)
	if !found {
		return nil, err
	}
	return &p, nil
}

func isPresident(guildID, userID int64) (bool, error) {
	p, err := currentPresident(guildID)
	return p != nil && p.UserID == userID, err
}

// requireActiveCitizen отвечает отказом и возвращает false, если пользователь не гражданин или заморожен.
func requireActiveCitizen(r *request) (bool, error) {
	if !r.inGuild() {
		return false, r.noGuild()
	}
	citizen, err := isCitizen(r.guildID(), r.userID())
	if err != nil {
		return false, err
	}
	if !citizen {
		return false, r.replyHidden("Это могут только граждане.")
	}
	st, err := userStats(r.guildID(), r.userID())
	if err != nil {
		return false, err
	}
	if st.remaining() > freezeDebt {
		return false, r.replyHidden(fmt.Sprintf("❄️ Твои привилегии заморожены: долг **%d** > %d. Отожмись!", st.remaining(), freezeDebt))
	}
	return true, nil
}

// ---------- роли ----------

func roleID(guildID int64, key string) (string, bool) {
	name := roleNames[key]
	guild, err := session.State.Guild(idStr(guildID))
	if err != nil {
		return "", false
	}
	session.State.RLock()
	for _, role := range guild.Roles {
		if role.Name == name {
			session.State.RUnlock()
			return role.ID, true
		}
	}
	session.State.RUnlock()
	role, err := session.GuildRoleCreate(idStr(guildID), &discordgo.RoleParams{Name: name})
	if err != nil {
		log.Printf("Не удалось создать роль %s: %v", name, err)
		return "", false
	}
	return role.ID, true
}

func changeRole(guildID, userID int64, role string, add bool) bool {
	var err error
	if add {
		err = session.GuildMemberRoleAdd(idStr(guildID), idStr(userID), role)
	} else {
		err = session.GuildMemberRoleRemove(idStr(guildID), idStr(userID), role)
	}
	if err == nil {
		return true
	}
	if isRESTStatus(err, 404) {
		return !add // участник ушёл с сервера — снимать нечего
	}
	log.Printf("Не удалось изменить роль %s у %d: %v", role, userID, err)
	return false
}

// setRoleHolders делает так, чтобы роль была ровно у ids (среди тех, кому её выдавал бот).
func setRoleHolders(guildID int64, key string, ids []int64) error {
	role, ok := roleID(guildID, key)
	if !ok {
		return nil
	}
	old, err := column[int64]("SELECT user_id FROM role_holders WHERE guild_id = $1 AND role_key = $2", guildID, key)
	if err != nil {
		return err
	}
	held := map[int64]bool{}
	for _, id := range old {
		held[id] = true
		if !slices.Contains(ids, id) && changeRole(guildID, id, role, false) {
			delete(held, id)
		}
	}
	for _, id := range ids {
		if !slices.Contains(old, id) && changeRole(guildID, id, role, true) {
			held[id] = true
		}
	}
	ctx, cancel := dbCtx()
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "DELETE FROM role_holders WHERE guild_id = $1 AND role_key = $2", guildID, key); err != nil {
		return err
	}
	for id := range held {
		if _, err := tx.Exec(ctx, "INSERT INTO role_holders (guild_id, role_key, user_id) VALUES ($1, $2, $3)", guildID, key, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func marginalIDs(guildID int64) ([]int64, error) {
	return column[int64]("SELECT user_id FROM marginals WHERE guild_id = $1", guildID)
}

// syncRoles: Гражданин, Президент, Маргинал — по данным в базе.
func syncRoles(guildID int64) error {
	citizens, err := citizenIDs(guildID)
	if err != nil {
		return err
	}
	if err := setRoleHolders(guildID, "citizen", citizens); err != nil {
		return err
	}
	p, err := currentPresident(guildID)
	if err != nil {
		return err
	}
	var presidents []int64
	if p != nil {
		presidents = []int64{p.UserID}
	}
	if err := setRoleHolders(guildID, "president", presidents); err != nil {
		return err
	}
	marginals, err := marginalIDs(guildID)
	if err != nil {
		return err
	}
	return setRoleHolders(guildID, "marginal", marginals)
}

// ---------- гражданство: команды ----------

// tryNaturalize делает гражданином, если есть поручители и выполнены условия.
func tryNaturalize(guildID, userID int64) (bool, string, error) {
	citizen, err := isCitizen(guildID, userID)
	if err != nil || citizen {
		return false, "", err
	}
	vouchers, err := column[int64]("SELECT voucher_id FROM vouches WHERE guild_id = $1 AND candidate_id = $2", guildID, userID)
	if err != nil {
		return false, "", err
	}
	citizens, err := citizenIDs(guildID)
	if err != nil {
		return false, "", err
	}
	vouchers = slices.DeleteFunc(vouchers, func(v int64) bool { return !slices.Contains(citizens, v) })
	if len(vouchers) < vouchesNeeded {
		return false, fmt.Sprintf("Поручителей: %d/%d.", len(vouchers), vouchesNeeded), nil
	}
	st, err := userStats(guildID, userID)
	if err != nil {
		return false, "", err
	}
	if st.remaining() >= citizenMaxDebt {
		return false, fmt.Sprintf("Поручителей хватает, но долг %d — нужно меньше %d.", st.remaining(), citizenMaxDebt), nil
	}
	if st.Done == 0 {
		return false, "Поручителей хватает, но не сделано ни одного отжимания.", nil
	}
	if err := exec("INSERT INTO citizens (guild_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", guildID, userID); err != nil {
		return false, "", err
	}
	if err := exec("DELETE FROM vouches WHERE guild_id = $1 AND candidate_id = $2", guildID, userID); err != nil {
		return false, "", err
	}
	if err := syncRoles(guildID); err != nil {
		return false, "", err
	}
	return true, fmt.Sprintf("🎉 %s стал гражданином!", mention(userID)), nil
}

func founderHandler(r *request) error {
	if r.i.Member == nil || r.i.Member.Permissions&discordgo.PermissionManageGuild == 0 {
		return r.replyHidden("Только для админов сервера.")
	}
	user := r.userOption("user")
	if user.Bot {
		return r.replyHidden("Боты не граждане 🤖")
	}
	if err := r.deferReply(); err != nil {
		return err
	}
	if err := exec("INSERT INTO citizens (guild_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", r.guildID(), user.ID); err != nil {
		return err
	}
	if err := syncRoles(r.guildID()); err != nil {
		return err
	}
	return r.followup(fmt.Sprintf("🏛️ %s — гражданин-основатель.", user.mention()), nil, false)
}

func vouchHandler(r *request) error {
	if ok, err := requireActiveCitizen(r); !ok {
		return err
	}
	user := r.userOption("user")
	if user.Bot || user.ID == r.userID() {
		return r.replyHidden("За себя и за ботов ручаться нельзя.")
	}
	citizen, err := isCitizen(r.guildID(), user.ID)
	if err != nil {
		return err
	}
	if citizen {
		return r.replyHidden(fmt.Sprintf("%s уже гражданин.", user.mention()))
	}
	minAge := citizenMinDays * 24 * time.Hour
	if user.JoinedAt.IsZero() || time.Since(user.JoinedAt) < minAge {
		allowed := ""
		if !user.JoinedAt.IsZero() {
			allowed = fmt.Sprintf(" Можно будет <t:%d:R>.", user.JoinedAt.Add(minAge).Unix())
		}
		return r.replyHidden(fmt.Sprintf("%s на сервере меньше %d дней — ручаться пока рано.%s", user.mention(), citizenMinDays, allowed))
	}
	if err := r.deferReply(); err != nil {
		return err
	}
	if err := exec("INSERT INTO vouches (guild_id, candidate_id, voucher_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
		r.guildID(), user.ID, r.userID()); err != nil {
		return err
	}
	_, note, err := tryNaturalize(r.guildID(), user.ID)
	if err != nil {
		return err
	}
	return r.followup(fmt.Sprintf("🤝 %s поручился за %s.\n%s", r.userMention(), user.mention(), note), nil, false)
}

func citizensHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	gid := r.guildID()
	ids, err := citizenIDs(gid)
	if err != nil {
		return err
	}
	active, err := activeCitizens(gid)
	if err != nil {
		return err
	}
	p, err := currentPresident(gid)
	if err != nil {
		return err
	}
	marginals, err := marginalIDs(gid)
	if err != nil {
		return err
	}
	var lines []string
	for _, id := range ids {
		line := mention(id)
		if p != nil && p.UserID == id {
			line += " 👑"
		}
		if !slices.Contains(active, id) {
			line += " ❄️"
		}
		if slices.Contains(marginals, id) {
			line += " 🚫"
		}
		lines = append(lines, line)
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: fmt.Sprintf("🏛️ Граждане (%d)", len(ids)), Description: orEmpty(strings.Join(lines, "\n"), "Пока никого."), Color: colorBlue,
		Footer: footer("👑 президент · ❄️ заморожен · 🚫 маргинал"),
	}, true)
}

func lawsHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	rows, err := fetch[struct {
		Text      string
		AdoptedAt time.Time
	}]("SELECT text, adopted_at FROM laws WHERE guild_id = $1 ORDER BY adopted_at", r.guildID())
	if err != nil {
		return err
	}
	var articles, laws []string
	for i, text := range constitution {
		articles = append(articles, fmt.Sprintf("**%d.** %s", i+1, text))
	}
	for i, l := range rows {
		laws = append(laws, fmt.Sprintf("**%d.** %s (<t:%d:d>)", i+1, l.Text, l.AdoptedAt.Unix()))
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: "📜 Законы", Color: colorDarkGold,
		Description: orEmpty(truncate(strings.Join(laws, "\n"), 4000), "Принятых законов пока нет."),
		Fields:      []*discordgo.MessageEmbedField{field("Конституция", strings.Join(articles, "\n"), false)},
	}, false)
}

// ---------- голосования ----------

var kindTitles = map[string]string{
	"revoke":   "🗳️ Лишение гражданства",
	"accuse":   "⚖️ Суд",
	"law":      "📜 Референдум",
	"marginal": "🚫 Признание маргиналом",
	"election": "👑 Выборы президента",
}

// по своему делу голосовать нельзя
func targetedKind(kind string) bool {
	return kind == "revoke" || kind == "accuse" || kind == "marginal"
}

type poll struct {
	ID, GuildID          int64
	Kind, Status         string
	CreatorID            int64
	TargetID, LawyerID   *int64
	Amount               *int
	Text                 *string
	ChannelID, MessageID *int64
	Result               *string
	EndsAt               time.Time
}

func (p *poll) target() int64 {
	if p.TargetID == nil {
		return 0
	}
	return *p.TargetID
}

func (p *poll) amount() int {
	if p.Amount == nil {
		return 0
	}
	return *p.Amount
}

func (p *poll) text() string {
	if p.Text == nil {
		return ""
	}
	return *p.Text
}

const pollColumns = "id, guild_id, kind, status, creator_id, target_id, lawyer_id, amount, text, channel_id, message_id, result, ends_at"

func getPoll(id int64) (*poll, error) {
	rows, err := fetch[poll]("SELECT "+pollColumns+" FROM polls WHERE id = $1", id)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func pollDescription(p *poll) string {
	switch p.Kind {
	case "revoke":
		return fmt.Sprintf("Лишить %s гражданства?\nНужно, чтобы «за» проголосовали ⅔ всех граждан (кроме замороженных и самого обвиняемого).", mention(p.target()))
	case "accuse":
		lawyer := "не нашлось"
		if p.LawyerID != nil {
			lawyer = mention(*p.LawyerID)
		}
		return fmt.Sprintf("%s обвиняет %s: %s\nНаказание: **%d** отжиманий\nАдвокат: %s\n\n"+
			"Виновен, если «за» проголосовали больше половины граждан (кроме обвиняемого). "+
			"Иначе **%d** отжиманий получает обвинитель за клевету.",
			mention(p.CreatorID), mention(p.target()), p.text(), p.amount(), lawyer, p.amount())
	case "law":
		return fmt.Sprintf("%s предлагает закон:\n> %s\n\nПринимается простым большинством голосов.", mention(p.CreatorID), p.text())
	case "marginal":
		return fmt.Sprintf("Признать %s маргиналом? Он не будет участвовать в ближайшей выдаче наград.\n"+
			"Нужно, чтобы «за» проголосовали больше половины граждан (кроме него самого).", mention(p.target()))
	}
	if p.Status == "nominating" {
		return fmt.Sprintf("Граждане, выдвигайтесь командой `/nominate` до <t:%d:f>.", p.EndsAt.Unix())
	}
	return "Голосуй за кандидата в меню ниже. Побеждает тот, у кого больше голосов."
}

func candidates(pollID int64) ([]int64, error) {
	return column[int64]("SELECT user_id FROM election_candidates WHERE poll_id = $1", pollID)
}

func pollEmbed(p *poll) (*discordgo.MessageEmbed, error) {
	embed := &discordgo.MessageEmbed{Title: fmt.Sprintf("%s #%d", kindTitles[p.Kind], p.ID), Description: pollDescription(p), Color: colorPurple}
	rows, err := fetch[struct {
		Choice string
		N      int
	}]("SELECT choice, COUNT(*)::int FROM poll_votes WHERE poll_id = $1 GROUP BY choice", p.ID)
	if err != nil {
		return nil, err
	}
	tally := map[string]int{}
	for _, row := range rows {
		tally[row.Choice] = row.N
	}
	if p.Kind == "election" {
		cands, err := candidates(p.ID)
		if err != nil {
			return nil, err
		}
		var lines []string
		for _, c := range cands {
			if p.Status == "nominating" {
				lines = append(lines, mention(c))
			} else {
				lines = append(lines, fmt.Sprintf("%s — %d", mention(c), tally[idStr(c)]))
			}
		}
		if p.Status == "nominating" {
			embed.Fields = append(embed.Fields, field("Кандидаты", orEmpty(strings.Join(lines, "\n"), "пока никого"), false))
		} else {
			embed.Fields = append(embed.Fields, field("Голоса", orEmpty(strings.Join(lines, "\n"), "—"), false))
		}
	} else {
		embed.Fields = append(embed.Fields, field("✅ За", strconv.Itoa(tally["yes"]), true), field("❌ Против", strconv.Itoa(tally["no"]), true))
	}
	if p.Status == "open" || p.Status == "nominating" {
		embed.Fields = append(embed.Fields, field("Итог", fmt.Sprintf("<t:%d:R>", p.EndsAt.Unix()), false))
	} else {
		result := "—"
		if p.Result != nil && *p.Result != "" {
			result = *p.Result
		}
		embed.Fields = append(embed.Fields, field("Итог", result, false))
	}
	return embed, nil
}

func yesNoComponents(pollID int64) []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "За", Emoji: &discordgo.ComponentEmoji{Name: "✅"}, Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("vote:%d:yes", pollID)},
		discordgo.Button{Label: "Против", Emoji: &discordgo.ComponentEmoji{Name: "❌"}, Style: discordgo.DangerButton, CustomID: fmt.Sprintf("vote:%d:no", pollID)},
	}}}
}

func openPollExists(guildID int64, kind string, target *int64) (bool, error) {
	var one int
	return fetchOne(`SELECT 1 FROM polls WHERE guild_id = $1 AND kind = $2 AND status IN ('open', 'nominating')
		AND target_id IS NOT DISTINCT FROM $3::bigint`, []any{guildID, kind, target}, &one)
}

func createPoll(r *request, kind string, hours int, status string, target, lawyer *int64, amount *int, text *string) error {
	var pollID int64
	if _, err := fetchOne(`INSERT INTO polls (guild_id, kind, status, creator_id, target_id, lawyer_id, amount, text, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		[]any{r.guildID(), kind, status, r.userID(), target, lawyer, amount, text, time.Now().Add(time.Duration(hours) * time.Hour)}, &pollID); err != nil {
		return err
	}
	p, err := getPoll(pollID)
	if err != nil {
		return err
	}
	embed, err := pollEmbed(p)
	if err != nil {
		return err
	}
	data := &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}}
	if status == "open" {
		data.Components = yesNoComponents(pollID)
	}
	if err := r.respond(data, discordgo.InteractionResponseChannelMessageWithSource); err != nil {
		return err
	}
	msg, err := session.InteractionResponse(r.i.Interaction)
	if err != nil {
		return err
	}
	return exec("UPDATE polls SET channel_id = $1, message_id = $2 WHERE id = $3", parseID(msg.ChannelID), parseID(msg.ID), pollID)
}

func handleVote(r *request, pollID int64, choice string) error {
	p, err := getPoll(pollID)
	if err != nil {
		return err
	}
	if p == nil || p.Status != "open" || !p.EndsAt.After(time.Now()) {
		return r.replyHidden("Голосование уже закрыто.")
	}
	if ok, err := requireActiveCitizen(r); !ok {
		return err
	}
	if targetedKind(p.Kind) && r.userID() == p.target() {
		return r.replyHidden("По своему делу голосовать нельзя.")
	}
	if p.Kind == "election" {
		var one int
		ok, err := fetchOne("SELECT 1 FROM election_candidates WHERE poll_id = $1 AND user_id = $2", []any{pollID, parseID(choice)}, &one)
		if err != nil {
			return err
		}
		if !ok {
			return r.replyHidden("Такого кандидата нет.")
		}
	} else if choice != "yes" && choice != "no" {
		return nil
	}
	// голос окончательный: повторное нажатие не меняет его
	var inserted string
	found, err := fetchOne(`INSERT INTO poll_votes (poll_id, voter_id, choice) VALUES ($1, $2, $3)
		ON CONFLICT (poll_id, voter_id) DO NOTHING RETURNING choice`, []any{pollID, r.userID(), choice}, &inserted)
	if err != nil {
		return err
	}
	if !found {
		return r.replyHidden("Ты уже проголосовал — голос менять нельзя.")
	}
	embed, err := pollEmbed(p)
	if err != nil {
		return err
	}
	if err := r.respond(&discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, Components: r.i.Message.Components},
		discordgo.InteractionResponseUpdateMessage); err != nil {
		return err
	}
	return r.followup("Голос учтён ✅ Изменить его уже нельзя.", nil, true)
}

// announcePoll обновляет сообщение голосования (убирает кнопки) и пишет итог ответом на него.
func announcePoll(p *poll, note string) error {
	if p.ChannelID == nil || p.MessageID == nil {
		return nil
	}
	channel, message := idStr(*p.ChannelID), idStr(*p.MessageID)
	embed, err := pollEmbed(p)
	if err != nil {
		return err
	}
	noComponents := []discordgo.MessageComponent{}
	embeds := []*discordgo.MessageEmbed{embed}
	_, err = session.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: message, Channel: channel, Embeds: &embeds, Components: &noComponents})
	if err == nil {
		_, err = session.ChannelMessageSendReply(channel, note, &discordgo.MessageReference{MessageID: message, ChannelID: channel})
	}
	if err != nil {
		_, err = session.ChannelMessageSendComplex(channel, &discordgo.MessageSend{Content: note, Embeds: embeds})
	}
	return err
}

func resolvePoll(p *poll) error {
	gid, kind, target, amount := p.GuildID, p.Kind, p.target(), p.amount()
	active, err := activeCitizens(gid)
	if err != nil {
		return err
	}
	voters := slices.DeleteFunc(active, func(u int64) bool { return targetedKind(kind) && u == target })
	rows, err := fetch[struct {
		VoterID int64
		Choice  string
	}]("SELECT voter_id, choice FROM poll_votes WHERE poll_id = $1", p.ID)
	if err != nil {
		return err
	}
	votes := map[int64]string{} // только действующие голоса
	for _, v := range rows {
		if slices.Contains(voters, v.VoterID) {
			votes[v.VoterID] = v.Choice
		}
	}
	yes, no := 0, 0
	for _, c := range votes {
		switch c {
		case "yes":
			yes++
		case "no":
			no++
		}
	}
	n := len(voters)
	title := fmt.Sprintf("%s #%d", kindTitles[kind], p.ID)
	var passed bool
	var result string

	switch kind {
	case "revoke":
		passed = n > 0 && yes*3 >= 2*n
		if passed {
			if err := exec("DELETE FROM citizens WHERE guild_id = $1 AND user_id = $2", gid, target); err != nil {
				return err
			}
			result = fmt.Sprintf("%s лишён гражданства (%d из %d за).", mention(target), yes, n)
		} else {
			result = fmt.Sprintf("%s остаётся гражданином (%d из %d за, нужно ⅔).", mention(target), yes, n)
		}
	case "accuse":
		passed = yes*2 > n
		punished, verdict := p.CreatorID, "клевета"
		if passed {
			punished, verdict = target, "виновен"
		}
		reason := fmt.Sprintf("Суд #%d: %s", p.ID, verdict)
		if err := addEntry(gid, punished, "owe", amount, botUserID, &reason, "court"); err != nil {
			return err
		}
		if passed {
			result = fmt.Sprintf("Виновен (%d из %d за). %s получает **%d** отжиманий.", yes, n, mention(target), amount)
		} else {
			result = fmt.Sprintf("Не виновен (%d из %d за). %s получает **%d** отжиманий за клевету.", yes, n, mention(p.CreatorID), amount)
		}
	case "marginal":
		passed = yes*2 > n
		if passed {
			if err := exec("INSERT INTO marginals (guild_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, target); err != nil {
				return err
			}
			result = fmt.Sprintf("%s признан маргиналом (%d из %d за) и пропустит ближайшую выдачу наград.", mention(target), yes, n)
		} else {
			result = fmt.Sprintf("%s не маргинал (%d из %d за).", mention(target), yes, n)
		}
	case "law":
		passed = yes > no
		if passed {
			if err := exec("INSERT INTO laws (guild_id, text, poll_id) VALUES ($1, $2, $3)", gid, p.text(), p.ID); err != nil {
				return err
			}
			result = fmt.Sprintf("Закон принят (%d за / %d против). Смотри `/laws`.", yes, no)
		} else {
			result = fmt.Sprintf("Закон отклонён (%d за / %d против).", yes, no)
		}
	default: // election
		tally := map[string]int{}
		best := 0
		for _, c := range votes {
			tally[c]++
			best = max(best, tally[c])
		}
		passed = len(tally) > 0
		if passed {
			var top []int64
			for c, v := range tally {
				if v == best {
					top = append(top, parseID(c))
				}
			}
			winner := top[rand.IntN(len(top))]
			if err := exec("INSERT INTO presidents (guild_id, user_id) VALUES ($1, $2)", gid, winner); err != nil {
				return err
			}
			result = fmt.Sprintf("Новый президент — %s (%d голосов)", mention(winner), best)
			if len(top) > 1 {
				result += " — по жребию среди равных."
			} else {
				result += "."
			}
		} else {
			result = "Никто не проголосовал, президент не выбран."
		}
	}

	status := "failed"
	if passed {
		status = "passed"
	}
	if err := exec("UPDATE polls SET status = $1, result = $2 WHERE id = $3", status, result, p.ID); err != nil {
		return err
	}
	if err := syncRoles(gid); err != nil {
		log.Printf("Не удалось обновить роли: %v", err)
	}
	p, err = getPoll(p.ID)
	if err != nil {
		return err
	}
	return announcePoll(p, fmt.Sprintf("**%s:** %s", title, result))
}

func memberName(guildID, userID int64) string {
	m, err := session.GuildMember(idStr(guildID), idStr(userID))
	if err != nil {
		return idStr(userID)
	}
	return orEmpty(displayName(m, m.User), idStr(userID))
}

func startElectionVoting(p *poll) error {
	cands, err := candidates(p.ID)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		if err := exec("UPDATE polls SET status = 'failed', result = 'Никто не выдвинулся.' WHERE id = $1", p.ID); err != nil {
			return err
		}
		p, err = getPoll(p.ID)
		if err != nil {
			return err
		}
		return announcePoll(p, fmt.Sprintf("**%s #%d:** никто не выдвинулся.", kindTitles["election"], p.ID))
	}
	if err := exec("UPDATE polls SET status = 'open', ends_at = $1 WHERE id = $2", time.Now().Add(electionHours*time.Hour), p.ID); err != nil {
		return err
	}
	p, err = getPoll(p.ID)
	if err != nil || p.ChannelID == nil {
		return err
	}
	channel := idStr(*p.ChannelID)
	if p.MessageID != nil {
		none := []discordgo.MessageComponent{}
		_, _ = session.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: idStr(*p.MessageID), Channel: channel, Components: &none})
	}
	var options []discordgo.SelectMenuOption
	for _, c := range cands[:min(len(cands), 25)] {
		options = append(options, discordgo.SelectMenuOption{Label: truncate(memberName(p.GuildID, c), 100), Value: idStr(c)})
	}
	embed, err := pollEmbed(p)
	if err != nil {
		return err
	}
	msg, err := session.ChannelMessageSendComplex(channel, &discordgo.MessageSend{
		Content: "🗳️ Выдвижение окончено — голосуем!", Embeds: []*discordgo.MessageEmbed{embed},
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{CustomID: fmt.Sprintf("elect:%d", p.ID), Placeholder: "Голосовать за…", Options: options},
		}}},
	})
	if err != nil {
		return err
	}
	return exec("UPDATE polls SET channel_id = $1, message_id = $2 WHERE id = $3", parseID(msg.ChannelID), parseID(msg.ID), p.ID)
}

func pollsLoop() {
	rows, err := fetch[poll]("SELECT " + pollColumns + " FROM polls WHERE status IN ('open', 'nominating') AND ends_at <= now() ORDER BY ends_at")
	if err != nil {
		log.Printf("Ошибка подведения итогов голосований: %v", err)
		return
	}
	for i := range rows {
		p := &rows[i]
		log.Printf("Подвожу итог голосования #%d (%s)", p.ID, p.Kind)
		if p.Status == "nominating" {
			err = startElectionVoting(p)
		} else {
			err = resolvePoll(p)
		}
		if err != nil {
			log.Printf("Ошибка подведения итогов голосования #%d: %v", p.ID, err)
		}
	}
}

const presidentShield = "🛡️ Президента нельзя забанить (Конституция, ст. 3)."

func accuseHandler(r *request) error {
	if ok, err := requireActiveCitizen(r); !ok {
		return err
	}
	gid := r.guildID()
	user := r.userOption("user")
	if user.Bot || user.ID == r.userID() {
		return r.replyHidden("Себя и ботов обвинять нельзя.")
	}
	if pres, err := isPresident(gid, user.ID); err != nil {
		return err
	} else if pres {
		return r.replyHidden(presidentShield)
	}
	active, err := activeCitizens(gid)
	if err != nil {
		return err
	}
	lawyers := slices.DeleteFunc(active, func(u int64) bool { return u == r.userID() || u == user.ID })
	var lawyer *int64
	if len(lawyers) > 0 {
		lawyer = &lawyers[rand.IntN(len(lawyers))]
	}
	amount, reason := r.intOption("pushups", 0), r.stringOption("reason")
	return createPoll(r, "accuse", r.intOption("hours", 24), "open", &user.ID, lawyer, &amount, &reason)
}

// targetPollChecks — общие проверки для голосований о гражданине.
func targetPollChecks(r *request, kind, notCitizen string) (*target, error) {
	if ok, err := requireActiveCitizen(r); !ok {
		return nil, err
	}
	gid := r.guildID()
	user := r.userOption("user")
	citizen, err := isCitizen(gid, user.ID)
	if err != nil {
		return nil, err
	}
	if !citizen {
		return nil, r.replyHidden(fmt.Sprintf(notCitizen, user.mention()))
	}
	if pres, err := isPresident(gid, user.ID); err != nil {
		return nil, err
	} else if pres {
		return nil, r.replyHidden(presidentShield)
	}
	if exists, err := openPollExists(gid, kind, &user.ID); err != nil {
		return nil, err
	} else if exists {
		return nil, r.replyHidden("Такое голосование уже идёт.")
	}
	return &user, nil
}

func revokeHandler(r *request) error {
	user, err := targetPollChecks(r, "revoke", "%s не гражданин.")
	if user == nil {
		return err
	}
	return createPoll(r, "revoke", r.intOption("hours", 24), "open", &user.ID, nil, nil, nil)
}

func marginalHandler(r *request) error {
	user, err := targetPollChecks(r, "marginal", "%s не гражданин — в наградах он и так не участвует.")
	if user == nil {
		return err
	}
	return createPoll(r, "marginal", r.intOption("hours", 24), "open", &user.ID, nil, nil, nil)
}

func proposeHandler(r *request) error {
	if ok, err := requireActiveCitizen(r); !ok {
		return err
	}
	text := r.stringOption("text")
	return createPoll(r, "law", lawHours, "open", nil, nil, nil, &text)
}

func electionHandler(r *request) error {
	if ok, err := requireActiveCitizen(r); !ok {
		return err
	}
	gid := r.guildID()
	if exists, err := openPollExists(gid, "election", nil); err != nil {
		return err
	} else if exists {
		return r.replyHidden("Выборы уже идут.")
	}
	p, err := currentPresident(gid)
	if err != nil {
		return err
	}
	if p != nil {
		allowed := p.ElectedAt.Add(presidentTermDays * 24 * time.Hour)
		if time.Now().Before(allowed) {
			return r.replyHidden(fmt.Sprintf("Срок президента %s ещё не истёк. Перевыборы можно начать <t:%d:R>.", mention(p.UserID), allowed.Unix()))
		}
	}
	return createPoll(r, "election", electionHours, "nominating", nil, nil, nil, nil)
}

func nominateHandler(r *request) error {
	if ok, err := requireActiveCitizen(r); !ok {
		return err
	}
	var pollID int64
	found, err := fetchOne("SELECT id FROM polls WHERE guild_id = $1 AND kind = 'election' AND status = 'nominating'", []any{r.guildID()}, &pollID)
	if err != nil {
		return err
	}
	if !found {
		return r.replyHidden("Сейчас нет выдвижения. Начать выборы: `/election`.")
	}
	if err := exec("INSERT INTO election_candidates (poll_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", pollID, r.userID()); err != nil {
		return err
	}
	if err := r.reply(fmt.Sprintf("🎙️ %s баллотируется в президенты!", r.userMention())); err != nil {
		return err
	}
	p, err := getPoll(pollID)
	if err != nil || p.ChannelID == nil || p.MessageID == nil {
		return err
	}
	embed, err := pollEmbed(p)
	if err != nil {
		return err
	}
	embeds := []*discordgo.MessageEmbed{embed}
	_, _ = session.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: idStr(*p.MessageID), Channel: idStr(*p.ChannelID), Embeds: &embeds})
	return nil
}

// ---------- роли по итогам и награды ----------

// periodScores: назначено за матчи Dota/PUBG и суд, число игр Dota + PUBG за период [start, end).
func periodScores(guildID int64, start, end time.Time) (scores, games map[int64]int, err error) {
	scores, games = map[int64]int{}, map[int64]int{}
	type row struct {
		UserID int64
		S, G   int
	}
	for _, table := range []string{"dota_matches", "pubg_matches"} {
		rows, err := fetch[row]("SELECT user_id, SUM(pushups)::int, COUNT(*)::int FROM "+table+
			" WHERE guild_id = $1 AND played_at >= $2 AND played_at < $3 GROUP BY user_id", guildID, start, end)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			scores[r.UserID] += r.S
			games[r.UserID] += r.G
		}
	}
	court, err := fetch[struct {
		UserID int64
		S      int
	}](`SELECT user_id, SUM(amount)::int FROM entries WHERE guild_id = $1 AND kind = 'owe' AND source = 'court'
		AND created_at >= $2 AND created_at < $3 GROUP BY user_id`, guildID, start, end)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range court {
		scores[r.UserID] += r.S
	}
	return scores, games, nil
}

type ranked struct {
	UserID int64
	Score  int
}

// eligibleRanking: граждане без заморозки, сыгравшие больше awardMinGames игр.
func eligibleRanking(guildID int64, start, end time.Time, skipMarginals bool) ([]ranked, error) {
	scores, games, err := periodScores(guildID, start, end)
	if err != nil {
		return nil, err
	}
	users, err := activeCitizens(guildID)
	if err != nil {
		return nil, err
	}
	var marginals []int64
	if skipMarginals {
		if marginals, err = marginalIDs(guildID); err != nil {
			return nil, err
		}
	}
	var out []ranked
	for _, u := range users {
		if games[u] > awardMinGames && !slices.Contains(marginals, u) {
			out = append(out, ranked{u, scores[u]})
		}
	}
	return out, nil
}

func leaders(ranking []ranked, best bool) ([]int64, int) {
	value := ranking[0].Score
	for _, r := range ranking {
		if (best && r.Score > value) || (!best && r.Score < value) {
			value = r.Score
		}
	}
	var ids []int64
	for _, r := range ranking {
		if r.Score == value {
			ids = append(ids, r.UserID)
		}
	}
	return ids, value
}

// syncRankRoles: Топ руинер и Порядочный гражданин — по назначенным отжиманиям за последние 30 дней.
func syncRankRoles(guildID int64) error {
	end := time.Now()
	ranking, err := eligibleRanking(guildID, end.AddDate(0, 0, -30), end, false)
	if err != nil {
		return err
	}
	var top, bottom []int64
	if len(ranking) >= 2 {
		top, _ = leaders(ranking, true)
		bottom, _ = leaders(ranking, false)
	}
	if err := setRoleHolders(guildID, "ruiner", top); err != nil {
		return err
	}
	return setRoleHolders(guildID, "decent", bottom)
}

func addMonths(t time.Time, n int) time.Time {
	return time.Date(t.Year(), t.Month()+time.Month(n), 1, 0, 0, 0, 0, t.Location())
}

func awardText(guildID int64, start, end time.Time, prize string) (string, error) {
	ranking, err := eligibleRanking(guildID, start, end, true)
	if err != nil {
		return "", err
	}
	if len(ranking) == 0 {
		return fmt.Sprintf("Никто из граждан не сыграл больше %d пати-игр — %s никому.", awardMinGames, prize), nil
	}
	top, topValue := leaders(ranking, true)
	bottom, bottomValue := leaders(ranking, false)
	return fmt.Sprintf("😈 Топ-1 (больше всех назначено, %d): %s — %s\n😇 Боттом-1 (меньше всех назначено, %d): %s — %s",
		topValue, mentions(top), prize, bottomValue, mentions(bottom), prize), nil
}

// maybeRunAwards раз в месяц (1-го числа или при первом запуске после него) подводит итоги прошлого месяца.
func maybeRunAwards(guildID int64) error {
	now := time.Now().In(cfg.Location)
	monthEnd := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, cfg.Location)
	monthStart := addMonths(monthEnd, -1)
	period := fmt.Sprintf("%d-%02d", monthStart.Year(), int(monthStart.Month()))
	var claimed string
	found, err := fetchOne("INSERT INTO award_runs (guild_id, period) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING period", []any{guildID, period}, &claimed)
	if err != nil || !found {
		return err // уже подводили
	}
	var firstLink *time.Time
	if _, err := fetchOne(`SELECT min(linked_at) FROM (SELECT linked_at FROM dota_links WHERE guild_id = $1
		UNION ALL SELECT linked_at FROM pubg_links WHERE guild_id = $1) AS l`, []any{guildID}, &firstLink); err != nil {
		return err
	}
	if firstLink == nil || !monthEnd.After(*firstLink) {
		return nil // месяц целиком до начала учёта матчей
	}
	month, err := awardText(guildID, monthStart, monthEnd, "ваучер Steam 10 $")
	if err != nil {
		return err
	}
	parts := []string{fmt.Sprintf("**Итоги за %s %d**", months[monthStart.Month()-1], monthStart.Year()), month}
	if m := monthEnd.Month(); m == time.January || m == time.April || m == time.July || m == time.October {
		quarterStart := addMonths(monthEnd, -3)
		quarter, err := awardText(guildID, quarterStart, monthEnd, "грамота и медаль 🏅")
		if err != nil {
			return err
		}
		parts = append(parts, "", fmt.Sprintf("**Итоги квартала (%s — %s)**", months[quarterStart.Month()-1], months[monthStart.Month()-1]), quarter)
	}
	marginals, err := marginalIDs(guildID)
	if err != nil {
		return err
	}
	if len(marginals) > 0 {
		parts = append(parts, "", "Маргиналы пропустили эту выдачу и снова участвуют: "+mentions(marginals))
	}
	if err := exec("DELETE FROM marginals WHERE guild_id = $1", guildID); err != nil {
		return err
	}
	if err := syncRoles(guildID); err != nil {
		log.Printf("Не удалось обновить роли: %v", err)
	}
	if g, ok := channelGuild(cfg.CivicChannelID); ok && g == guildID {
		sendEmbeds(cfg.CivicChannelID, []*discordgo.MessageEmbed{{
			Title: "🏆 Награды", Description: strings.Join(parts, "\n"), Color: colorGold, Footer: footer("Награды выдаёт админ"),
		}})
	}
	return nil
}

func civicLoop() {
	session.State.RLock()
	ids := make([]int64, 0, len(session.State.Guilds))
	for _, g := range session.State.Guilds {
		ids = append(ids, parseID(g.ID))
	}
	session.State.RUnlock()
	for _, id := range ids {
		if err := maybeRunAwards(id); err != nil {
			log.Printf("Ошибка наград на %d: %v", id, err)
		}
		if err := syncRankRoles(id); err != nil {
			log.Printf("Ошибка ролей на %d: %v", id, err)
		}
	}
}
