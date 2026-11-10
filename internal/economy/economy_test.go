package economy

import (
	"errors"
	"testing"
	"time"

	"pushups-bot/internal/store"
	"pushups-bot/internal/store/storetest"
)

func TestPoints(t *testing.T) {
	for n, want := range map[int]string{1: "1 поинт", 3: "3 поинта", 5: "5 поинтов", 11: "11 поинтов", 21: "21 поинт", 112: "112 поинтов", -2: "-2 поинта"} {
		if got := Points(n); got != want {
			t.Errorf("%d: %q", n, got)
		}
	}
}

func mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if err := store.Exec(sql, args...); err != nil {
		t.Fatal(err)
	}
}

func balanceOf(t *testing.T, user int64) int {
	t.Helper()
	n, err := Balance(1, user)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWallet(t *testing.T) {
	storetest.Begin(t)
	if b := balanceOf(t, 7); b != StartBonus {
		t.Fatalf("новичок: %d", b)
	}
	// до начала экономики не считается, после — 1:1; назначенные не считаются
	mustExec(t, "INSERT INTO entries (guild_id, user_id, kind, amount, author_id, created_at) VALUES (1, 7, 'done', 500, 7, $1)", Start.Add(-time.Hour))
	mustExec(t, "INSERT INTO entries (guild_id, user_id, kind, amount, author_id) VALUES (1, 7, 'done', 950, 7)")
	mustExec(t, "INSERT INTO entries (guild_id, user_id, kind, amount, author_id) VALUES (1, 7, 'owe', 300, 7)")
	if b := balanceOf(t, 7); b != 1050 {
		t.Fatalf("баланс %d, ждали 1050", b)
	}
	photo, _ := FindItem("photo")
	if _, bal, err := Buy(1, 7, photo); !errors.Is(err, ErrNotEnough) || bal != 1050 {
		t.Fatalf("фото: %d %v", bal, err)
	}
	voucher, _ := FindItem("voucher")
	id, bal, err := Buy(1, 7, voucher)
	if err != nil || bal != 50 || id == 0 {
		t.Fatalf("ваучер: %d %d %v", id, bal, err)
	}
	if ok, _, _ := CanUndoDone(1, 7, 950, time.Now()); ok {
		t.Fatal("отмена потраченных отжиманий должна быть запрещена")
	}
	if ok, _, _ := CanUndoDone(1, 7, 500, Start.Add(-time.Hour)); !ok {
		t.Fatal("старые отжимания поинтов не давали, их отмена ничего не тратит")
	}
	// повтор того же ref не списывает дважды
	stake := Move{Amount: -20, Ref: "stake:b1", BetID: "b1", Reason: "ставка"}
	for range 2 {
		if _, err := Apply(1, 7, nil, stake); err != nil {
			t.Fatal(err)
		}
	}
	if b := balanceOf(t, 7); b != 30 {
		t.Fatalf("после ставки %d, ждали 30", b)
	}
	// ставка без итога возвращается при перезапуске, с итогом — нет
	mustExec(t, `INSERT INTO casino_bets (id, guild_id, user_id, game, stake, multiplier, payout, result) VALUES ('b2', 1, 7, 'rocket', 10, 0, 0, '{}')`)
	if _, err := Apply(1, 7, nil, Move{Amount: -10, Ref: "stake:b2", BetID: "b2", Reason: "ставка"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RefundUnsettled(); err != nil {
		t.Fatal(err)
	}
	if _, err := RefundUnsettled(); err != nil {
		t.Fatal(err)
	}
	if b := balanceOf(t, 7); b != 40 {
		t.Fatalf("после возврата %d, ждали 40 (вернули только b1)", b)
	}
}

func TestOldCasinoCleanup(t *testing.T) {
	storetest.Begin(t)
	mustExec(t, `INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason) VALUES
		(1, 8, 'done', 40, 8, '🎰 лудомания: 20 × 2.00 · блэкджек'),
		(1, 8, 'done', 0, 8, '🎰 лудомания: 10 × 0.00 · рулетка'),
		(1, 8, 'done', 15, 8, '🎰 лудомания: 10 × 1.48')`)
	mustExec(t, store.Schema)
	mustExec(t, store.Schema)
	got, err := store.Column[string]("SELECT amount || ' ' || reason FROM entries WHERE user_id = 8 ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"20 🎰 лудомания: 20 (выигрыш убран) · блэкджек", "10 🎰 лудомания: 10 (выигрыш убран) · рулетка", "15 🎰 лудомания: 10 × 1.48"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: %q", i, got[i])
		}
	}
}
