package casino

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"pushups-bot/internal/store"
	"pushups-bot/internal/store/storetest"
)

func TestCasinoFeed(t *testing.T) {
	storetest.Begin(t)
	prevName := memberName
	t.Cleanup(func() { memberName = prevName })
	memberName = func(_, userID int64) string { return fmt.Sprintf("игрок %d", userID) }

	ctx := context.Background()
	add := func(id string, guild, user int64, game string, stake, payout int, ago string) {
		t.Helper()
		if _, err := store.DB.Exec(ctx, `INSERT INTO casino_bets (id, guild_id, user_id, game, stake, multiplier, payout, result, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, '{}', now() - $8::interval)`,
			id, guild, user, game, stake, float64(payout)/float64(stake), payout, ago); err != nil {
			t.Fatal(err)
		}
	}
	// 12 мелких игр за последний час: в «последних» только 10 самых свежих
	for i := range 12 {
		add(fmt.Sprintf("f-small-%d", i), testGuildID, 701, "roulette", 20, 0, fmt.Sprintf("%d minutes", 30+i))
	}
	add("f-big", testGuildID, 702, "rocket", 100, 900, "2 days")         // +800
	add("f-mid", testGuildID, 703, "plinko", 50, 300, "3 days")          // +250
	add("f-jp", testGuildID, 704, "blackjack", 20, 0, "1 day")           // −20, но джекпот 500 → +480
	add("f-old", testGuildID, 705, "rocket", 100, 5000, "8 days")        // старше недели — не в топе
	add("f-other", 2, 706, "rocket", 100, 9000, "1 hour")                // другой сервер
	add("f-tiny", testGuildID, 707, "plinko", 20, 30, "4 days")          // +10
	add("f-small-win", testGuildID, 708, "roulette", 20, 40, "5 days")   // +20
	add("f-smaller-win", testGuildID, 709, "roulette", 20, 24, "6 days") // +4, шестой — за пределами топ-5
	add("f-now", testGuildID, 710, "blackjack", 40, 100, "1 second")     // +60, самая свежая
	if _, err := store.DB.Exec(ctx, `INSERT INTO jackpot_wins (bet_id, guild_id, user_id, name, game, amount)
		VALUES ('f-jp', 1, 704, 'x', 'blackjack', 500)`); err != nil {
		t.Fatal(err)
	}

	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 701
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	mux := activityMux()
	code, out := casinoCall(t, mux, "GET", "/api/feed", devToken(t, mux), nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/feed: %d %v", code, out)
	}
	recent := out["recent"].([]any)
	if len(recent) != feedRecent {
		t.Fatalf("последних игр %d, ждали %d", len(recent), feedRecent)
	}
	first := recent[0].(map[string]any)
	if first["name"] != "игрок 710" || first["game"] != "blackjack" || first["profit"] != 60.0 || first["stake"] != 40.0 {
		t.Fatalf("самая свежая игра: %v", first)
	}
	if second := recent[1].(map[string]any); second["name"] != "игрок 701" || second["profit"] != -20.0 {
		t.Fatalf("вторая игра: %v", second)
	}

	top := out["top"].([]any)
	want := []struct {
		name    string
		profit  float64
		jackpot float64
	}{{"игрок 702", 800, 0}, {"игрок 704", 480, 500}, {"игрок 703", 250, 0}, {"игрок 710", 60, 0}, {"игрок 708", 20, 0}}
	if len(top) != len(want) {
		t.Fatalf("топ: %v", top)
	}
	for i, w := range want {
		g := top[i].(map[string]any)
		if g["name"] != w.name || g["profit"] != w.profit || g["jackpot"] != w.jackpot {
			t.Fatalf("топ[%d] = %v, ждали %+v", i, g, w)
		}
	}
	if out["topDays"] != float64(feedTopDays) {
		t.Fatalf("topDays: %v", out["topDays"])
	}
}

func TestCasinoFeedNameFallback(t *testing.T) {
	session = nil
	names = map[[2]int64]cachedName{}
	if got := memberName(1, 999); got != nameUnknown {
		t.Fatalf("имя без Discord: %q", got)
	}
}
