package bot

import (
	"testing"

	"pushups-bot/internal/store"
)

func TestSurplusShown(t *testing.T) {
	for _, c := range []struct{ owed, done, remaining, surplus int }{
		{30, 10, 20, 0}, {30, 30, 0, 0}, {30, 50, 0, 20}, {0, 15, 0, 15},
	} {
		st := store.Stat{Owed: c.owed, Done: c.done}
		if st.Remaining() != c.remaining || st.Surplus() != c.surplus {
			t.Fatalf("%+v: осталось %d, запас %d", c, st.Remaining(), st.Surplus())
		}
	}

	fake := setup(t)
	run(20, "done", num("amount", 50))
	want(t, fake, "в запасе **+50**")
	run(21, "owe", user("user", 20), num("amount", 30))
	want(t, fake, "в запасе **+20**")
	run(20, "status")
	want(t, fake, `"name":"В запасе","value":"+20"`)
	run(21, "owe", user("user", 20), num("amount", 40))
	want(t, fake, "Теперь осталось: **20**")
	run(20, "status")
	want(t, fake, `"name":"Осталось","value":"20"`)
	run(20, "done", num("amount", 20))
	want(t, fake, "Долгов нет — красавчик!")
}
