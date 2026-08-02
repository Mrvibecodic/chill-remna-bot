package storage

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
)

// UseInvite должен быть атомарным «счётчиком с проверками»: не пускать сверх
// лимита, мимо срока и после отзыва.
func TestInvites(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()

		if err := st.CreateInvite(ctx, &model.Invite{Code: "two", MaxUses: 2}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			ok, err := st.UseInvite(ctx, "two")
			if err != nil || !ok {
				t.Fatalf("активация %d: ok=%v err=%v", i+1, ok, err)
			}
		}
		if ok, _ := st.UseInvite(ctx, "two"); ok {
			t.Fatal("третья активация сверх лимита не должна проходить")
		}
		inv, err := st.GetInvite(ctx, "two")
		if err != nil || inv == nil || inv.Used != 2 {
			t.Fatalf("счётчик активаций: %+v err=%v", inv, err)
		}

		past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
		_ = st.CreateInvite(ctx, &model.Invite{Code: "old", MaxUses: 0, ExpiresAt: past})
		if ok, _ := st.UseInvite(ctx, "old"); ok {
			t.Fatal("просроченное приглашение не должно активироваться")
		}

		_ = st.CreateInvite(ctx, &model.Invite{Code: "rev", MaxUses: 0})
		if err := st.RevokeInvite(ctx, "rev"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.UseInvite(ctx, "rev"); ok {
			t.Fatal("отозванное приглашение не должно активироваться")
		}

		list, err := st.ListInvites(ctx)
		if err != nil || len(list) != 3 {
			t.Fatalf("список приглашений: %d err=%v", len(list), err)
		}
		if err := st.DeleteInvite(ctx, "rev"); err != nil {
			t.Fatal(err)
		}
		if inv, _ := st.GetInvite(ctx, "rev"); inv != nil {
			t.Fatal("удалённое приглашение не должно читаться")
		}
		if ok, _ := st.UseInvite(ctx, "нет-такого"); ok {
			t.Fatal("несуществующий код не должен активироваться")
		}
	})
}

func TestAutoPayStore(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if err := st.UpsertUser(ctx, 21); err != nil {
			t.Fatal(err)
		}
		ap := &model.AutoPay{
			TelegramID: 21, Method: model.PayMethodYooKassa, MethodID: "pm_1",
			Title: "•••• 4242", Months: 3, Amount: "300.00", Currency: "RUB", Enabled: true,
		}
		if err := st.SetAutoPay(ctx, ap); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetAutoPay(ctx, 21)
		if err != nil || got == nil || got.MethodID != "pm_1" || got.Months != 3 || !got.Enabled {
			t.Fatalf("чтение автосписания: %+v err=%v", got, err)
		}

		if err := st.SetAutoPayEnabled(ctx, 21, false); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetAutoPay(ctx, 21); got == nil || got.Enabled {
			t.Fatalf("выключение автосписания: %+v", got)
		}

		if err := st.UpdateAutoPayResult(ctx, 21, "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", 2, "нет денег"); err != nil {
			t.Fatal(err)
		}
		got, _ = st.GetAutoPay(ctx, 21)
		if got.Fails != 2 || got.LastError != "нет денег" || got.NextTryAt == "" {
			t.Fatalf("исход попытки не сохранён: %+v", got)
		}

		list, err := st.ListAutoPay(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("список автосписаний: %d err=%v", len(list), err)
		}

		// Удаление пользователя должно уносить и автосписание, иначе бот
		// продолжит списывать деньги за удалённого.
		if err := st.DeleteUser(ctx, 21); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetAutoPay(ctx, 21); got != nil {
			t.Fatalf("автосписание удалённого пользователя осталось: %+v", got)
		}
	})
}

// Закрытие публичного бота сохраняет доступ уже зарегистрированным.
func TestWhitelistAllUsers(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		for _, id := range []int64{31, 32, 33} {
			if err := st.UpsertUser(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.SetWhitelisted(ctx, 31, true); err != nil {
			t.Fatal(err)
		}
		n, err := st.WhitelistAllUsers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("ожидалось 2 обновлённых пользователя, получено %d", n)
		}
		cnt, err := st.CountWhitelisted(ctx)
		if err != nil || cnt != 3 {
			t.Fatalf("CountWhitelisted = %d, err=%v", cnt, err)
		}
		for _, id := range []int64{31, 32, 33} {
			u, _ := st.GetUser(ctx, id)
			if u == nil || !u.Whitelisted {
				t.Fatalf("пользователь %d должен иметь доступ: %+v", id, u)
			}
		}
	})
}

// Повторное включение автопродления снимает паузу до следующей попытки.
func TestAutoPayEnableClearsRetryPause(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		_ = st.UpsertUser(ctx, 41)
		_ = st.SetAutoPay(ctx, &model.AutoPay{TelegramID: 41, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: false})
		if err := st.UpdateAutoPayResult(ctx, 41, "", "2099-01-01T00:00:00Z", 2, "нет денег"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetAutoPayEnabled(ctx, 41, true); err != nil {
			t.Fatal(err)
		}
		ap, _ := st.GetAutoPay(ctx, 41)
		if ap.NextTryAt != "" || ap.Fails != 0 || ap.LastError != "" {
			t.Fatalf("включение должно сбрасывать паузу и счётчик: %+v", ap)
		}
	})
}
