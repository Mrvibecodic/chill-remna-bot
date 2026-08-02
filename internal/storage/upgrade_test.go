package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// Симуляция апгрейда с dev-сборки, где 0037 выполнилась ещё без paid_period:
// 0038 обязана добавить колонку, и autopay-запросы должны работать.
func TestUpgradeAddsPaidPeriod(t *testing.T) {
	dir := t.TempDir()
	st, err := Open("sqlite", filepath.Join(dir, "bot.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Откатываем к состоянию dev-сборки: колонки нет, но версия 37 отмечена,
	// а запись 38 удаляем, как будто её ещё не было.
	sq := st.(*sqliteStore)
	if _, err := sq.db.ExecContext(ctx, "ALTER TABLE autopay DROP COLUMN paid_period"); err != nil {
		t.Fatal(err)
	}
	if _, err := sq.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 38"); err != nil {
		t.Fatal(err)
	}
	// Апгрейд: миграция 0038 должна добавить колонку.
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("апгрейд: %v", err)
	}
	if _, err := st.GetAutoPay(ctx, 1); err != nil {
		t.Fatalf("autopay после апгрейда: %v", err)
	}
}
