package casino

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"testing"
)

// Монте-Карло симуляции игр казино: CASINO_SIM=1 go test ./internal/casino -run Sim -v
// Без переменной пропускаются — миллионы партий занимают время.

func simEnabled(t *testing.T) {
	if os.Getenv("CASINO_SIM") == "" {
		t.Skip("CASINO_SIM не задан")
	}
}

type simStats struct {
	games          int
	staked, payout int64
	wins           int
}

func (s *simStats) add(stake, payout int) {
	s.games++
	s.staked += int64(stake)
	s.payout += int64(payout)
	if payout > stake {
		s.wins++
	}
}

func (s simStats) rtp() float64 { return float64(s.payout) / float64(s.staked) }

// sessions прогоняет players игроков по games ставок и печатает разброс итогов.
func sessions(t *testing.T, name string, players, games int, play func() (stake, payout int)) {
	nets := make([]float64, players)
	var all simStats
	inProfit := 0
	for i := range nets {
		var s simStats
		for range games {
			st, p := play()
			s.add(st, p)
			all.add(st, p)
		}
		nets[i] = float64(s.payout-s.staked) / float64(s.staked)
		if s.payout > s.staked {
			inProfit++
		}
	}
	sort.Float64s(nets)
	q := func(p float64) float64 { return nets[int(p*float64(len(nets)-1))] * 100 }
	t.Logf("  %-28s %4d игр × %5d игроков: RTP %6.2f%%, в плюсе %5.1f%%, итог p5 %+7.1f%% / медиана %+6.1f%% / p95 %+7.1f%%",
		name, games, players, all.rtp()*100, float64(inProfit)/float64(players)*100, q(0.05), q(0.5), q(0.95))
}

func checkRTP(t *testing.T, name string, s simStats, want, tol float64) {
	t.Helper()
	got := s.rtp()
	mark := "ok"
	if math.Abs(got-want) > tol {
		mark = "РАСХОЖДЕНИЕ"
		t.Errorf("%s: RTP %.4f, теория %.4f (±%.4f)", name, got, want, tol)
	}
	t.Logf("  %-28s %8d игр: RTP %6.2f%% (теория %6.2f%%), выигрышей %5.1f%%  %s",
		name, s.games, got*100, want*100, float64(s.wins)/float64(s.games)*100, mark)
}

// ---------- Рулетка ----------

func TestSimRoulette(t *testing.T) {
	simEnabled(t)
	const spins = 2_000_000
	// равномерность колеса: хи-квадрат по 37 секторам
	counts := make([]int, 37)
	for range spins {
		counts[rand.IntN(37)]++
	}
	exp := float64(spins) / 37
	chi := 0.0
	for _, c := range counts {
		chi += (float64(c) - exp) * (float64(c) - exp) / exp
	}
	t.Logf("колесо: χ² = %.1f при 36 степенях свободы (критическое 0.1%% ≈ 67.98)", chi)
	if chi > 67.98 {
		t.Errorf("колесо неравномерно: χ² = %.1f", chi)
	}

	bets := []struct {
		kind  string
		value int
	}{
		{"red", 0}, {"black", 0}, {"even", 0}, {"odd", 0}, {"low", 0}, {"high", 0},
		{"dozen", 1}, {"dozen", 3}, {"number", 0}, {"number", 17},
	}
	const stake = 100
	for _, b := range bets {
		var s simStats
		for range spins / 4 {
			m, _ := rouletteMultiplier(b.kind, b.value, rand.IntN(37))
			s.add(stake, roundPayout(stake, m))
		}
		// для числа дисперсия огромная — допуск шире
		tol := 0.01
		if b.kind == "number" {
			tol = 0.05
		}
		checkRTP(t, fmt.Sprintf("%s %d", b.kind, b.value), s, 36.0/37, tol)
	}
	for _, games := range []int{100, 1000} {
		for _, b := range []struct {
			kind  string
			value int
		}{{"red", 0}, {"number", 7}} {
			sessions(t, "рулетка "+b.kind, 2000, games, func() (int, int) {
				m, _ := rouletteMultiplier(b.kind, b.value, rand.IntN(37))
				return stake, roundPayout(stake, m)
			})
		}
	}
}

// ---------- Plinko ----------

func TestSimPlinko(t *testing.T) {
	simEnabled(t)
	const drops = 500_000
	const stake = 100
	for n := plinkoRowsMin; n <= plinkoRowsMax; n++ {
		odds := plinkoOdds(n)
		for _, risk := range []string{"low", "medium", "high"} {
			table := plinkoTables[n][risk]
			exact := 0.0
			for k, m := range table {
				exact += odds[k] * m
			}
			var s simStats
			slots := make([]int, n+1)
			for range drops {
				_, slot := plinkoDrop(n)
				slots[slot]++
				s.add(stake, roundPayout(stake, table[slot]))
			}
			// хи-квадрат распределения по лункам против биномиального (крайние лунки объединяем с соседями — там мало попаданий)
			chi, df := 0.0, -1
			var o, e float64
			for k := range slots {
				o += float64(slots[k])
				e += odds[k] * drops
				if e >= 20 || k == n {
					chi += (o - e) * (o - e) / e
					df++
					o, e = 0, 0
				}
			}
			// приблизительный порог 0.1% для df ≤ 16: df + 3.5·√(2df)
			limit := float64(df) + 3.5*math.Sqrt(2*float64(df))
			if chi > limit {
				t.Errorf("%d %s: лунки распределены не по биному, χ²=%.1f (df %d)", n, risk, chi, df)
			}
			tol := 0.01
			if risk == "high" {
				tol = 0.06 // крайние лунки x100+ сильно раскачивают выборку
			}
			checkRTP(t, fmt.Sprintf("plinko %2d %-6s (точно %.4f)", n, risk, exact), s, exact, tol)
			if math.Abs(exact-plinkoRTP) > 0.005 {
				t.Errorf("%d %s: точный RTP после округления таблицы %.4f", n, risk, exact)
			}
		}
	}
	for _, games := range []int{100, 1000} {
		for _, risk := range []string{"low", "high"} {
			table := plinkoTables[16][risk]
			sessions(t, "plinko 16 "+risk, 2000, games, func() (int, int) {
				_, slot := plinkoDrop(16)
				return stake, roundPayout(stake, table[slot])
			})
		}
	}
	t.Logf("таблица 16 high: %v", plinkoTables[16]["high"])
	t.Logf("таблица 8 low:   %v", plinkoTables[8]["low"])
}

// ---------- Рокетка ----------

// rocketTheory — точный RTP при выводе на m: P(crash > m) · m.
// crash = floor(99/(1-u))/100 > m  ⇔  99/(1-u) ≥ 100m+1  ⇔  1-u ≤ 99/(100m+1).
func rocketTheory(m float64) float64 {
	if m >= rocketMax {
		return 0
	}
	return math.Min(1, 99/(math.Round(m*100)+1)) * m
}

func TestSimRocket(t *testing.T) {
	simEnabled(t)
	const rounds = 2_000_000
	const stake = 100
	for _, auto := range []float64{1.01, 1.1, 1.5, 2, 3, 5, 10, 50, 99.99} {
		var s simStats
		for range rounds {
			crash := crashPoint(rand.Float64())
			_, mult := rocketOutcome(crash, auto, crash) // ракета долетела до взрыва — сработает только автовывод
			s.add(stake, roundPayout(stake, mult))
		}
		tol := 0.01 + 0.002*auto
		checkRTP(t, fmt.Sprintf("рокетка автовывод x%.2f", auto), s, rocketTheory(auto), tol)
	}
	// распределение точки взрыва
	inst, capped := 0, 0
	for range rounds {
		c := crashPoint(rand.Float64())
		if c == 1 {
			inst++
		}
		if c == rocketMax {
			capped++
		}
	}
	t.Logf("  взрыв сразу на x1.00: %.3f%% (теория 1.00%%), упёрлись в потолок x100: %.3f%% (теория %.3f%%)",
		float64(inst)/rounds*100, float64(capped)/rounds*100, 99.0/10000*100)
	for _, games := range []int{100, 1000} {
		for _, auto := range []float64{2, 10} {
			sessions(t, fmt.Sprintf("рокетка авто x%.0f", auto), 2000, games, func() (int, int) {
				crash := crashPoint(rand.Float64())
				_, mult := rocketOutcome(crash, auto, crash)
				return stake, roundPayout(stake, mult)
			})
		}
	}
}

// ---------- Блэкджек ----------

// basicStrategy — базовая стратегия для одной колоды, дилер стоит на мягких 17, без сплита.
func basicStrategy(h *blackjackHand) string {
	pt, soft := handTotal(h.Player)
	up := min(h.Dealer[0].Rank, 10)
	if up == 1 {
		up = 11
	}
	two := len(h.Player) == 2
	dbl := func(ok bool, fallback string) string {
		if ok && two && h.canDouble() {
			return "double"
		}
		return fallback
	}
	if soft {
		switch {
		case pt >= 19:
			return "stand"
		case pt == 18:
			if up >= 3 && up <= 6 {
				return dbl(true, "stand")
			}
			if up == 2 || up == 7 || up == 8 {
				return "stand"
			}
			return "hit"
		case pt == 17:
			return dbl(up >= 3 && up <= 6, "hit")
		case pt >= 15:
			return dbl(up >= 4 && up <= 6, "hit")
		case pt >= 13:
			return dbl(up >= 5 && up <= 6, "hit")
		}
		return "hit"
	}
	switch {
	case pt >= 17:
		return "stand"
	case pt >= 13:
		if up <= 6 {
			return "stand"
		}
		return "hit"
	case pt == 12:
		if up >= 4 && up <= 6 {
			return "stand"
		}
		return "hit"
	case pt == 11:
		return dbl(true, "hit")
	case pt == 10:
		return dbl(up <= 9, "hit")
	case pt == 9:
		return dbl(up <= 6, "hit")
	case pt == 8:
		return dbl(up == 5 || up == 6, "hit")
	}
	return "hit"
}

// mimicDealer — играть как дилер: добирать до 17.
func mimicDealer(h *blackjackHand) string {
	if pt, _ := handTotal(h.Player); pt < 17 {
		return "hit"
	}
	return "stand"
}

func playBlackjack(strategy func(*blackjackHand) string, stake int) (int, int, string) {
	h := newBlackjackHand("sim", player{}, stake, shuffledDeck())
	for h.Status == "playing" {
		if err := h.act(strategy(h)); err != nil {
			panic(err)
		}
	}
	return h.Stake, roundPayout(h.Stake, h.Mult), h.Status
}

func TestSimBlackjack(t *testing.T) {
	simEnabled(t)
	const hands = 2_000_000
	const stake = 100
	for _, st := range []struct {
		name string
		f    func(*blackjackHand) string
		want float64 // ориентир по литературе для этих правил
		tol  float64
	}{
		// одна колода, S17, 3:2, удвоение на любых двух, без сплита ≈ −0.4…−0.6% к базовой без сплита
		{"базовая стратегия", basicStrategy, 0.995, 0.008},
		// «как дилер» — классически около −5.5%
		{"как дилер", mimicDealer, 0.945, 0.015},
	} {
		var s simStats
		var staked int64
		statuses := map[string]int{}
		for range hands {
			st2, p, status := playBlackjack(st.f, stake)
			s.add(st2, p)
			staked += int64(stake) // RTP на исходную ставку — так считают house edge
			statuses[status]++
		}
		edgeOnInitial := float64(s.payout-s.staked) / float64(staked) * 100
		checkRTP(t, "блэкджек "+st.name, s, st.want, st.tol)
		t.Logf("    преимущество игрока на исходную ставку: %+.2f%%; исходы: %v", edgeOnInitial, pct(statuses, hands))
	}
	// разовые проверки честности колоды: 52 разные карты, каждая карта на каждой позиции равновероятна
	pos0 := make([]int, 52)
	for range 520_000 {
		d := shuffledDeck()
		seen := map[card]bool{}
		for _, c := range d {
			seen[c] = true
		}
		if len(seen) != 52 {
			t.Fatal("в колоде повторы")
		}
		pos0[(d[0].Rank-1)*4+d[0].Suit]++
	}
	chi := 0.0
	for _, c := range pos0 {
		chi += (float64(c) - 10000) * (float64(c) - 10000) / 10000
	}
	t.Logf("  первая карта колоды: χ² = %.1f при 51 степени свободы (критическое 0.1%% ≈ 87.97)", chi)
	if chi > 87.97 {
		t.Errorf("тасовка неравномерна: χ² = %.1f", chi)
	}
	for _, games := range []int{100, 1000} {
		sessions(t, "блэкджек базовая", 2000, games, func() (int, int) {
			s, p, _ := playBlackjack(basicStrategy, stake)
			return s, p
		})
	}
}

func pct(m map[string]int, n int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += fmt.Sprintf("%s %.1f%% ", k, float64(m[k])/float64(n)*100)
	}
	return out
}
