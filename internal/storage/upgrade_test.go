package storage

import (
	"context"
	"path/filepath"
	"testing"

	"remnabot/internal/model"
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

// Регресс блокера релиза: dev-установки, где СТАРАЯ редакция 0037 уже добавила
// paid_period (версия 37 записана, 0038 тогда не существовал), не должны падать
// на 0038 — SQLite не умеет ADD COLUMN IF NOT EXISTS.
func TestUpgradeToleratesPreexistingPaidPeriod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev37.db")
	st, err := Open("sqlite", path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	sq := st.(*sqliteStore)
	ctx := context.Background()
	// Имитация установки на старой редакции 0037: колонка уже есть, а версия 38
	// ещё не записана.
	if _, err := sq.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version >= 38"); err != nil {
		t.Fatal(err)
	}
	// Повторный прогон миграций обязан пережить «duplicate column name» на 0038
	// и дойти до конца.
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("апгрейд dev-установки с уже существующей колонкой упал: %v", err)
	}
	var v int
	if err := sq.db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v < 40 {
		t.Fatalf("миграции не дошли до конца: версия %d", v)
	}
	// Автопродление после такого апгрейда должно работать.
	if err := st.SetAutoPay(ctx, &model.AutoPay{TelegramID: 1, Method: "yookassa", MethodID: "pm", Months: 1, PaidPeriod: "2026-09"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAutoPay(ctx, 1)
	if err != nil || got == nil || got.PaidPeriod != "2026-09" {
		t.Fatalf("autopay после апгрейда: %+v err=%v", got, err)
	}
	_ = st.Close()
}
