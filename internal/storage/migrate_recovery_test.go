package storage

import (
	"context"
	"errors"
	"os"
	"testing"

	"remnabot/internal/model"
)

// Обрыв между шагом миграции и записью версии оставлял базу в состоянии,
// из которого бот не поднимался вообще — «колонка уже есть», лечится руками.
func TestMigrate_SurvivesInterruptedStep(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("нет TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	st, err := Open("postgres", dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("первый прогон: %v", err)
	}
	b := &st.(*pgStore).base

	// Имитируем обрыв: колонка добавлена, версия не записана.
	if _, err := b.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = 62"); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("после обрыва бот не поднимается: %v", err)
	}
	var n int
	if err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM schema_migrations WHERE version = 62").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("версия не зафиксирована: %d", n)
	}
}

// Транзакция: неудачный шаг не оставляет версию записанной.
func TestMigrate_StepAndVersionAreOneTransaction(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("нет TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	st, err := Open("postgres", dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	b := &st.(*pgStore).base

	err = applyMigration(ctx, b, "pg", "9999_bad.sql", 9999, "ALTER TABLE users ADD COLUMN")
	if err == nil {
		t.Fatal("кривая миграция прошла")
	}
	var n int
	if e := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM schema_migrations WHERE version = 9999").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n != 0 {
		t.Fatal("версия записана при неудачном шаге — следующий старт пропустит миграцию")
	}
}

// Столкновение первичных ключей (id платежа = наносекунды) читалось как
// «этот платёж уже проведён». Пополнение при этом теряется: finalizeTopUp на
// таком ответе выходит с nil, а транзакция откатилась и баланса нет.
func TestPayment_PKCollisionIsNotDuplicate(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()

		first := &model.Payment{ID: 424242, TelegramID: 111, Method: "yookassa", ExtID: "pay-A", Status: "paid"}
		if err := st.AddPayment(ctx, first); err != nil {
			t.Fatalf("первый платёж: %v", err)
		}
		// Другая сделка, но тот же id — «не повезло с часами».
		second := &model.Payment{ID: 424242, TelegramID: 222, Method: "yookassa", ExtID: "pay-B", Status: "paid"}
		if err := st.AddPayment(ctx, second); err != nil {
			t.Fatalf("столкновение id принято за дубль сделки: %v", err)
		}
		if second.ID == first.ID {
			t.Fatal("id не перевыдан")
		}
		if ok, _ := st.PaymentByExtID(ctx, "pay-B"); !ok {
			t.Fatal("второй платёж не записан — деньги потеряны")
		}

		// А настоящий дубль ключа сделки по-прежнему распознаётся.
		dup := &model.Payment{TelegramID: 111, Method: "yookassa", ExtID: "pay-A", Status: "paid"}
		if err := st.AddPayment(ctx, dup); !errors.Is(err, ErrDuplicateExtID) {
			t.Fatalf("дубль ключа сделки не распознан: %v", err)
		}
	})
}
