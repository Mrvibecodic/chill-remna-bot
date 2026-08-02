package app

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/yookassa"
)

func autoPayApp(t *testing.T) (*App, *fakeStore) {
	t.Helper()
	a, fs := refTestApp(t)
	a.botCfg.YooKassa = model.YooKassaConfig{
		Enabled: true, ShopID: "shop", SecretKey: "sec", AutoPay: true, AutoPayDays: 1,
	}
	a.botCfg.NormalizeYooKassa()
	return a, fs
}

// Успешный платёж с сохранённым способом оплаты включает автопродление.
func TestAutoPay_SavedFromPayment(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()

	pay := &yookassa.Payment{ID: "pay_1"}
	pay.Metadata = map[string]string{"autopay": "1", "telegram_id": "42", "months": "3"}
	pay.PaymentMethod.ID = "pm_1"
	pay.PaymentMethod.Saved = true
	pay.PaymentMethod.Card.Last4 = "4242"
	pay.Amount.Value = "300.00"
	pay.Amount.Currency = "RUB"

	a.saveAutoPayFromPayment(ctx, 42, 3, pay)

	ap, _ := fs.GetAutoPay(ctx, 42)
	if ap == nil || !ap.Enabled || ap.MethodID != "pm_1" || ap.Months != 3 {
		t.Fatalf("автопродление не сохранено: %+v", ap)
	}
	if !a.autoPayOn(ctx, 42) {
		t.Fatal("autoPayOn должен быть true")
	}
}

// Обычный платёж (без выбора автопродления) ничего не подключает.
func TestAutoPay_NotSavedWithoutFlag(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()

	pay := &yookassa.Payment{ID: "pay_2"}
	pay.PaymentMethod.ID = "pm_2"
	pay.PaymentMethod.Saved = true // ЮKassa сохранила, но пользователь не просил
	a.saveAutoPayFromPayment(ctx, 43, 1, pay)

	if ap, _ := fs.GetAutoPay(ctx, 43); ap != nil {
		t.Fatalf("автопродление не должно подключаться без флага: %+v", ap)
	}
}

// Пользователь выключает автопродление — запись остаётся, но списаний нет.
func TestAutoPay_UserCanDisableAndEnable(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()
	_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: 44, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: true})

	if err := a.SetAutoPayEnabled(ctx, 44, false); err != nil {
		t.Fatalf("выключение: %v", err)
	}
	if a.autoPayOn(ctx, 44) {
		t.Fatal("после выключения автопродление должно быть выключено")
	}
	if err := a.SetAutoPayEnabled(ctx, 44, true); err != nil {
		t.Fatalf("включение: %v", err)
	}
	if !a.autoPayOn(ctx, 44) {
		t.Fatal("после включения автопродление должно работать")
	}
	// Без сохранённого способа оплаты включать нечего.
	if err := a.SetAutoPayEnabled(ctx, 45, true); err == nil {
		t.Fatal("включение без сохранённой карты должно возвращать ошибку")
	}
}

// Списываем только когда подписка вот-вот кончится и пауза после неудачи прошла.
func TestAutoPay_DueWindow(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()
	now := time.Now().UTC()

	_ = fs.UpsertUser(ctx, 46)
	_ = fs.SetSubExpiry(ctx, 46, now.Add(10*24*time.Hour).Format(time.RFC3339), "paid")
	ap := &model.AutoPay{TelegramID: 46, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: true}
	_ = fs.SetAutoPay(ctx, ap)

	if a.autoPayDue(ctx, ap, now) {
		t.Fatal("за 10 дней до конца списывать рано")
	}
	_ = fs.SetSubExpiry(ctx, 46, now.Add(12*time.Hour).Format(time.RFC3339), "paid")
	if !a.autoPayDue(ctx, ap, now) {
		t.Fatal("за полдня до конца пора списывать")
	}

	ap.NextTryAt = now.Add(6 * time.Hour).Format(time.RFC3339)
	if a.autoPayDue(ctx, ap, now) {
		t.Fatal("пауза после неудачной попытки должна соблюдаться")
	}
	ap.NextTryAt = ""

	ap.Enabled = false
	if a.autoPayDue(ctx, ap, now) {
		t.Fatal("выключенное автопродление списывать нельзя")
	}
	ap.Enabled = true

	_ = fs.SetBlocked(ctx, 46, true)
	if a.autoPayDue(ctx, ap, now) {
		t.Fatal("заблокированному пользователю списывать нельзя")
	}
}

// После нескольких неудач подряд автопродление выключается само.
func TestAutoPay_DisablesAfterFails(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_ = fs.UpsertUser(ctx, 47)
	ap := &model.AutoPay{TelegramID: 47, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: true}
	_ = fs.SetAutoPay(ctx, ap)

	for i := 0; i < model.AutoPayMaxFails-1; i++ {
		cur, _ := fs.GetAutoPay(ctx, 47)
		a.autoPayFail(ctx, cur, now, "тест")
	}
	cur, _ := fs.GetAutoPay(ctx, 47)
	if !cur.Enabled {
		t.Fatalf("до лимита автопродление должно оставаться включённым: %+v", cur)
	}
	a.autoPayFail(ctx, cur, now, "тест")
	cur, _ = fs.GetAutoPay(ctx, 47)
	if cur.Enabled {
		t.Fatal("после лимита неудач автопродление должно выключиться")
	}
}

// Удаление пользователя не должно оставлять «висящее» автосписание.
func TestAutoPay_StateForMiniApp(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()
	_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: 48, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 6, Title: "•••• 1111", Enabled: true})

	dto := a.MiniAutoPay(ctx, 48)
	if !dto.Available || !dto.On || dto.Months != 6 || dto.Title != "•••• 1111" || !dto.CanEnable {
		t.Fatalf("состояние для мини-аппа: %+v", dto)
	}
	a.botCfg.YooKassa.AutoPay = false
	if a.MiniAutoPay(ctx, 48).Available {
		t.Fatal("при выключенных автоплатежах Available должен быть false")
	}
}

func TestMonthsWord(t *testing.T) {
	cases := map[int]string{1: "1 месяц", 3: "3 месяца", 6: "6 месяцев", 12: "12 месяцев"}
	for months, want := range cases {
		if got := monthsWord(model.LangRU, months); got != want {
			t.Errorf("monthsWord(%d) = %q, ожидалось %q", months, got, want)
		}
	}
	if got := monthsWord(model.LangEN, 3); got != "3 months" {
		t.Errorf("monthsWord(en,3) = %q", got)
	}
}
