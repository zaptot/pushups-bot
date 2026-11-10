package bot

import (
	"cmp"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"pushups-bot/internal/economy"
	"pushups-bot/internal/shared"
	"pushups-bot/internal/store"
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }
func strPtr(v string) *string     { return &v }

func userOpt(name, desc string, required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionUser, Name: name, Description: desc, Required: required}
}

func intOpt(name, desc string, required bool, minV, maxV int) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionInteger, Name: name, Description: desc, Required: required,
		MinValue: floatPtr(float64(minV)), MaxValue: float64(maxV),
	}
}

func strOpt(name, desc string, required bool) *discordgo.ApplicationCommandOption {
	return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: desc, Required: required}
}

func hoursOpt() *discordgo.ApplicationCommandOption {
	return intOpt("hours", "Сколько часов идёт голосование (по умолчанию 24)", false, 1, 168)
}

type command struct {
	def     *discordgo.ApplicationCommand
	handler func(*request) error
}

func cmd(name, desc string, handler func(*request) error, opts ...*discordgo.ApplicationCommandOption) command {
	return command{&discordgo.ApplicationCommand{Name: name, Description: desc, Options: opts}, handler}
}

func commandList() []command {
	amountDesc := fmt.Sprintf("Сколько отжиманий сделал (минимум %d)", doneMin)
	board := &discordgo.ApplicationCommandOption{
		Type: discordgo.ApplicationCommandOptionString, Name: "board", Description: "Какой лидерборд показать", Required: true,
		Choices: []*discordgo.ApplicationCommandOptionChoice{
			{Name: "Больше всего осталось (должники)", Value: "remaining"},
			{Name: "Больше всего сделано", Value: "done"},
		},
	}
	lawText := strOpt("text", "Текст закона", true)
	lawText.MinLength, lawText.MaxLength = intPtr(5), 1000

	manageGuild := int64(discordgo.PermissionManageGuild)
	noDM := false
	founderCmd := cmd("founder", "(Админ) Назначить гражданина-основателя", founderHandler, userOpt("user", "Кого сделать гражданином", true))
	founderCmd.def.DefaultMemberPermissions = &manageGuild
	founderCmd.def.DMPermission = &noDM

	return []command{
		cmd("owe", "Назначить участнику отжимания (проигранный спор)", oweHandler,
			userOpt("user", "Кто должен отжиматься", true), intOpt("amount", "Сколько отжиманий", true, 1, 10000),
			strOpt("reason", "За что, например «3 кила × 10»", false)),
		cmd("done", "Записать сделанные отжимания", doneHandler, intOpt("amount", amountDesc, true, doneMin, 10000)),
		cmd("status", "Сколько отжиманий осталось и сделано", statusHandler, userOpt("user", "Чей статус (по умолчанию — твой)", false)),
		cmd("top", "Лидерборд за последний год", topHandler, board),
		cmd("history", "Последние записи участника", historyHandler, userOpt("user", "Чья история (по умолчанию — твоя)", false)),
		cmd("undo", "Отменить свою последнюю запись (если ошибся)", undoHandler),
		cmd("casino", "Открыть казино на поинты: рулетка, Plinko, блэкджек, рокетка, магазин", casinoHandler),
		cmd("shop", "Магазин: что можно купить за поинты", shopHandler),
		cmd("buy", "Купить товар за поинты", buyHandler, shopItemOpt()),

		cmd("linkdota", "Привязать Dota-аккаунт к участнику", linkDotaHandler, userOpt("user", "Участник Discord", true),
			strOpt("account", "Dota ID (Friend ID), Steam64 ID или ссылка на профиль Dotabuff/OpenDota", true)),
		cmd("unlinkdota", "Отвязать Dota-аккаунт от участника", unlinkDotaHandler, userOpt("user", "Участник Discord", true)),
		cmd("dotalinks", "Список привязанных Dota-аккаунтов", dotaLinksHandler),
		cmd("calculate_dota", "Посчитать новые пати-матчи Dota и начислить отжимания", calculateDotaHandler),
		cmd("dotasolo_on", "Получать отжимания и за соло-игры в Dota", func(r *request) error { return setDotaSolo(r, true) }),
		cmd("dotasolo_off", "Снова получать отжимания только за пати-игры в Dota", func(r *request) error { return setDotaSolo(r, false) }),

		cmd("linkpubg", "Привязать PUBG-аккаунт к участнику", linkPubgHandler, userOpt("user", "Участник Discord", true),
			strOpt("nickname", "Ник в PUBG (с учётом регистра)", true)),
		cmd("unlinkpubg", "Отвязать PUBG-аккаунт от участника", unlinkPubgHandler, userOpt("user", "Участник Discord", true)),
		cmd("pubglinks", "Список привязанных PUBG-аккаунтов", pubgLinksHandler),
		cmd("calculate_pubg", "Посчитать новые пати-матчи PUBG и начислить отжимания", calculatePubgHandler),

		founderCmd,
		cmd("vouch", "Поручиться за участника, чтобы он стал гражданином", vouchHandler, userOpt("user", "За кого ручаешься", true)),
		cmd("citizens", "Список граждан", citizensHandler),
		cmd("laws", "Конституция и принятые законы", lawsHandler),
		cmd("accuse", "Обвинить участника (решают граждане)", accuseHandler, userOpt("user", "Кого обвиняешь", true),
			intOpt("pushups", "Наказание в отжиманиях", true, 1, 1000), strOpt("reason", "В чём обвинение", true), hoursOpt()),
		cmd("revoke", "Голосование за лишение гражданства (нужно ⅔ граждан)", revokeHandler, userOpt("user", "Кого лишить гражданства", true), hoursOpt()),
		cmd("marginal", "Голосование: признать гражданина маргиналом (без наград)", marginalHandler, userOpt("user", "Кого признать маргиналом", true), hoursOpt()),
		cmd("propose", "Предложить закон (референдум на 1 день)", proposeHandler, lawText),
		cmd("election", "Начать выборы президента", electionHandler),
		cmd("nominate", "Выдвинуть себя в президенты (во время выборов)", nominateHandler),
	}
}

var handlers = map[string]func(*request) error{}

func onInteraction(_ *discordgo.Session, i *discordgo.InteractionCreate) {
	defer shared.Protect("обработчике команды")
	r := &request{i: i}
	var err error
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		data := i.ApplicationCommandData()
		log.Printf("Команда /%s от %s (%d)", data.Name, r.user().Username, r.userID())
		h, ok := handlers[data.Name]
		if !ok {
			return
		}
		r.options = make(map[string]*discordgo.ApplicationCommandInteractionDataOption, len(data.Options))
		for _, o := range data.Options {
			r.options[o.Name] = o
		}
		if err = h(r); err == nil {
			log.Printf("Команда /%s от %s выполнена", data.Name, r.user().Username)
		}
	case discordgo.InteractionMessageComponent:
		data := i.MessageComponentData()
		parts := strings.Split(data.CustomID, ":")
		switch {
		case parts[0] == "vote" && len(parts) == 3:
			err = handleVote(r, shared.ParseID(parts[1]), parts[2])
		case parts[0] == "elect" && len(parts) == 2 && len(data.Values) > 0:
			err = handleVote(r, shared.ParseID(parts[1]), data.Values[0])
		}
	}
	if err != nil {
		log.Printf("Ошибка команды: %v", err)
		const text = "Что-то пошло не так, попробуй ещё раз."
		if replyErr := r.replyHidden(text); replyErr != nil {
			_ = r.followup(text, nil, true)
		}
	}
}

func oweHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	user := r.userOption("user")
	if user.Bot {
		return r.replyHidden("Боты не отжимаются 🤖")
	}
	amount := r.intOption("amount", 0)
	reason := r.stringOption("reason")
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	if err := store.AddEntry(r.guildID(), user.ID, "owe", amount, r.userID(), reasonPtr, "manual"); err != nil {
		return err
	}
	st, err := store.UserStats(r.guildID(), user.ID)
	if err != nil {
		return err
	}
	text := fmt.Sprintf("💪 %s назначил %s **%d** отжиманий", r.userMention(), user.mention(), amount)
	if reason != "" {
		text += " — " + reason
	}
	return r.reply(text + fmt.Sprintf("\nТеперь осталось: **%d**", st.Remaining()))
}

func doneSummary(r *request, counted int) (string, error) {
	st, err := store.UserStats(r.guildID(), r.userID())
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("✅ %s засчитано **%d** отжиманий (за год: %d)\n", r.userMention(), counted, st.Done)
	if st.Remaining() > 0 {
		return text + fmt.Sprintf("Осталось: **%d**", st.Remaining()), nil
	}
	return text + "Долгов нет — красавчик! 🏆", nil
}

// announceCitizenship: возможно, теперь выполнены условия гражданства.
func announceCitizenship(r *request) {
	became, note, err := tryNaturalize(r.guildID(), r.userID())
	if err != nil {
		log.Printf("Не удалось проверить гражданство: %v", err)
	} else if became {
		if err := r.followup(note, nil, false); err != nil {
			log.Printf("Не удалось объявить гражданство: %v", err)
		}
	}
}

func doneHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	amount := r.intOption("amount", 0)
	if err := store.AddEntry(r.guildID(), r.userID(), "done", amount, r.userID(), nil, "manual"); err != nil {
		return err
	}
	text, err := doneSummary(r, amount)
	if err != nil {
		return err
	}
	if err := r.reply(text); err != nil {
		return err
	}
	announceCitizenship(r)
	return nil
}

func statusHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	t := r.userOption("user")
	st, err := store.UserStats(r.guildID(), t.ID)
	if err != nil {
		return err
	}
	bal, err := economy.Balance(r.guildID(), t.ID)
	if err != nil {
		return err
	}
	civic := "не гражданин"
	citizen, err := isCitizen(r.guildID(), t.ID)
	if err != nil {
		return err
	}
	if citizen {
		civic = "🏛️ гражданин"
		if st.Remaining() > freezeDebt {
			civic = "❄️ гражданин (заморожен)"
		}
		president, err := isPresident(r.guildID(), t.ID)
		if err != nil {
			return err
		}
		if president {
			civic = "👑 президент, " + civic
		}
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: "Отжимания: " + t.Name, Color: colorOrange,
		Fields: []*discordgo.MessageEmbedField{
			field("Осталось", fmt.Sprint(st.Remaining()), true),
			field("Сделано", fmt.Sprint(st.Done), true),
			field("Назначено", fmt.Sprint(st.Owed), true),
			field("💰 Поинты", economy.Points(bal), true),
			field("Статус", civic, false),
		},
		Footer: footer(fmt.Sprintf("За последние %d дней", windowDays)),
	}, false)
}

func topHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	stats, err := store.GuildStats(r.guildID())
	if err != nil {
		return err
	}
	remaining := r.stringOption("board") == "remaining"
	title := "🏆 Сделано отжиманий"
	if remaining {
		title = "😰 Должники: осталось отжиманий"
	}
	type place struct {
		score int
		id    int64
	}
	var ranking []place
	for id, st := range stats {
		score := st.Done
		if remaining {
			score = st.Remaining()
		}
		if score > 0 {
			ranking = append(ranking, place{score, id})
		}
	}
	slices.SortFunc(ranking, func(a, b place) int { return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(b.id, a.id)) })
	if len(ranking) > topSize {
		ranking = ranking[:topSize]
	}
	medals := map[int]string{1: "🥇", 2: "🥈", 3: "🥉"}
	var lines []string
	for i, p := range ranking {
		medal, ok := medals[i+1]
		if !ok {
			medal = fmt.Sprintf("`%d.`", i+1)
		}
		line := fmt.Sprintf("%s %s — **%d**", medal, mention(p.id), p.score)
		lines = append(lines, line)
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: title, Description: shared.OrEmpty(strings.Join(lines, "\n"), "Пока пусто."), Color: colorGold,
		Footer: footer(fmt.Sprintf("За последние %d дней", windowDays)),
	}, false)
}

func historyHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	t := r.userOption("user")
	rows, err := store.Fetch[struct {
		Kind      string
		Amount    int
		AuthorID  int64
		Reason    *string
		CreatedAt time.Time
	}](`SELECT kind, amount, author_id, reason, created_at FROM entries
		WHERE guild_id = $1 AND user_id = $2 ORDER BY id DESC LIMIT 10`, r.guildID(), t.ID)
	if err != nil {
		return err
	}
	var lines []string
	for _, e := range rows {
		line := fmt.Sprintf("<t:%d:d> ✅ сделал %d", e.CreatedAt.Unix(), e.Amount)
		if e.Kind == "owe" {
			line = fmt.Sprintf("<t:%d:d> ➕ %d от %s", e.CreatedAt.Unix(), e.Amount, mention(e.AuthorID))
			if e.Reason != nil && *e.Reason != "" {
				line += " — " + *e.Reason
			}
		} else if e.Reason != nil && *e.Reason != "" {
			line += " (" + *e.Reason + ")"
		}
		lines = append(lines, line)
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: "История: " + t.Name, Description: shared.OrEmpty(strings.Join(lines, "\n"), "Записей нет."), Color: colorBlurple,
	}, true)
}

// interactionLaunchActivity — ответ, открывающий Activity приложения (в discordgo пока нет константы).
const interactionLaunchActivity discordgo.InteractionResponseType = 12

func casinoHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	if cfg.ActivityPort == "" {
		return r.replyHidden("Казино пока не запущено.")
	}
	return r.respond(nil, interactionLaunchActivity)
}

func undoHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	var id, userID int64
	var kind string
	var amount int
	var reason *string
	var createdAt time.Time
	found, err := store.FetchOne(`SELECT id, user_id, kind, amount, reason, created_at FROM entries
		WHERE guild_id = $1 AND author_id = $2 ORDER BY id DESC LIMIT 1`,
		[]any{r.guildID(), r.userID()}, &id, &userID, &kind, &amount, &reason, &createdAt)
	if err != nil {
		return err
	}
	if !found {
		return r.replyHidden("Тебе нечего отменять.")
	}
	if kind == "done" && reason != nil && strings.HasPrefix(*reason, "🎰 лудомания:") {
		return r.replyHidden("🎰 Казино ставок не возвращает: бросок лудомании отменить нельзя.")
	}
	if kind == "done" {
		ok, bal, err := economy.CanUndoDone(r.guildID(), userID, amount, createdAt)
		if err != nil {
			return err
		}
		if !ok {
			return r.replyHidden(fmt.Sprintf("Нельзя: поинты за эти %d отжиманий уже потрачены (на балансе %s).", amount, economy.Points(bal)))
		}
	}
	if err := store.Exec("DELETE FROM entries WHERE id = $1", id); err != nil {
		return err
	}
	what := fmt.Sprintf("запись о %d сделанных отжиманиях", amount)
	if kind == "owe" {
		what = fmt.Sprintf("назначение %d отжиманий %s", amount, mention(userID))
	}
	return r.reply(fmt.Sprintf("↩️ %s отменил %s.", r.userMention(), what))
}

func shopItemOpt() *discordgo.ApplicationCommandOption {
	opt := strOpt("item", "Что купить", true)
	for _, it := range economy.Items {
		opt.Choices = append(opt.Choices, &discordgo.ApplicationCommandOptionChoice{
			Name: fmt.Sprintf("%s — %s", it.Title, economy.Points(it.Price)), Value: it.Key,
		})
	}
	return opt
}

func shopHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	bal, err := economy.Balance(r.guildID(), r.userID())
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(economy.Items))
	for _, it := range economy.Items {
		lines = append(lines, fmt.Sprintf("**%s** — %s", it.Title, economy.Points(it.Price)))
	}
	return r.replyEmbed(&discordgo.MessageEmbed{
		Title: "🛒 Магазин", Color: colorGold, Description: strings.Join(lines, "\n"),
		Fields: []*discordgo.MessageEmbedField{field("Твой баланс", economy.Points(bal), false)},
		Footer: footer("1 сделанное отжимание = 1 поинт. Купить — /buy, товар выдаёт админ"),
	}, true)
}

func buyHandler(r *request) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	item, ok := economy.FindItem(r.stringOption("item"))
	if !ok {
		return r.replyHidden("Такого товара нет.")
	}
	orderID, bal, err := economy.Buy(r.guildID(), r.userID(), item)
	if errors.Is(err, economy.ErrNotEnough) {
		return r.replyHidden(fmt.Sprintf("Не хватает: «%s» стоит %s, у тебя %s.", item.Title, economy.Points(item.Price), economy.Points(bal)))
	}
	if err != nil {
		return err
	}
	return r.reply(economy.OrderText(r.userID(), orderID, item, bal))
}
