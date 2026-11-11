package bot

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"pushups-bot/internal/economy"
	"pushups-bot/internal/shared"
)

// /award и /deduct — ручное начисление и снятие поинтов (за помощь серверу, исправление ошибок).
// Доступны только владельцу Discord-сервера: владелец у сервера один, его нельзя выдать ролью или
// правом, а сменить его может только сам владелец. Каждое движение публично и пишется в кошелёк
// со своей причиной.

const (
	awardMax       = 10000
	awardReasonMin = 3
	awardReasonMax = 200
)

// guildOwner — ID владельца сервера: из кэша сессии, иначе запросом к Discord. В тестах подменяется.
var guildOwner = func(guildID int64) (int64, error) {
	id := shared.IDStr(guildID)
	if g, err := session.State.Guild(id); err == nil && g.OwnerID != "" {
		return shared.ParseID(g.OwnerID), nil
	}
	g, err := session.Guild(id)
	if err != nil {
		return 0, err
	}
	return shared.ParseID(g.OwnerID), nil
}

func awardCommands() []command {
	manageGuild := int64(discordgo.PermissionManageGuild)
	noDM := false
	reason := strOpt("reason", "За что (видно всем)", true)
	reason.MinLength, reason.MaxLength = intPtr(awardReasonMin), awardReasonMax
	award := cmd("award", "(Владелец) Выдать поинты участнику", awardHandler,
		userOpt("user", "Кому", true), intOpt("amount", "Сколько поинтов", true, 1, awardMax), reason)
	deduct := cmd("deduct", "(Владелец) Снять поинты у участника", deductHandler,
		userOpt("user", "У кого", true), intOpt("amount", "Сколько поинтов", true, 1, awardMax), reason)
	for _, c := range []command{award, deduct} {
		// скрывает команды у участников без права «Управлять сервером»; настоящая проверка — в обработчике
		c.def.DefaultMemberPermissions = &manageGuild
		c.def.DMPermission = &noDM
	}
	return []command{award, deduct}
}

func awardHandler(r *request) error  { return moveHandler(r, 1) }
func deductHandler(r *request) error { return moveHandler(r, -1) }

func moveHandler(r *request, sign int) error {
	if !r.inGuild() {
		return r.noGuild()
	}
	owner, err := guildOwner(r.guildID())
	if err != nil {
		return err
	}
	if owner == 0 || r.userID() != owner {
		return r.replyHidden("Выдавать и снимать поинты может только владелец сервера.")
	}
	user := r.userOption("user")
	if user.Bot {
		return r.replyHidden("У ботов нет кошелька 🤖")
	}
	amount := r.intOption("amount", 0)
	reason := strings.TrimSpace(r.stringOption("reason"))
	if amount < 1 || amount > awardMax || len([]rune(reason)) < awardReasonMin {
		return r.replyHidden(fmt.Sprintf("Нужно от 1 до %d поинтов и причина хотя бы из %d символов.", awardMax, awardReasonMin))
	}
	kind, label := "award", "награда"
	if sign < 0 {
		kind, label = "deduct", "снятие"
	}
	bal, err := economy.Apply(r.guildID(), user.ID, nil, economy.Move{
		Amount: sign * amount,
		Ref:    fmt.Sprintf("%s:%d:%d", kind, user.ID, time.Now().UnixNano()),
		Reason: fmt.Sprintf("%s от владельца: %s", label, reason),
	})
	if errors.Is(err, economy.ErrNotEnough) {
		return r.replyHidden(fmt.Sprintf("Столько не снять: у %s только %s.", user.mention(), economy.Points(bal)))
	}
	if err != nil {
		return err
	}
	if sign > 0 {
		return r.reply(fmt.Sprintf("🎁 %s получает **%s** — %s. Баланс: %s.", user.mention(), economy.Points(amount), reason, economy.Points(bal)))
	}
	return r.reply(fmt.Sprintf("➖ У %s снято **%s** — %s. Баланс: %s.", user.mention(), economy.Points(amount), reason, economy.Points(bal)))
}
