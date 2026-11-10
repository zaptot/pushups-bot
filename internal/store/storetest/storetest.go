// Package storetest готовит базу для тестов: все изменения идут в транзакции, которая откатывается.
package storetest

import (
	"context"
	"testing"

	"pushups-bot/internal/config"
	"pushups-bot/internal/store"
)

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
	if _, err := tx.Exec(ctx, store.Schema); err != nil {
		t.Fatal(err)
	}
	store.DB = tx
}
