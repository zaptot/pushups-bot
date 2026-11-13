package casino

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"pushups-bot/internal/economy"
	"pushups-bot/internal/store"
	"pushups-bot/internal/store/storetest"
)

func TestJackpotMath(t *testing.T) {
	if got := jackpotContribution(100); got != 1 {
		t.Fatalf("ставка 100: в пул %d, ждали 1", got)
	}
	// малые ставки пополняют пул в среднем ровно на 1%
	const n = 200_000
	sum := 0
	for range n {
		sum += jackpotContribution(20)
	}
	if avg := float64(sum) / n; avg < 0.19 || avg > 0.21 {
		t.Fatalf("ставка 20: в пул в среднем %.3f, ждали 0.2", avg)
	}
	for pool, want := range map[int]int{300: 270, 5: 4, 1: 0, 0: 0} {
		if got := jackpotPrize(pool); got != want {
			t.Fatalf("пул %d: приз %d, ждали %d", pool, got, want)
		}
	}
	if c := jackpotChance(20); c < 0.00039 || c > 0.00041 {
		t.Fatalf("шанс ставки 20: %v, ждали 0.0004", c)
	}
	if c := jackpotChance(10_000); c != jackpotMaxChance {
		t.Fatalf("шанс не ограничен потолком: %v", c)
	}
	if text := jackpotText(42, "roulette", 270); !strings.Contains(text, "<@42>") || !strings.Contains(text, "рулетке") || !strings.Contains(text, "270 поинтов") {
		t.Fatalf("объявление: %q", text)
	}
}

func jackpotOf(t *testing.T, raw json.RawMessage) jackpotOutcome {
	t.Helper()
	var out struct{ Jackpot jackpotOutcome }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Jackpot
}

func TestJackpotSettle(t *testing.T) {
	storetest.Begin(t)
	wasEnabled := jackpotEnabled
	jackpotEnabled = true
	t.Cleanup(func() { jackpotRoll, jackpotEnabled = randRoll, wasEnabled })
	jackpotRoll = func() float64 { return 1 } // не срывается

	// ставок ещё не было: страница видит стартовую затравку
	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 601
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	if code, out := casinoCall(t, activityMux(), "GET", "/api/jackpot", devToken(t, activityMux()), nil); code != http.StatusOK ||
		out["pool"] != float64(jackpotSeed) || out["last"] != nil {
		t.Fatalf("джекпот до первой ставки: %d %v", code, out)
	}

	lose := bet{ID: "jp-lose", GuildID: testGuildID, UserID: 601, Name: "Первый", Game: "roulette",
		Stake: 100, Payout: 0, Result: rouletteResult{Number: 3}}
	raw, err := settle(lose, true)
	if err != nil {
		t.Fatal(err)
	}
	if jp := jackpotOf(t, raw); jp.Added != 1 || jp.Pool != jackpotSeed+1 || jp.Won != 0 {
		t.Fatalf("ставка 100 в новый пул: %+v", jp)
	}
	// повтор той же ставки не пополняет пул второй раз и отдаёт тот же итог
	again, err := settle(lose, true)
	if err != nil {
		t.Fatal(err)
	}
	if jp := jackpotOf(t, again); jp.Pool != jackpotSeed+1 {
		t.Fatalf("повтор: %+v", jp)
	}
	if n := count(t, "SELECT pool FROM jackpot WHERE guild_id = 1"); n != jackpotSeed+1 {
		t.Fatalf("пул после повтора %d, ждали %d", n, jackpotSeed+1)
	}

	jackpotRoll = func() float64 { return 0 } // срывается с любой ставки
	win := bet{ID: "jp-win", GuildID: testGuildID, UserID: 602, Name: "Второй", Game: "plinko",
		Stake: 100, Payout: 200, Result: plinkoResult{Rows: 8, Risk: "low"}}
	raw, err = settle(win, true)
	if err != nil {
		t.Fatal(err)
	}
	// пул: 50 + 1 + 1 = 52; приз: 52 - ceil(5.2) = 46; остаток: 6
	if jp := jackpotOf(t, raw); jp.Added != 1 || jp.Won != 46 || jp.Pool != 6 {
		t.Fatalf("срыв пула 52: %+v", jp)
	}
	if bal, err := economy.Balance(testGuildID, 602); err != nil || bal != economy.StartBonus-100+200+46 {
		t.Fatalf("баланс сорвавшего %d (%v), ждали %d", bal, err, economy.StartBonus-100+200+46)
	}
	if n := count(t, "SELECT count(*) FROM jackpot_wins WHERE bet_id = 'jp-win' AND amount = 46"); n != 1 {
		t.Fatalf("записей о срыве %d", n)
	}

	cfg.ActivityDevUserID = 602
	mux := activityMux()
	code, out := casinoCall(t, mux, "GET", "/api/jackpot", devToken(t, mux), nil)
	if code != http.StatusOK || out["pool"] != 6.0 || out["target"] != float64(jackpotTarget) {
		t.Fatalf("GET /api/jackpot: %d %v", code, out)
	}
	if last, _ := out["last"].(map[string]any); last["name"] != "Второй" || last["amount"] != 46.0 || last["game"] != "plinko" {
		t.Fatalf("последний джекпот: %v", out["last"])
	}
}

var randRoll = jackpotRoll

// выключенный джекпот: пул не трогается, срыва нет, в итоге игры нет поля jackpot
func TestJackpotDisabled(t *testing.T) {
	storetest.Begin(t)
	wasEnabled := jackpotEnabled
	jackpotEnabled = false
	jackpotRoll = func() float64 { return 0 } // сорвался бы с любой ставки
	t.Cleanup(func() { jackpotRoll, jackpotEnabled = randRoll, wasEnabled })
	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 602
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	mux := activityMux()
	token := devToken(t, mux)
	code, out := casinoCall(t, mux, "POST", "/api/roulette", token, map[string]any{"id": "jp-off", "stake": 20, "bet": map[string]any{"kind": "red"}})
	if code != http.StatusOK || out["jackpot"] != nil {
		t.Fatalf("ставка: %d %v", code, out)
	}
	if n := count(t, "SELECT count(*) FROM jackpot_wins WHERE bet_id = 'jp-off'"); n != 0 {
		t.Fatal("джекпот сорвался, хотя выключен")
	}
	if n := count(t, "SELECT count(*) FROM jackpot WHERE guild_id = 1"); n != 0 {
		t.Fatal("выключенный джекпот создал пул")
	}
	if _, st := casinoCall(t, mux, "GET", "/api/jackpot", token, nil); st["enabled"] != false {
		t.Fatalf("страница должна знать, что джекпот выключен: %v", st)
	}
}

// пул, созданный до затравки, получает её один раз при запуске схемы
func TestJackpotSeedMigration(t *testing.T) {
	storetest.Begin(t)
	if _, err := store.DB.Exec(context.Background(), `INSERT INTO jackpot (guild_id, pool, seeded) VALUES (2, 15, false)`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := store.DB.Exec(context.Background(), store.Schema); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, "SELECT pool FROM jackpot WHERE guild_id = 2"); n != 15+jackpotSeed {
		t.Fatalf("пул после миграции %d, ждали %d", n, 15+jackpotSeed)
	}
}
