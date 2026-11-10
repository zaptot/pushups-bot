// Package storetest готовит базу для тестов: все изменения идут в транзакции, которая откатывается.
package storetest

import (
	"context"
	"testing"

	"pushups-bot/internal/config"
	"pushups-bot/internal/store"
)

// schemaLock — ключ advisory-лока для тестов; не пересекается с id пользователей из economy.Apply.
const schemaLock int64 = -7_301_000_001

// Begin подключается к базе из DATABASE_URL (или пропускает тест, если её нет), открывает транзакцию,
// создаёт схему и подменяет store.DB на эту транзакцию до конца теста.
func Begin(t testing.TB) {
	t.Helper()
	config.Load()
	if config.Values.DatabaseURL == "" {
		t.Skip("нет DATABASE_URL")
	}
	pool, err := store.Connect(config.Values.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	previous := store.DB
	t.Cleanup(func() {
		store.DB = previous
		tx.Rollback(ctx)
		pool.Close()
	})
	// go test запускает пакеты параллельно, и все они ходят в одну базу. ALTER TABLE из схемы держит
	// эксклюзивные блокировки до отката, и транзакции разных пакетов ловили deadlock (40P01).
	// Общий advisory-лок до конца транзакции выстраивает тесты с базой в очередь.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLock); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, store.Schema); err != nil {
		t.Fatal(err)
	}
	store.DB = tx
}
