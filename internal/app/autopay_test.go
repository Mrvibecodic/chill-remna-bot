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

// Успешный платёж сохраняет способ оплаты, но НЕ включает списания: сперва
// пользователю приходит предложение, и только его «да» включает автопродление.
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

	a.saveAutoPayFromPayment(ctx, 42, 3, pay, nil)

	ap, _ := fs.GetAutoPay(ctx, 42)
	if ap == nil || ap.MethodID != "pm_1" || ap.Months != 3 {
		t.Fatalf("способ оплаты не сохранён: %+v", ap)
	}
	if ap.Enabled || a.autoPayOn(ctx, 42) {
		t.Fatal("до согласия пользователя автопродление должно быть выключено")
	}

	// Пользователь соглашается — списания включаются.
	a.onAutoPayUser(ctx, 42, "on")
	if !a.autoPayOn(ctx, 42) {
		t.Fatal("после согласия автопродление должно быть включено")
	}

	// Повторная оплата уже подключённого пользователя согласие не сбрасывает.
	pay.ID = "pay_1b"
	a.saveAutoPayFromPayment(ctx, 42, 3, pay, nil)
	if !a.autoPayOn(ctx, 42) {
		t.Fatal("повторная оплата не должна выключать автопродление")
	}
}

// Отказ от предложения оставляет автопродление выключенным.
func TestAutoPay_DeclineOffer(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()

	pay := &yookassa.Payment{ID: "pay_d"}
	pay.Metadata = map[string]string{"autopay": "1"}
	pay.PaymentMethod.ID = "pm_d"
	pay.PaymentMethod.Saved = true
	a.saveAutoPayFromPayment(ctx, 49, 1, pay, nil)

	a.onAutoPayUser(ctx, 49, "no")
	if a.autoPayOn(ctx, 49) {
		t.Fatal("после отказа списаний быть не должно")
	}
	// Карта сохранена — можно включить позже одной кнопкой.
	if ap, _ := fs.GetAutoPay(ctx, 49); ap == nil || ap.MethodID != "pm_d" {
		t.Fatalf("сохранённый способ оплаты должен остаться: %+v", ap)
	}
	if !a.MiniAutoPay(ctx, 49).CanEnable {
		t.Fatal("CanEnable должен быть true — карта сохранена")
	}
}

// Обычный платёж (без выбора автопродления) ничего не подключает.
func TestAutoPay_NotSavedWithoutFlag(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()

	pay := &yookassa.Payment{ID: "pay_2"}
	pay.PaymentMethod.ID = "pm_2"
	pay.PaymentMethod.Saved = true // ЮKassa сохранила, но пользователь не просил
	a.saveAutoPayFromPayment(ctx, 43, 1, pay, nil)

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

	if _, due := a.autoPayDue(ctx, ap, now); due {
		t.Fatal("за 10 дней до конца списывать рано")
	}
	_ = fs.SetSubExpiry(ctx, 46, now.Add(12*time.Hour).Format(time.RFC3339), "paid")
	if _, due := a.autoPayDue(ctx, ap, now); !due {
		t.Fatal("за полдня до конца пора списывать")
	}

	ap.NextTryAt = now.Add(6 * time.Hour).Format(time.RFC3339)
	if _, due := a.autoPayDue(ctx, ap, now); due {
		t.Fatal("пауза после неудачной попытки должна соблюдаться")
	}
	ap.NextTryAt = ""

	// Страховка от повторного списания: оплата меньше суток назад.
	ap.LastPayAt = now.Add(-2 * time.Hour).Format(time.RFC3339)
	if _, due := a.autoPayDue(ctx, ap, now); due {
		t.Fatal("сразу после оплаты списывать повторно нельзя")
	}
	ap.LastPayAt = ""

	ap.Enabled = false
	if _, due := a.autoPayDue(ctx, ap, now); due {
		t.Fatal("выключенное автопродление списывать нельзя")
	}
	ap.Enabled = true

	_ = fs.SetBlocked(ctx, 46, true)
	if _, due := a.autoPayDue(ctx, ap, now); due {
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
	_ = fs.UpsertUser(ctx, 48)
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

// Проблема магазина (нет цены / не настроена касса) не должна выглядеть для
// пользователя как «не хватило средств» и не должна копить ему неудачи.
func TestAutoPay_ShopIssueDoesNotBlameUser(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	a.botCfg.Pricing = model.Pricing{} // цен нет
	a.botCfg.NormalizePricing()
	_ = fs.UpsertUser(ctx, 60)
	ap := &model.AutoPay{TelegramID: 60, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: true}
	_ = fs.SetAutoPay(ctx, ap)

	reason := a.chargeAutoPay(ctx, ap, now, now.Add(time.Hour))
	if reason == "" {
		t.Fatal("проблема магазина должна возвращаться наверх для одного уведомления админу")
	}
	cur, _ := fs.GetAutoPay(ctx, 60)
	if cur.Fails != 0 {
		t.Fatalf("неудачи пользователя копиться не должны: fails=%d", cur.Fails)
	}
	if !cur.Enabled {
		t.Fatal("автопродление не должно выключаться из-за настроек магазина")
	}
	if cur.NextTryAt == "" {
		t.Fatal("следующая попытка должна быть отложена")
	}
}

// За один и тот же период деньги не списываются дважды, даже если продление в
// панели не удалось и срок подписки не сдвинулся.
func TestAutoPay_NoDoubleChargeForSamePeriod(t *testing.T) {
	a, fs := autoPayApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	exp := now.Add(12 * time.Hour)

	_ = fs.UpsertUser(ctx, 70)
	_ = fs.SetSubExpiry(ctx, 70, exp.Format(time.RFC3339), "paid")
	ap := &model.AutoPay{TelegramID: 70, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: true}
	_ = fs.SetAutoPay(ctx, ap)

	if _, due := a.autoPayDue(ctx, ap, now); !due {
		t.Fatal("первое списание за период должно быть разрешено")
	}
	// Списали, но продление не прошло: срок подписки прежний, а период оплачен.
	_ = fs.MarkAutoPayCharged(ctx, 70, now.Format(time.RFC3339), autoPayPeriod(exp), "", "панель недоступна")
	cur, _ := fs.GetAutoPay(ctx, 70)
	if _, due := a.autoPayDue(ctx, cur, now.Add(48*time.Hour)); due {
		t.Fatal("повторное списание за оплаченный период недопустимо")
	}
	// Подписка всё-таки продлилась — новый период снова можно оплачивать.
	next := now.Add(30 * 24 * time.Hour)
	_ = fs.SetSubExpiry(ctx, 70, next.Format(time.RFC3339), "paid")
	if _, due := a.autoPayDue(ctx, cur, next.Add(-2*time.Hour)); !due {
		t.Fatal("за новый период списание должно быть разрешено")
	}
}

func TestCurrencyHelpers(t *testing.T) {
	if curSymbol("RUB") != curRUB || curSymbol("") != curRUB {
		t.Fatal("рубли должны печататься символом")
	}
	if curSymbol("kzt") != "KZT" {
		t.Fatalf("прочие валюты — кодом, получено %q", curSymbol("kzt"))
	}
	// «₽» — три байта, но не код валюты: такой в ЮKassa слать нельзя.
	if currencyCode("₽") || currencyCode("RU") || currencyCode("RU1") {
		t.Fatal("некорректный код валюты не должен проходить проверку")
	}
	if !currencyCode("rub") {
		t.Fatal("трёхбуквенный код должен проходить")
	}
}
