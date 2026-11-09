package main

// Тесты ходят в настоящую базу из DATABASE_URL, но всё выполняется в транзакции и откатывается.
// Discord, OpenDota и PUBG подменены, поэтому реальных запросов к ним нет.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	testGuild   = "1"
	testGuildID = int64(1)
)

type discordCall struct{ Method, Path, Body string }

type fakeDiscord struct {
	mu    sync.Mutex
	calls []discordCall
}

func (f *fakeDiscord) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, discordCall{req.Method, req.URL.Path, string(body)})
	f.mu.Unlock()
	return &http.Response{
		StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Request: req,
		Body: io.NopCloser(strings.NewReader(`{"id":"555","channel_id":"777"}`)),
	}, nil
}

func (f *fakeDiscord) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// text — все запросы к Discord одной строкой.
func (f *fakeDiscord) text() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, c := range f.calls {
		fmt.Fprintf(&b, "%s %s %s\n", c.Method, c.Path, c.Body)
	}
	return b.String()
}

func setup(t *testing.T) *fakeDiscord {
	t.Helper()
	loadConfig()
	if cfg.DatabaseURL == "" {
		t.Skip("нет DATABASE_URL")
	}
	pool, err := connectDB(cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		tx.Rollback(ctx)
		pool.Close()
	})
	if _, err := tx.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	db = tx
	fake := &fakeDiscord{}
	session, _ = discordgo.New("Bot test")
	session.Client = &http.Client{Transport: fake}
	botUserID = 999
	wheelSpinTime = 0
	dotaRequestPause = 0
	for _, c := range commandList() {
		handlers[c.def.Name] = c.handler
	}
	return fake
}

func member(id int64) *discordgo.Member {
	return &discordgo.Member{
		User: &discordgo.User{ID: idStr(id), Username: "u" + idStr(id)}, Nick: "nick" + idStr(id),
		JoinedAt: time.Now().AddDate(0, -1, 0), Permissions: discordgo.PermissionManageGuild,
	}
}

type opt struct {
	name  string
	kind  discordgo.ApplicationCommandOptionType
	value any
}

func user(name string, id int64) opt {
	return opt{name, discordgo.ApplicationCommandOptionUser, idStr(id)}
}
func num(name string, v int) opt {
	return opt{name, discordgo.ApplicationCommandOptionInteger, float64(v)}
}
func str(name, v string) opt { return opt{name, discordgo.ApplicationCommandOptionString, v} }

func run(from int64, name string, opts ...opt) {
	data := discordgo.ApplicationCommandInteractionData{
		Name: name, Resolved: &discordgo.ApplicationCommandInteractionDataResolved{
			Users: map[string]*discordgo.User{}, Members: map[string]*discordgo.Member{},
		},
	}
	for _, o := range opts {
		data.Options = append(data.Options, &discordgo.ApplicationCommandInteractionDataOption{Name: o.name, Type: o.kind, Value: o.value})
		if o.kind == discordgo.ApplicationCommandOptionUser {
			id := o.value.(string)
			m := member(parseID(id))
			data.Resolved.Users[id] = m.User
			data.Resolved.Members[id] = &discordgo.Member{Nick: m.Nick, JoinedAt: m.JoinedAt}
		}
	}
	onInteraction(session, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "10", AppID: "20", Token: "tok", Type: discordgo.InteractionApplicationCommand, GuildID: testGuild, ChannelID: "777",
		Member: member(from), Data: data,
	}})
}

func click(from int64, customID string, values ...string) {
	onInteraction(session, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "11", AppID: "20", Token: "tok", Type: discordgo.InteractionMessageComponent, GuildID: testGuild, ChannelID: "777",
		Member: member(from), Message: &discordgo.Message{ID: "555", ChannelID: "777"},
		Data: discordgo.MessageComponentInteractionData{CustomID: customID, Values: values},
	}})
}

func want(t *testing.T, fake *fakeDiscord, substr string) {
	t.Helper()
	if !strings.Contains(fake.text(), substr) {
		t.Fatalf("нет %q в ответах:\n%s", substr, fake.text())
	}
	fake.reset()
}

func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if _, err := fetchOne(sql, args, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func entries(t *testing.T, id int64) stat {
	t.Helper()
	st, err := userStats(testGuildID, id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if err := exec(sql, args...); err != nil {
		t.Fatal(err)
	}
}

func openPollID(t *testing.T, kind string) int64 {
	t.Helper()
	var id int64
	if _, err := fetchOne("SELECT id FROM polls WHERE guild_id = 1 AND kind = $1 AND status IN ('open', 'nominating') ORDER BY id DESC LIMIT 1", []any{kind}, &id); err != nil || id == 0 {
		t.Fatalf("нет открытого голосования %s: %v", kind, err)
	}
	return id
}

func closePoll(t *testing.T, fake *fakeDiscord, id int64) {
	t.Helper()
	fake.reset()
	mustExec(t, "UPDATE polls SET ends_at = now() - interval '1 minute' WHERE id = $1", id)
	pollsLoop()
}

func TestPureLogic(t *testing.T) {
	kda := []struct{ k, d, a, want int }{{10, 2, 0, 0}, {3, 1, 0, 10}, {2, 1, 0, 20}, {1, 1, 0, 30}, {0, 5, 1, 50}, {0, 7, 0, 70}, {5, 0, 3, 0}}
	for _, c := range kda {
		if _, got := kdaPushups(c.k, c.d, c.a); got != c.want {
			t.Errorf("kda %v: %d", c, got)
		}
	}
	for in, out := range map[string]int64{
		"https://steamcommunity.com/profiles/76561198324752433/": 364486705, "364486705": 364486705,
		"https://www.dotabuff.com/players/364486705": 364486705,
	} {
		if got, ok := parseAccountID(in); !ok || got != out {
			t.Errorf("%s: %d", in, got)
		}
	}
	if _, ok := parseAccountID("abc"); ok {
		t.Error("abc разобран как ID")
	}
	score := []struct {
		k, a      int
		d         float64
		pts, push int
	}{{0, 0, 0, 0, 10}, {3, 0, 0, 300, 0}, {1, 1, 149.4, 299, 10}, {1, 1, 149.6, 300, 0}, {0, 2, 250, 350, 0}}
	for _, c := range score {
		if p, u := pubgScore(c.k, c.a, c.d); p != c.pts || u != c.push {
			t.Errorf("pubg %v: %d %d", c, p, u)
		}
	}
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := addMonths(jan, -3); got.Month() != time.October || got.Year() != 2025 {
		t.Error(got)
	}
	ids, v := leaders([]ranked{{1, 30}, {2, 10}, {3, 30}}, true)
	if len(ids) != 2 || v != 30 {
		t.Error(ids, v)
	}
	if ids, v = leaders([]ranked{{1, 30}, {2, 10}, {3, 30}}, false); len(ids) != 1 || ids[0] != 2 || v != 10 {
		t.Error(ids, v)
	}
	if n := len(commandList()); n != 27 {
		t.Errorf("команд: %d", n)
	}
	for _, c := range commandList() {
		if r := []rune(c.def.Description); len(r) > 100 || len(r) == 0 {
			t.Errorf("/%s: описание %d символов", c.def.Name, len(r))
		}
		for _, o := range c.def.Options {
			if r := []rune(o.Description); len(r) > 100 {
				t.Errorf("/%s %s: описание %d символов", c.def.Name, o.Name, len(r))
			}
		}
	}
}

func TestWheelLandsOnChosenSector(t *testing.T) {
	isYellow := func(r, g, b uint32) bool { return r>>8 > 210 && g>>8 > 170 && b>>8 < 110 }
	isRed := func(r, g, b uint32) bool { return r>>8 > 180 && g>>8 < 90 && b>>8 < 90 }
	dir := os.Getenv("WHEEL_PREVIEW")
	for _, coef := range wheelMultipliers {
		for i := range 24 {
			win := i%2 == 0
			start, pointer := pickSpin(coef, win)
			gifData, pngData, err := renderWheel(coef, start, pointer)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(pngData))
			if err != nil {
				t.Fatal(err)
			}
			yellow, red := 0, 0
			for d := 30; d < 96; d++ {
				r, g, b, _ := img.At(wheelSize/2, wheelSize/2-d).RGBA()
				if isYellow(r, g, b) {
					yellow++
				}
				if isRed(r, g, b) {
					red++
				}
			}
			if (yellow > red) != win {
				t.Errorf("x%d win=%v: жёлтых %d, красных %d", coef, win, yellow, red)
			}
			if i == 0 {
				anim, err := gif.DecodeAll(bytes.NewReader(gifData))
				if err != nil {
					t.Fatal(err)
				}
				if len(anim.Image) != wheelFrames {
					t.Errorf("кадров %d", len(anim.Image))
				}
				if dir != "" {
					os.WriteFile(filepath.Join(dir, fmt.Sprintf("go_x%d.png", coef)), pngData, 0o644)
					os.WriteFile(filepath.Join(dir, fmt.Sprintf("go_x%d.gif", coef)), gifData, 0o644)
				}
				t.Logf("x%d: gif %d КБ", coef, len(gifData)/1024)
			}
		}
	}
}

func TestPushupCommands(t *testing.T) {
	fake := setup(t)
	run(7, "done", num("amount", 20))
	want(t, fake, "засчитано **20**")
	run(7, "owe", user("user", 8), num("amount", 30), str("reason", "спор"))
	want(t, fake, "назначил")
	if st := entries(t, 8); st.Owed != 30 || st.remaining() != 30 {
		t.Fatal(st)
	}
	run(8, "status")
	want(t, fake, "Отжимания: nick8")
	run(7, "undo")
	want(t, fake, "отменил назначение 30")
	if entries(t, 8).Owed != 0 {
		t.Fatal("/undo не сработал")
	}
	run(7, "history")
	want(t, fake, "сделал 20")

	run(7, "done_ludoman", num("amount", 20), num("multiplier", 5))
	text := fake.text()
	if !strings.Contains(text, "крутит колесо") || !strings.Contains(text, `filename="wheel.gif"`) {
		t.Fatal(text)
	}
	if !strings.Contains(text, "20 × **5** = **100**") && !strings.Contains(text, "20 × **0** = **0**") {
		t.Fatal(text)
	}
	if strings.Contains(text, "wheel.png") {
		t.Fatal("гифка должна остаться без замены", text)
	}
	fake.reset()
	got := entries(t, 7).Done
	if got != 20 && got != 120 {
		t.Fatal(got)
	}
	run(7, "undo")
	want(t, fake, "Казино ставок не возвращает")

	balance, err := ludomanBalance(testGuildID)
	if err != nil {
		t.Fatal(err)
	}
	if balance[7] != 80 && balance[7] != -20 {
		t.Fatal(balance)
	}
	run(7, "top", str("board", "done"))
	want(t, fake, fmt.Sprintf("(%+d)", balance[7]))
}

func TestCitizenshipAndPolls(t *testing.T) {
	fake := setup(t)
	session.State.GuildAdd(&discordgo.Guild{ID: testGuild, Roles: []*discordgo.Role{{ID: "500", Name: "Гражданин"}}})
	for _, id := range []int64{1, 2, 3, 4} {
		run(id, "founder", user("user", id))
	}
	want(t, fake, "/guilds/1/members/4/roles/500")
	if n := count(t, "SELECT count(*)::int FROM citizens WHERE guild_id = 1"); n != 4 {
		t.Fatal(n)
	}
	// поручительства
	run(1, "vouch", user("user", 10))
	want(t, fake, "Поручителей: 1/2")
	mustExec(t, "INSERT INTO entries (guild_id, user_id, kind, amount, author_id) VALUES (1, 10, 'owe', 110, 1)")
	run(2, "vouch", user("user", 10))
	want(t, fake, "долг 110")
	run(10, "done", num("amount", 11))
	want(t, fake, "стал гражданином")
	run(99, "vouch", user("user", 11))
	want(t, fake, "только граждане")

	// заморозка
	mustExec(t, "INSERT INTO entries (guild_id, user_id, kind, amount, author_id) VALUES (1, 4, 'owe', 301, 1)")
	run(4, "propose", str("text", "Закон о заморозке"))
	want(t, fake, "заморожены")

	// референдум: окончательный голос, итог большинством
	run(1, "propose", str("text", "Тестовый закон"))
	want(t, fake, "Тестовый закон")
	law := openPollID(t, "law")
	if n := count(t, "SELECT count(*)::int FROM polls WHERE id = $1 AND message_id = 555", law); n != 1 {
		t.Fatal("сообщение голосования не сохранено")
	}
	click(1, fmt.Sprintf("vote:%d:yes", law))
	want(t, fake, "Голос учтён")
	click(1, fmt.Sprintf("vote:%d:no", law))
	want(t, fake, "голос менять нельзя")
	click(99, fmt.Sprintf("vote:%d:yes", law))
	want(t, fake, "только граждане")
	closePoll(t, fake, law)
	want(t, fake, "Закон принят")
	run(1, "laws")
	want(t, fake, "Тестовый закон")

	// суд: большинство «за» → наказан обвиняемый; адвокат не обвинитель и не обвиняемый
	run(1, "accuse", user("user", 2), num("pushups", 50), str("reason", "руинил"))
	want(t, fake, "руинил")
	court := openPollID(t, "accuse")
	var lawyer int64
	fetchOne("SELECT lawyer_id FROM polls WHERE id = $1", []any{court}, &lawyer)
	if lawyer == 1 || lawyer == 2 || lawyer == 0 {
		t.Fatal("адвокат", lawyer)
	}
	click(2, fmt.Sprintf("vote:%d:yes", court))
	want(t, fake, "По своему делу")
	for _, id := range []int64{1, 3, 10} {
		click(id, fmt.Sprintf("vote:%d:yes", court))
	}
	closePoll(t, fake, court)
	want(t, fake, "Виновен (3 из 3 за)")
	if entries(t, 2).Owed != 50 {
		t.Fatal(entries(t, 2))
	}

	// суд без голосов → клевета
	run(1, "accuse", user("user", 3), num("pushups", 40), str("reason", "навет"))
	closePoll(t, fake, openPollID(t, "accuse"))
	want(t, fake, "за клевету")
	if entries(t, 1).Owed != 40 {
		t.Fatal(entries(t, 1))
	}

	// лишение гражданства: ⅔
	run(1, "revoke", user("user", 10))
	revoke := openPollID(t, "revoke")
	fake.reset()
	run(2, "revoke", user("user", 10))
	want(t, fake, "уже идёт")
	for _, id := range []int64{1, 2} {
		click(id, fmt.Sprintf("vote:%d:yes", revoke))
	}
	closePoll(t, fake, revoke)
	want(t, fake, "лишён гражданства (2 из 3 за)")

	// маргинал
	run(1, "marginal", user("user", 3))
	marg := openPollID(t, "marginal")
	for _, id := range []int64{1, 2} {
		click(id, fmt.Sprintf("vote:%d:yes", marg))
	}
	closePoll(t, fake, marg)
	want(t, fake, "признан маргиналом")
	run(1, "citizens")
	want(t, fake, "🚫")

	// выборы
	run(1, "nominate")
	want(t, fake, "нет выдвижения")
	run(1, "election")
	elect := openPollID(t, "election")
	fake.reset()
	run(2, "election")
	want(t, fake, "уже идут")
	run(1, "nominate")
	want(t, fake, "баллотируется")
	closePoll(t, fake, elect)
	want(t, fake, fmt.Sprintf("elect:%d", elect))
	click(2, fmt.Sprintf("elect:%d", elect), "1")
	want(t, fake, "Голос учтён")
	click(3, fmt.Sprintf("elect:%d", elect), "42")
	want(t, fake, "Такого кандидата нет")
	closePoll(t, fake, elect)
	want(t, fake, "Новый президент")
	if ok, _ := isPresident(testGuildID, 1); !ok {
		t.Fatal("президент не выбран")
	}
	run(2, "accuse", user("user", 1), num("pushups", 5), str("reason", "x"))
	want(t, fake, "нельзя забанить")
	run(2, "election")
	want(t, fake, "ещё не истёк")

	// /vouch за новичка
	id := idStr(20)
	m := member(20)
	onInteraction(session, &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "12", AppID: "20", Token: "tok", Type: discordgo.InteractionApplicationCommand, GuildID: testGuild, ChannelID: "777",
		Member: member(2),
		Data: discordgo.ApplicationCommandInteractionData{
			Name:    "vouch",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{{Name: "user", Type: discordgo.ApplicationCommandOptionUser, Value: id}},
			Resolved: &discordgo.ApplicationCommandInteractionDataResolved{
				Users:   map[string]*discordgo.User{id: m.User},
				Members: map[string]*discordgo.Member{id: {JoinedAt: time.Now()}},
			},
		},
	}})
	want(t, fake, "ручаться пока рано")
}

func TestDotaProcessing(t *testing.T) {
	fake := setup(t)
	now := time.Now()
	ago := func(h int) int64 { return now.Add(-time.Duration(h) * time.Hour).Unix() }
	m := func(id, start int64, k, d, a int) map[string]any {
		return map[string]any{"match_id": id, "start_time": start, "duration": 1800, "kills": k, "deaths": d, "assists": a}
	}
	matches := map[string][]map[string]any{
		// 1 — соло до включения, 2 — соло после, 3 — свежий соло, 4 — пати, 9 — до привязки
		"111": {m(1, ago(10), 1, 5, 1), m(2, ago(5), 1, 5, 1), m(3, ago(1), 1, 5, 1), m(4, ago(10), 10, 1, 0), m(9, ago(48), 1, 5, 1)},
		"222": {m(5, ago(5), 1, 5, 1), m(4, ago(10), 1, 1, 0)},
	}
	sawSignificant := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			return
		}
		sawSignificant = r.URL.Query().Get("significant") == "0"
		json.NewEncoder(w).Encode(matches[strings.Split(r.URL.Path, "/")[2]])
	}))
	defer srv.Close()
	opendotaBase = srv.URL

	mustExec(t, `INSERT INTO dota_links (guild_id, user_id, account_id, linked_by, linked_at, solo_since) VALUES
		(1, 1, 111, 0, $1, $2), (1, 2, 222, 0, $1, NULL)`, now.Add(-24*time.Hour), now.Add(-7*time.Hour))
	results, failed, err := processNewMatches(testGuildID)
	if err != nil || len(failed) != 0 {
		t.Fatal(err, failed)
	}
	got := map[string]bool{}
	for _, r := range results {
		got[fmt.Sprintf("%d:%d:%v", r.UserID, r.MatchID, r.Solo)] = true
	}
	for _, key := range []string{"1:2:true", "1:4:false", "2:4:false"} {
		if !got[key] {
			t.Errorf("нет %s в %v", key, got)
		}
	}
	if len(results) != 3 || !sawSignificant {
		t.Fatal(results, sawSignificant)
	}
	if again, _, _ := processNewMatches(testGuildID); len(again) != 0 {
		t.Fatal("повторный подсчёт", again)
	}
	if st := entries(t, 1); st.Owed != 50 { // матч 2: KDA 0.4 → 50, матч 4: KDA 10 → 0
		t.Fatal(st)
	}
	run(1, "dotalinks")
	want(t, fake, "соло")
	run(2, "dotasolo_on")
	want(t, fake, "теперь получает")
	run(2, "dotasolo_off")
	want(t, fake, "только за пати")
	run(50, "dotasolo_on")
	want(t, fake, "Сначала привяжи")
	run(1, "calculate_dota")
	want(t, fake, "Новых пати-матчей нет")
	run(1, "unlinkdota", user("user", 2))
	want(t, fake, "отвязан от `222`")
}

func TestPubgProcessing(t *testing.T) {
	fake := setup(t)
	cfg.PubgKey = "test"
	created := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	participant := func(id, account string, k, a int, d float64) map[string]any {
		return map[string]any{"type": "participant", "id": id, "attributes": map[string]any{"stats": map[string]any{
			"playerId": account, "kills": k, "assists": a, "damageDealt": d}}}
	}
	roster := func(ids ...string) map[string]any {
		refs := []map[string]string{}
		for _, id := range ids {
			refs = append(refs, map[string]string{"type": "participant", "id": id})
		}
		return map[string]any{"type": "roster", "id": "r" + ids[0], "attributes": map[string]any{"stats": map[string]any{"rank": 1}},
			"relationships": map[string]any{"participants": map[string]any{"data": refs}}}
	}
	attrs := func(kind, mode string) map[string]any {
		return map[string]any{"attributes": map[string]any{"createdAt": created, "matchType": kind, "gameMode": mode}}
	}
	matches := map[string]map[string]any{
		"m-party": {"data": attrs("official", "squad-fpp"),
			"included": []any{participant("p1", "account.a1", 1, 0, 120.4), participant("p2", "account.a2", 3, 1, 50),
				participant("p3", "account.a3", 0, 0, 10), participant("p4", "account.x", 5, 0, 900), roster("p1", "p2"), roster("p3", "p4")}},
		"m-arcade": {"data": attrs("arcade", "tdm"),
			"included": []any{participant("q1", "account.a1", 0, 0, 0), participant("q2", "account.a2", 0, 0, 0), roster("q1", "q2")}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/players" {
			var data []map[string]any
			for _, acc := range strings.Split(r.URL.Query().Get("filter[playerIds]"), ",") {
				data = append(data, map[string]any{"id": acc, "relationships": map[string]any{"matches": map[string]any{
					"data": []map[string]string{{"id": "m-party"}, {"id": "m-arcade"}}}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		json.NewEncoder(w).Encode(matches[strings.TrimPrefix(r.URL.Path, "/matches/")])
	}))
	defer srv.Close()
	pubgBase = srv.URL

	linked := time.Now().Add(-24 * time.Hour)
	for i, acc := range []string{"account.a1", "account.a2", "account.a3"} {
		mustExec(t, "INSERT INTO pubg_links (guild_id, user_id, account_id, name, linked_by, linked_at) VALUES (1, $1, $2, $2, 0, $3)", int64(101+i), acc, linked)
	}
	results, failed, err := processNewPubgMatches(testGuildID)
	if err != nil || len(failed) != 0 {
		t.Fatal(err, failed)
	}
	if len(results) != 2 {
		t.Fatal(results)
	}
	for _, r := range results {
		if (r.UserID == 101 && (r.Points != 220 || r.Pushups != 10)) || (r.UserID == 102 && (r.Points != 400 || r.Pushups != 0)) {
			t.Error(r)
		}
	}
	if entries(t, 101).Owed != 10 || entries(t, 102).Owed != 0 {
		t.Fatal(entries(t, 101), entries(t, 102))
	}
	if again, _, _ := processNewPubgMatches(testGuildID); len(again) != 0 {
		t.Fatal("повторный подсчёт", again)
	}
	run(101, "pubglinks")
	want(t, fake, "account.a1")
}

func TestAwards(t *testing.T) {
	fake := setup(t)
	session.State.GuildAdd(&discordgo.Guild{ID: testGuild})
	session.State.ChannelAdd(&discordgo.Channel{ID: "777", GuildID: testGuild})
	cfg.CivicChannelID = "777"
	now := time.Now().In(cfg.Location)
	monthEnd := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, cfg.Location)
	played := monthEnd.AddDate(0, 0, -5)
	mustExec(t, "INSERT INTO dota_links (guild_id, user_id, account_id, linked_by, linked_at) VALUES (1, 1, 1, 0, $1)", played.AddDate(0, -3, 0))
	for _, id := range []int64{1, 2, 3} {
		mustExec(t, "INSERT INTO citizens (guild_id, user_id) VALUES (1, $1)", id)
	}
	mustExec(t, "INSERT INTO marginals (guild_id, user_id) VALUES (1, 3)")
	game := func(user int64, matchID, pushups int) {
		mustExec(t, `INSERT INTO dota_matches (guild_id, user_id, match_id, kills, deaths, assists, pushups, played_at)
			VALUES (1, $1, $2, 0, 0, 0, $3, $4)`, user, matchID, pushups, played)
	}
	for i := range 11 {
		game(1, 1000+i, 30)
		game(2, 2000+i, 10)
		game(3, 3000+i, 70)
	}
	if err := maybeRunAwards(testGuildID); err != nil {
		t.Fatal(err)
	}
	text := fake.text()
	if !strings.Contains(text, "назначено, 330") || !strings.Contains(text, "назначено, 110") || strings.Contains(text, "назначено, 770") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "снова участвуют") || !strings.Contains(text, "@3") {
		t.Fatal(text)
	}
	fake.reset()
	if n := count(t, "SELECT count(*)::int FROM marginals WHERE guild_id = 1"); n != 0 {
		t.Fatal(n)
	}
	if err := maybeRunAwards(testGuildID); err != nil {
		t.Fatal(err)
	}
	if fake.text() != "" {
		t.Fatal("награды выданы дважды", fake.text())
	}
	scores, games, err := periodScores(testGuildID, played.Add(-time.Hour), played.Add(time.Hour))
	if err != nil || scores[1] != 330 || games[2] != 11 {
		t.Fatal(scores, games, err)
	}
}

func TestLiveDataStillReadable(t *testing.T) {
	setup(t)
	gid := parseID(cfg.GuildID)
	stats, err := guildStats(gid)
	if err != nil {
		t.Fatal(err)
	}
	balance, err := ludomanBalance(gid)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := citizenIDs(gid)
	if err != nil {
		t.Fatal(err)
	}
	polls, err := fetch[poll]("SELECT "+pollColumns+" FROM polls WHERE guild_id = $1", gid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("игроков %d, лудоманов %d, граждан %d, голосований %d", len(stats), len(balance), len(ids), len(polls))
	if len(stats) == 0 {
		t.Fatal("настоящие данные не прочитались")
	}
}
