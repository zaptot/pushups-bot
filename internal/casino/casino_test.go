package casino

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"pushups-bot/internal/economy"
	"pushups-bot/internal/store"
	"pushups-bot/internal/store/storetest"
)

const (
	testGuild   = "1"
	testGuildID = int64(1)
)

func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if _, err := store.FetchOne(sql, args, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func casinoCall(t *testing.T, mux http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "127.0.0.1:50000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("ответ не JSON: %s", rec.Body.String())
		}
	}
	return rec.Code, out
}

var citizenInDB = isCitizen

func init() {
	betInterval = 0
	// тесты без базы считают всех гражданами; проверку по базе — см. TestCasinoCitizensOnly
	isCitizen = func(player) (bool, error) { return true, nil }
}

func devToken(t *testing.T, mux http.Handler) string {
	t.Helper()
	code, out := casinoCall(t, mux, "POST", "/api/session", "", nil)
	if code != http.StatusOK {
		t.Fatalf("сессия: %d %v", code, out)
	}
	return out["token"].(string)
}

func TestCasinoDevSession(t *testing.T) {
	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 0
	mux := activityMux()
	if code, _ := casinoCall(t, mux, "POST", "/api/session", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("без режима разработки ждали 401, получили %d", code)
	}
	cfg.ActivityDevUserID = 42
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	if devToken(t, mux) == "" {
		t.Fatal("пустой токен")
	}
	if code, _ := casinoCall(t, mux, "POST", "/api/roulette", "nope", map[string]any{"id": "x", "stake": 20}); code != http.StatusUnauthorized {
		t.Fatalf("без токена ждали 401, получили %d", code)
	}
}

type fakeOAuth struct{ member bool }

func (f fakeOAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusNotFound, `{}`
	switch {
	case req.URL.Path == "/api/oauth2/token" && req.FormValue("code") == "good" && req.FormValue("client_id") == "555":
		status, body = http.StatusOK, `{"access_token":"t"}`
	case req.URL.Path == "/api/users/@me" && req.Header.Get("Authorization") == "Bearer t":
		status, body = http.StatusOK, `{"id":"77"}`
	case strings.HasSuffix(req.URL.Path, "/guilds/1/members/77") && f.member:
		status, body = http.StatusOK, `{"nick":"Качок","user":{"id":"77","username":"u77"}}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Request: req,
		Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestCasinoDiscordSession(t *testing.T) {
	oldTransport, oldSession := http.DefaultTransport, session
	t.Cleanup(func() {
		http.DefaultTransport, session, cfg.ClientID, cfg.ClientSecret = oldTransport, oldSession, "", ""
	})
	session, _ = discordgo.New("Bot test")
	session.State.User = &discordgo.User{ID: "999"}
	cfg.ClientID, cfg.ClientSecret = "555", "secret"
	mux := activityMux()
	if _, out := casinoCall(t, mux, "GET", "/api/config", "", nil); out["clientId"] != "555" {
		t.Fatalf("clientId: %v", out)
	}
	login := func(member bool, body map[string]any) (int, map[string]any) {
		http.DefaultTransport = fakeOAuth{member}
		session.Client = &http.Client{Transport: fakeOAuth{member}}
		return casinoCall(t, mux, "POST", "/api/session", "", body)
	}

	playersMu.Lock()
	players["old"] = player{Expires: time.Now().Add(-time.Second)}
	playersMu.Unlock()
	code, out := login(true, map[string]any{"code": "good", "guildId": "1"})
	if code != http.StatusOK || out["user"].(map[string]any)["name"] != "Качок" {
		t.Fatalf("вход: %d %v", code, out)
	}
	playersMu.Lock()
	_, stale := players["old"]
	p := players[out["token"].(string)]
	playersMu.Unlock()
	if stale || p.UserID != 77 || p.GuildID != 1 {
		t.Fatalf("сессии: stale=%v player=%+v", stale, p)
	}

	cases := []struct {
		member bool
		body   map[string]any
		want   int
	}{
		{true, map[string]any{"code": "bad", "guildId": "1"}, http.StatusUnauthorized},
		{true, map[string]any{"code": "good"}, http.StatusForbidden},
		{false, map[string]any{"code": "good", "guildId": "1"}, http.StatusForbidden},
	}
	for _, c := range cases {
		if code, out := login(c.member, c.body); code != c.want {
			t.Fatalf("%v: ждали %d, получили %d %v", c.body, c.want, code, out)
		}
	}
	cfg.ClientSecret = ""
	if code, _ := login(true, map[string]any{"code": "good", "guildId": "1"}); code != http.StatusUnauthorized {
		t.Fatalf("без секрета ждали 401, получили %d", code)
	}
}

func TestCasinoRoulettePayouts(t *testing.T) {
	bets := []struct {
		kind   string
		values []int
	}{
		{"red", []int{0}}, {"black", []int{0}}, {"even", []int{0}}, {"odd", []int{0}},
		{"low", []int{0}}, {"high", []int{0}}, {"dozen", []int{1, 2, 3}},
	}
	numbers := make([]int, 37)
	for i := range numbers {
		numbers[i] = i
	}
	bets = append(bets, struct {
		kind   string
		values []int
	}{"number", numbers})
	for _, b := range bets {
		for _, v := range b.values {
			total := 0.0
			for n := range 37 {
				m, err := rouletteMultiplier(b.kind, v, n)
				if err != nil {
					t.Fatal(err)
				}
				total += m
			}
			if math.Abs(total/37-36.0/37) > 1e-9 {
				t.Fatalf("%s %d: возврат %.4f, ждали 36/37", b.kind, v, total/37)
			}
		}
	}
	if m, _ := rouletteMultiplier("red", 0, 0); m != 0 {
		t.Fatal("зеро не должно выигрывать на красном")
	}
	if m, _ := rouletteMultiplier("number", 0, 0); m != 36 {
		t.Fatal("ставка на зеро должна платить x36")
	}
	for _, bad := range []struct {
		kind  string
		value int
	}{{"dozen", 0}, {"dozen", 4}, {"number", 37}, {"number", -1}, {"color", 0}} {
		if _, err := rouletteMultiplier(bad.kind, bad.value, 5); err == nil {
			t.Fatalf("%s %d должна быть отклонена", bad.kind, bad.value)
		}
	}
}

func TestCasinoRoundPayout(t *testing.T) {
	const runs = 100000
	sum := 0
	for range runs {
		v := roundPayout(37, 0.5)
		if v != 18 && v != 19 {
			t.Fatalf("37 × 0.5 дало %d", v)
		}
		sum += v
	}
	if avg := float64(sum) / runs; math.Abs(avg-18.5) > 0.05 {
		t.Fatalf("среднее %.3f, ждали 18.5", avg)
	}
	for range 1000 {
		if roundPayout(20, 2) != 40 || roundPayout(10, 1.1) != 11 {
			t.Fatal("целое произведение не должно меняться")
		}
	}
}

func TestCasinoPlinkoTables(t *testing.T) {
	if len(plinkoTables) != plinkoRowsMax-plinkoRowsMin+1 {
		t.Fatalf("таблиц по рядам %d", len(plinkoTables))
	}
	for n, byRisk := range plinkoTables {
		odds := plinkoOdds(n)
		total := 0.0
		for _, p := range odds {
			total += p
		}
		if math.Abs(total-1) > 1e-12 {
			t.Fatalf("%d рядов: вероятности в сумме %.6f", n, total)
		}
		if len(byRisk) != 3 {
			t.Fatalf("%d рядов: уровней риска %d", n, len(byRisk))
		}
		for risk, table := range byRisk {
			rtp := 0.0
			for k, m := range table {
				rtp += odds[k] * m
				if m != table[n-k] {
					t.Fatalf("%d %s: таблица несимметрична", n, risk)
				}
			}
			if math.Abs(rtp-plinkoRTP) > 0.005 {
				t.Fatalf("%d %s: возврат %.4f", n, risk, rtp)
			}
		}
		low, high := byRisk["low"], byRisk["high"]
		if high[0] <= low[0] || high[n/2] >= low[n/2] {
			t.Fatalf("%d рядов: высокий риск должен платить больше на краю и меньше в центре", n)
		}
	}
	for range 1000 {
		path, slot := plinkoDrop(12)
		sum := 0
		for _, b := range path {
			sum += b
		}
		if len(path) != 12 || sum != slot {
			t.Fatalf("путь %v не ведёт в лунку %d", path, slot)
		}
	}
}

func testDeck(t *testing.T, codes ...string) []card {
	t.Helper()
	deck := make([]card, len(codes))
	for i, code := range codes {
		rank := strings.IndexByte(blackjackRanks, code[0]) + 1
		suit := strings.IndexByte(blackjackSuits, code[1])
		if rank == 0 || suit < 0 {
			t.Fatalf("неверная карта %q", code)
		}
		deck[i] = card{rank, suit}
	}
	return deck
}

func TestCasinoBlackjackRules(t *testing.T) {
	totals := []struct {
		cards []string
		total int
		soft  bool
	}{
		{[]string{"As", "6h"}, 17, true},
		{[]string{"As", "6h", "Td"}, 17, false},
		{[]string{"As", "Ah", "9d"}, 21, true},
		{[]string{"Ks", "Qh", "5d"}, 25, false},
	}
	for _, c := range totals {
		if total, soft := handTotal(testDeck(t, c.cards...)); total != c.total || soft != c.soft {
			t.Fatalf("%v: %d %v, ждали %d %v", c.cards, total, soft, c.total, c.soft)
		}
	}
	cases := []struct {
		name    string
		deck    []string
		actions []string
		status  string
		mult    float64
		stake   int
	}{
		{"блэкджек игрока", []string{"As", "9h", "Kd", "7c"}, nil, "blackjack", 2.5, 20},
		{"блэкджек у обоих", []string{"As", "Ah", "Kd", "Qc"}, nil, "push", 1, 20},
		{"блэкджек дилера", []string{"Ts", "Ah", "9d", "Kc"}, nil, "lose", 0, 20},
		{"20 против 19", []string{"Ks", "9h", "Qd", "Tc"}, []string{"stand"}, "win", 2, 20},
		{"дилер стоит на мягких 17", []string{"Ts", "Ah", "8d", "6c", "Ks"}, []string{"stand"}, "win", 2, 20},
		{"дилер добирает с 16", []string{"Ts", "Th", "8d", "6c", "5s"}, []string{"stand"}, "lose", 0, 20},
		{"дилер перебрал", []string{"Ts", "Th", "8d", "6c", "9s"}, []string{"stand"}, "win", 2, 20},
		{"ничья", []string{"Ts", "Th", "8d", "8c"}, []string{"stand"}, "push", 1, 20},
		{"перебор", []string{"Ts", "9h", "6d", "8c", "Ks"}, []string{"hit"}, "bust", 0, 20},
		{"удвоение", []string{"5s", "Th", "6d", "7c", "9s"}, []string{"double"}, "win", 2, 40},
		{"21 после добора", []string{"5s", "Th", "6d", "7c", "Ks"}, []string{"hit"}, "win", 2, 20},
	}
	for _, c := range cases {
		h := newBlackjackHand("h", player{}, 20, testDeck(t, c.deck...))
		for _, a := range c.actions {
			if err := h.act(a); err != nil {
				t.Fatalf("%s: %s: %v", c.name, a, err)
			}
		}
		if h.Status != c.status || h.Mult != c.mult || h.Stake != c.stake {
			t.Fatalf("%s: %s x%.1f ставка %d, ждали %s x%.1f ставка %d", c.name, h.Status, h.Mult, h.Stake, c.status, c.mult, c.stake)
		}
		if err := h.act("hit"); err == nil {
			t.Fatalf("%s: ход после конца партии должен быть отклонён", c.name)
		}
	}
	bust := newBlackjackHand("h", player{}, 20, testDeck(t, "Ts", "9h", "6d", "8c", "Ks", "2s"))
	if err := bust.act("hit"); err != nil || len(bust.Dealer) != 2 {
		t.Fatalf("при переборе дилер не должен добирать: %v %d", err, len(bust.Dealer))
	}

	h := newBlackjackHand("h", player{}, 20, testDeck(t, "Ts", "9h", "2d", "Qc", "3s", "4s", "5s"))
	raw, err := json.Marshal(h.view())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Qc") || !strings.Contains(string(raw), blackjackHidden) {
		t.Fatalf("закрытая карта дилера видна: %s", raw)
	}
	if h.view().DealerTotal != 9 {
		t.Fatalf("сумма дилера по открытой карте %d", h.view().DealerTotal)
	}
	if err := h.act("hit"); err != nil || h.Seq != 1 || h.canDouble() {
		t.Fatalf("после добора: %v, seq %d, удвоение %v", err, h.Seq, h.canDouble())
	}
	if err := h.act("double"); err == nil {
		t.Fatal("удвоение на трёх картах должно быть отклонено")
	}
	if err := h.act("split"); err == nil {
		t.Fatal("неизвестное действие должно быть отклонено")
	}
}

func TestCasinoRocket(t *testing.T) {
	if crashPoint(0) != 1 || crashPoint(0.999999999) != rocketMax {
		t.Fatalf("границы точки взрыва: %.2f %.2f", crashPoint(0), crashPoint(0.999999999))
	}
	const runs = 200000
	crashes := make([]float64, runs)
	for i := range crashes {
		crashes[i] = crashPoint(rand.Float64())
	}
	for _, m := range []float64{1.5, 2, 5, 10} {
		survived, returned := 0, 0.0
		for _, c := range crashes {
			if status, mult := rocketOutcome(c, 0, m); status == "won" {
				survived++
				returned += mult
			}
		}
		if p := float64(survived) / runs; math.Abs(p-0.99/m) > 0.01 {
			t.Fatalf("x%.1f: доля %.4f, ждали %.4f", m, p, 0.99/m)
		}
		if rtp := returned / runs; math.Abs(rtp-0.99) > 0.02 {
			t.Fatalf("вывод на x%.1f: возврат %.4f", m, rtp)
		}
	}
	outcomes := []struct {
		crash, auto, m float64
		status         string
		mult           float64
	}{
		{3.41, 0, 2.03, "won", 2.03},
		{1.87, 0, 1.87, "crashed", 0},
		{1.87, 0, 2.5, "crashed", 0},
		{3, 1.5, 2.2, "won", 1.5},
		{3, 1.5, 1.5, "won", 1.5},
		{1.4, 1.5, 1.4, "crashed", 0},
		{1.5, 1.5, 1.5, "crashed", 0},
		{1, 0, 1, "crashed", 0},
		{rocketMax, rocketAutoMax, rocketMax, "won", rocketAutoMax},
	}
	for _, c := range outcomes {
		if status, mult := rocketOutcome(c.crash, c.auto, c.m); status != c.status || mult != c.mult {
			t.Fatalf("взрыв x%.2f, авто x%.2f, вывод x%.2f: %s x%.2f, ждали %s x%.2f", c.crash, c.auto, c.m, status, mult, c.status, c.mult)
		}
	}
	if d := rocketDelay(2); d < 11*time.Second || d > 12*time.Second {
		t.Fatalf("x2 должно быть через ~11.6 с, получилось %v", d)
	}
	if m := rocketMultiplier(rocketDelay(2) + time.Millisecond); m != 2 {
		t.Fatalf("множитель в момент x2: %.2f", m)
	}
}

func TestCasinoCitizensOnly(t *testing.T) {
	storetest.Begin(t)
	isCitizen = citizenInDB
	forgetCitizens()
	t.Cleanup(func() {
		isCitizen = func(player) (bool, error) { return true, nil }
		forgetCitizens()
	})
	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 601
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	mux := activityMux()
	code, out := casinoCall(t, mux, "POST", "/api/session", "", nil)
	if code != http.StatusOK || out["citizen"] != false || out["token"] != nil {
		t.Fatalf("не гражданин: %d %v", code, out)
	}
	stale := newPlayer(player{UserID: 601, GuildID: testGuildID})
	body := map[string]any{"id": "nc-1", "stake": 20, "bet": map[string]any{"kind": "red"}}
	if code, _ := casinoCall(t, mux, "POST", "/api/roulette", stale, body); code != http.StatusForbidden {
		t.Fatalf("ставка не гражданина: ждали 403, получили %d", code)
	}
	if err := store.Exec("INSERT INTO citizens (guild_id, user_id) VALUES (1, 601)"); err != nil {
		t.Fatal(err)
	}
	code, out = casinoCall(t, mux, "POST", "/api/session", "", nil)
	if code != http.StatusOK || out["citizen"] != true || out["token"] == nil {
		t.Fatalf("гражданин: %d %v", code, out)
	}
	token := out["token"].(string)
	if code, out := casinoCall(t, mux, "POST", "/api/roulette", token, body); code != http.StatusOK {
		t.Fatalf("ставка гражданина: %d %v", code, out)
	}
	// подтверждённое гражданство помнится citizenTTL, без запроса в базу на каждую кнопку
	if err := store.Exec("DELETE FROM citizens WHERE guild_id = 1 AND user_id = 601"); err != nil {
		t.Fatal(err)
	}
	if code, _ := casinoCall(t, mux, "GET", "/api/balance", token, nil); code != http.StatusOK {
		t.Fatalf("гражданство из кэша: ждали 200, получили %d", code)
	}
	forgetCitizens() // как будто citizenTTL прошёл
	if code, _ := casinoCall(t, mux, "GET", "/api/balance", token, nil); code != http.StatusForbidden {
		t.Fatalf("отнятое гражданство: ждали 403, получили %d", code)
	}
}

func TestCasinoReplay(t *testing.T) {
	storetest.Begin(t)
	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 501
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	mux := activityMux()
	token := devToken(t, mux)
	body := map[string]any{"id": "replay-1", "stake": 20, "bet": map[string]any{"kind": "red"}}
	var first map[string]any
	for i := range 10 {
		code, out := casinoCall(t, mux, "POST", "/api/roulette", token, body)
		if code != http.StatusOK {
			t.Fatalf("ставка %d: %d %v", i, code, out)
		}
		if first == nil {
			first = out
		} else if out["number"] != first["number"] || out["payout"] != first["payout"] {
			t.Fatalf("повтор дал другой результат: %v против %v", out, first)
		}
	}
	if n := count(t, "SELECT count(*) FROM entries WHERE guild_id = 1 AND user_id = 501"); n != 0 {
		t.Fatalf("казино записало отжимания: %d записей в entries", n)
	}
	if n := count(t, "SELECT count(*) FROM casino_bets WHERE id = 'replay-1'"); n != 1 {
		t.Fatalf("записей в casino_bets %d, ждали 1", n)
	}
	// повтор сразу после ставки упирается в ограничение частоты — и всё равно получает сохранённый итог
	betInterval = time.Hour
	if code, out := casinoCall(t, mux, "POST", "/api/roulette", token, body); code != http.StatusOK || out["number"] != first["number"] {
		t.Fatalf("быстрый повтор: %d %v", code, out)
	}
	fresh := map[string]any{"id": "replay-fast", "stake": 20, "bet": map[string]any{"kind": "red"}}
	if code, _ := casinoCall(t, mux, "POST", "/api/roulette", token, fresh); code != http.StatusTooManyRequests {
		t.Fatalf("новая ставка сразу: ждали 429, получили %d", code)
	}
	betInterval = 0
	_, bal := casinoCall(t, mux, "GET", "/api/balance", token, nil)
	if want := float64(economy.StartBonus - 20 + int(first["payout"].(float64))); bal["balance"] != want {
		t.Fatalf("баланс %v, ждали %v", bal["balance"], want)
	}
	if code, out := casinoCall(t, mux, "POST", "/api/blackjack", token, map[string]any{"stake": 5000}); code != http.StatusUnprocessableEntity || !strings.Contains(out["error"].(string), "Не хватает") || out["code"] != codeNotEnough {
		t.Fatalf("ставка больше баланса: %d %v", code, out)
	}
	if code, out := casinoCall(t, mux, "POST", "/api/shop/buy", token, map[string]any{"item": "voucher"}); code != http.StatusUnprocessableEntity || out["code"] != codeNotEnough {
		t.Fatalf("покупка без денег: %d %v", code, out)
	}
	for _, auto := range []float64{1, 100} {
		if code, out := casinoCall(t, mux, "POST", "/api/rocket", token, map[string]any{"stake": 30, "auto": auto}); code != http.StatusUnprocessableEntity {
			t.Fatalf("автовывод x%.2f: ждали 422, получили %d %v", auto, code, out)
		}
	}
	code, out := casinoCall(t, mux, "POST", "/api/rocket", token, map[string]any{"stake": 30, "auto": 0})
	if code != http.StatusOK {
		t.Fatalf("рокетка: %d %v", code, out)
	}
	_, after := casinoCall(t, mux, "GET", "/api/balance", token, nil)
	if after["balance"] != bal["balance"].(float64)-30 {
		t.Fatalf("ставка рокетки не списалась сразу: %v -> %v", bal, after)
	}
	rocketMu.Lock()
	rocketRounds[out["id"].(string)].finish(1.5)
	rocketMu.Unlock()
	other := newPlayer(player{UserID: 502, GuildID: testGuildID})
	if code, _ := casinoCall(t, mux, "POST", "/api/roulette", other, body); code != http.StatusConflict {
		t.Fatalf("чужой id ставки: ждали 409, получили %d", code)
	}
	if code, _ := casinoCall(t, mux, "POST", "/api/roulette", token, map[string]any{"id": "replay-2", "stake": 5, "bet": map[string]any{"kind": "red"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("ставка 5: ждали 422, получили %d", code)
	}
}

func TestCasinoDevSessionLocalOnly(t *testing.T) {
	cfg.GuildID, cfg.ActivityDir = testGuild, ""
	cfg.ActivityDevUserID = 42
	t.Cleanup(func() { cfg.ActivityDevUserID = 0 })
	mux := activityMux()
	for _, c := range []struct {
		remote, forwarded string
		want              int
	}{
		{"127.0.0.1:50000", "", http.StatusOK},
		{"[::1]:50000", "", http.StatusOK},
		{"192.0.2.1:50000", "", http.StatusForbidden},
		{"127.0.0.1:50000", "203.0.113.5", http.StatusForbidden},
	} {
		req := httptest.NewRequest("POST", "/api/session", nil)
		req.RemoteAddr = c.remote
		if c.forwarded != "" {
			req.Header.Set("X-Forwarded-For", c.forwarded)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s: ждали %d, получили %d", c.remote, c.forwarded, c.want, rec.Code)
		}
	}
}

func TestCasinoRateLimit(t *testing.T) {
	betInterval = time.Hour
	t.Cleanup(func() { betInterval = 0 })
	p := player{UserID: 5, GuildID: 1}
	if rec := httptest.NewRecorder(); tooFast(rec, p) {
		t.Fatal("первая ставка отклонена")
	}
	rec := httptest.NewRecorder()
	if !tooFast(rec, p) || rec.Code != http.StatusTooManyRequests {
		t.Fatalf("вторая ставка сразу: ждали 429, получили %d", rec.Code)
	}
	if rec := httptest.NewRecorder(); tooFast(rec, player{UserID: 6, GuildID: 1}) {
		t.Fatal("ставка другого игрока отклонена")
	}
}

func TestCasinoDoubleCap(t *testing.T) {
	deck := []card{{9, 0}, {10, 0}, {2, 0}, {7, 0}, {5, 0}}
	h := newBlackjackHand("cap", player{UserID: 1}, stakeMax, deck)
	if h.Status != "playing" || h.canDouble() {
		t.Fatalf("удвоение на максимальной ставке должно быть запрещено: %s %v", h.Status, h.canDouble())
	}
	if err := h.act("double"); err != errBadAction {
		t.Fatalf("ждали errBadAction, получили %v", err)
	}
	h = newBlackjackHand("ok", player{UserID: 1}, stakeMax/2, deck)
	if !h.canDouble() {
		t.Fatal("удвоение до максимума должно быть разрешено")
	}
}

func TestCasinoAllowsDiscordFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	allowDiscordFrame(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors") || !strings.Contains(got, "discordsays.com") {
		t.Fatalf("нет разрешения на показ внутри Discord: %q", got)
	}
}
