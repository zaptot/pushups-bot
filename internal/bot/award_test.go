package bot

import (
	"testing"

	"github.com/bwmarrin/discordgo"

	"pushups-bot/internal/economy"
)

func balance(t *testing.T, id int64) int {
	t.Helper()
	bal, err := economy.Balance(testGuildID, id)
	if err != nil {
		t.Fatal(err)
	}
	return bal
}

func TestAwardOwnerOnly(t *testing.T) {
	fake := setup(t)
	prev := guildOwner
	t.Cleanup(func() { guildOwner = prev })
	guildOwner = func(int64) (int64, error) { return 7, nil }
	start := economy.StartBonus

	// у участника с правом «Управлять сервером», но не владельца, ничего не выходит
	run(8, "award", user("user", 9), num("amount", 300), str("reason", "3 PR в казино"))
	want(t, fake, "только владелец сервера")
	run(8, "award", user("user", 8), num("amount", 300), str("reason", "сам себе"))
	want(t, fake, "только владелец сервера")
	if n := count(t, "SELECT count(*) FROM wallet WHERE guild_id = 1"); n != 0 {
		t.Fatalf("не владелец записал %d движений", n)
	}

	run(7, "award", user("user", 9), num("amount", 300), str("reason", "3 PR в казино"))
	want(t, fake, "получает **300 поинтов** — 3 PR в казино. Баланс: 400 поинтов")
	if got := balance(t, 9); got != start+300 {
		t.Fatalf("баланс после награды %d", got)
	}
	if n := count(t, "SELECT count(*) FROM wallet WHERE user_id = 9 AND amount = 300 AND ref LIKE 'award:9:%' AND reason = 'награда от владельца: 3 PR в казино'"); n != 1 {
		t.Fatalf("строк награды в кошельке %d", n)
	}

	run(7, "deduct", user("user", 9), num("amount", 1000), str("reason", "ошибся"))
	want(t, fake, "Столько не снять")
	run(7, "deduct", user("user", 9), num("amount", 50), str("reason", "ошибся с суммой"))
	want(t, fake, "снято **50 поинтов** — ошибся с суммой. Баланс: 350 поинтов")
	if got := balance(t, 9); got != start+250 {
		t.Fatalf("баланс после снятия %d", got)
	}

	run(7, "award", user("user", 9), num("amount", 10), str("reason", "  "))
	want(t, fake, "причина хотя бы")
	if got := balance(t, 9); got != start+250 {
		t.Fatalf("баланс изменился от отклонённых команд: %d", got)
	}
}

func TestAwardCommandsHidden(t *testing.T) {
	for _, c := range awardCommands() {
		if c.def.DefaultMemberPermissions == nil || *c.def.DefaultMemberPermissions != discordgo.PermissionManageGuild {
			t.Fatalf("/%s видна всем", c.def.Name)
		}
		if c.def.DMPermission == nil || *c.def.DMPermission {
			t.Fatalf("/%s доступна в личке", c.def.Name)
		}
	}
	names := map[string]int{}
	for _, c := range commandList() {
		names[c.def.Name]++
	}
	for name, n := range names {
		if n > 1 {
			t.Fatalf("команда /%s объявлена %d раз", name, n)
		}
	}
}

func TestGuildOwnerFromState(t *testing.T) {
	setup(t)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: testGuild, OwnerID: "7"}); err != nil {
		t.Fatal(err)
	}
	if owner, err := guildOwner(testGuildID); err != nil || owner != 7 {
		t.Fatalf("владелец %d (%v), ждали 7", owner, err)
	}
}
