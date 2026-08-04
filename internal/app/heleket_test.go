package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/heleket"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func TestHeleketOrderIDAndData(t *testing.T) {
	// Покупка.
	oid := hlOrderID(777, 3, "")
	if !strings.HasPrefix(oid, "hl-777-3-") {
		t.Fatalf("order_id покупки: %q", oid)
	}
	for _, r := range oid {
		ok := r == '-' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			t.Fatalf("order_id содержит недопустимый символ %q: %s", r, oid)
		}
	}
	// Nonce обязателен: на повторный order_id Heleket вернул бы старый счёт.
	if hlOrderID(777, 3, "") == oid {
		t.Fatal("order_id не уникален между вызовами")
	}
	// Пополнение — свой префикс.
	if got := hlOrderID(777, 50000, purposeTopUp); !strings.HasPrefix(got, "hlt-777-50000-") {
		t.Fatalf("order_id пополнения: %q", got)
	}

	// additional_data round-trip.
	tg, mo, kp := parseHLData(hlData(777, 3, 0))
	if tg != 777 || mo != 3 || kp != 0 {
		t.Fatalf("покупка: tg=%d mo=%d kp=%d", tg, mo, kp)
	}
	tg, mo, kp = parseHLData(hlData(777, 0, 50000))
	if tg != 777 || mo != 0 || kp != 50000 {
		t.Fatalf("пополнение: tg=%d mo=%d kp=%d", tg, mo, kp)
	}
	// E-mail-аккаунт кабинета: telegram-id отрицательный.
	tg, mo, _ = parseHLData(hlData(-1754300000000000000, 1, 0))
	if tg != -1754300000000000000 || mo != 1 {
		t.Fatalf("отрицательный tg: tg=%d mo=%d", tg, mo)
	}
	if _, _, _ = parseHLData(""); false {
		t.Fatal("unreachable")
	}
}

func TestParseHLOrderIDFallback(t *testing.T) {
	tg, n := parseHLOrderID("hl-777-3-abcd1234")
	if tg != 777 || n != 3 {
		t.Fatalf("покупка: tg=%d n=%d", tg, n)
	}
	tg, n = parseHLOrderID("hlt-777-50000-abcd1234")
	if tg != 777 || n != 50000 {
		t.Fatalf("пополнение: tg=%d n=%d", tg, n)
	}
	// Отрицательный telegram-id даёт пустой элемент после split по «-».
	tg, n = parseHLOrderID("hl--100500-1-abcd1234")
	if tg != -100500 || n != 1 {
		t.Fatalf("отрицательный tg: tg=%d n=%d", tg, n)
	}
	// Чужой order_id не должен разбираться в получателя.
	if tg, _ := parseHLOrderID("order-42"); tg != 0 {
		t.Fatalf("чужой order_id разобран: tg=%d", tg)
	}
}

func TestHeleketConfigDefaults(t *testing.T) {
	var cfg model.HeleketConfig
	if got := cfg.SubtractOrDefault(); got != model.HeleketDefaultSubtract {
		t.Fatalf("subtract по умолчанию: %d", got)
	}
	// Явный ноль — «комиссию платит магазин» — не должен подменяться дефолтом.
	zero := 0
	cfg.Subtract = &zero
	if got := cfg.SubtractOrDefault(); got != 0 {
		t.Fatalf("явный subtract=0 подменён на %d", got)
	}
	over := 500
	cfg.Subtract = &over
	if got := cfg.SubtractOrDefault(); got != 100 {
		t.Fatalf("subtract не зажат в 0..100: %d", got)
	}

	if got := cfg.LifetimeOrDefault(); got != model.HeleketDefaultLifetime {
		t.Fatalf("lifetime по умолчанию: %d", got)
	}
	cfg.Lifetime = 60 // меньше минимума Heleket
	if got := cfg.LifetimeOrDefault(); got != model.HeleketDefaultLifetime {
		t.Fatalf("слишком малый lifetime не заменён дефолтом: %d", got)
	}
	cfg.Lifetime = 99999 // больше максимума
	if got := cfg.LifetimeOrDefault(); got != model.HeleketDefaultLifetime {
		t.Fatalf("слишком большой lifetime не заменён дефолтом: %d", got)
	}
	cfg.Lifetime = 7200
	if got := cfg.LifetimeOrDefault(); got != 7200 {
		t.Fatalf("валидный lifetime изменён: %d", got)
	}
}

// Форматные строки, у которых при добавлении Heleket изменилось число
// аргументов, — прямой регресс на «%!s(int=0)» и «%!d(MISSING)» из прошлых правок.
func TestHeleketScreenPlaceholders(t *testing.T) {
	bad := func(t *testing.T, key, got string) {
		t.Helper()
		if strings.Contains(got, "%!") || strings.Contains(got, "MISSING") || strings.Contains(got, "EXTRA") {
			t.Fatalf("битый шаблон %s: %q", key, got)
		}
	}
	for _, lang := range []string{model.LangRU, model.LangEN} {
		bad(t, "hl.title", i18n.T(lang, "hl.title", "on", "m-uuid", "yes", "RUB", "USDT", 100, 60, "https://t.me"))
		bad(t, "hl.probe_ok", i18n.T(lang, "hl.probe_ok", 12, "USDT, TRX"))
		bad(t, "hl.probe_fail", i18n.T(lang, "hl.probe_fail", "boom"))
		bad(t, "hl.pay_prompt", i18n.T(lang, "hl.pay_prompt", 3, "597 ₽"))
		bad(t, "hl.fail", i18n.T(lang, "hl.fail", "boom"))
		bad(t, "method.hl_btn", i18n.T(lang, "method.hl_btn", "199 ₽"))
		bad(t, "hl.admin_underpaid", i18n.T(lang, "hl.admin_underpaid", "u-1", "199.00 RUB"))
		bad(t, "hl.admin_locked", i18n.T(lang, "hl.admin_locked", "u-1", "199.00 RUB"))
		// Экран «Продажи» получил седьмую метку метода.
		bad(t, "subsetup.title", i18n.T(lang, "subsetup.title",
			"✅", "✅", "✅", "✅", "✅", "✅", "✅", "—", "—", "MONTH", "—", "—"))
		// Экран «Вебхуки» получил пятый адрес.
		bad(t, "wh.urls", i18n.T(lang, "wh.urls", "a", "b", "c", "d", "e"))
	}
}

func TestCallbackRouting_Heleket(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	// Админ включает шлюз кнопкой.
	a.handleCallback(ctx, cb(100, "hl:toggle"))
	if !a.hlConfig().Enabled {
		t.Fatal("кнопка включения Heleket не сработала")
	}
	a.handleCallback(ctx, cb(100, "hl:toggle"))
	if a.hlConfig().Enabled {
		t.Fatal("кнопка выключения Heleket не сработала")
	}

	// Обычный пользователь до админки не дотягивается.
	a.handleCallback(ctx, cb(555, "hl:toggle"))
	if a.hlConfig().Enabled {
		t.Fatal("не-админ смог включить Heleket")
	}

	// Проверка оплаты у ненастроенного шлюза не должна паниковать.
	a.handleCallback(ctx, cb(555, "hlc:some-uuid"))
}

func TestHeleketCallbackURL(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{}
	if got := a.hlCallbackURL(); got != "" {
		t.Fatalf("без публичного адреса callback должен быть пустым: %q", got)
	}
	a.botCfg.Webhook.PublicBaseURL = "https://bot.example/"
	if got := a.hlCallbackURL(); got != "https://bot.example/webhook/heleket" {
		t.Fatalf("callback из PublicBaseURL: %q", got)
	}
	a.botCfg.Webhook.TLS = true
	a.botCfg.Webhook.Domain = "pay.example"
	if got := a.hlCallbackURL(); got != "https://pay.example/webhook/heleket" {
		t.Fatalf("callback из домена с TLS: %q", got)
	}
}

func TestHeleketCurrencyFallsBackToRUB(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{}
	if got := a.hlCurrency(); got != "RUB" {
		t.Fatalf("пустая валюта прайса должна давать RUB: %q", got)
	}
	a.botCfg.Pricing.Currency = "usd"
	if got := a.hlCurrency(); got != "USD" {
		t.Fatalf("валюта прайса: %q", got)
	}
	a.botCfg.Pricing.Currency = "₽"
	if got := a.hlCurrency(); got != "RUB" {
		t.Fatalf("символ вместо кода должен давать RUB: %q", got)
	}
}

// hlStub поднимает заглушку API Heleket и переключает на неё адаптер.
func hlStub(t *testing.T, infoJSON string) *App {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(infoJSON))
	}))
	t.Cleanup(srv.Close)
	old := heleket.BaseURL
	heleket.BaseURL = srv.URL
	t.Cleanup(func() { heleket.BaseURL = old })

	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   &fakeMsg{},
		store: &fakeStore{},
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Pricing: model.Pricing{Currency: "₽", Base: map[int]string{1: "150"}},
		Heleket: model.HeleketConfig{Enabled: true, MerchantID: "m-uuid", APIKey: "pay-key"},
	}
	a.botCfg.NormalizePricing()
	return a
}

// Вебхук с неверной подписью НЕ должен ломать приём денег: решение принимается
// по ответу /v1/payment/info, подпись только пишется в журнал.
func TestHeleketWebhook_TopUpFinalizedAndDeduplicated(t *testing.T) {
	a := hlStub(t, `{"state":0,"result":{"uuid":"u-1","order_id":"hlt-555-50000-abcd","status":"paid","amount":"500.00","currency":"RUB","payer_amount":"5.90","payer_currency":"USDT","is_final":true,"additional_data":"tg=555&kp=50000"}}`)
	fs := a.store.(*fakeStore)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodHeleket, ExtID: "hl:u-1", TelegramID: u,
		Purpose: purposeTopUp, Kopecks: 50000,
	})

	body := []byte(`{"type":"payment","uuid":"u-1","order_id":"hlt-555-50000-abcd","status":"paid","sign":"deadbeefdeadbeefdeadbeefdeadbeef"}`)
	handled, err := a.HandleHeleketWebhook(ctx, body)
	if err != nil || !handled {
		t.Fatalf("вебхук не обработан: handled=%v err=%v", handled, err)
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 50000 {
		t.Fatalf("баланс не зачислен: %+v", got)
	}

	// Повторная доставка того же вебхука не должна зачислять второй раз.
	if _, err := a.HandleHeleketWebhook(ctx, body); err != nil {
		t.Fatalf("повторный вебхук: %v", err)
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 50000 {
		t.Fatalf("дубль вебхука зачислен повторно: %+v", got)
	}
}

// Недоплата: подписку не выдаём, баланс не трогаем, админа зовём.
func TestHeleketWebhook_UnderpaidNotifiesAdmin(t *testing.T) {
	a := hlStub(t, `{"state":0,"result":{"uuid":"u-2","order_id":"hlt-555-50000-abcd","status":"wrong_amount","amount":"500.00","currency":"RUB","is_final":true,"additional_data":"tg=555&kp=50000"}}`)
	fs := a.store.(*fakeStore)
	fm := a.msg.(*fakeMsg)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodHeleket, ExtID: "hl:u-2", TelegramID: u,
		Purpose: purposeTopUp, Kopecks: 50000,
	})

	if _, err := a.HandleHeleketWebhook(ctx, []byte(`{"type":"payment","uuid":"u-2","status":"wrong_amount"}`)); err != nil {
		t.Fatalf("вебхук: %v", err)
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 0 {
		t.Fatalf("при недоплате баланс не должен меняться: %+v", got)
	}
	if !strings.Contains(fm.joined(), "u-2") {
		t.Fatalf("админ не уведомлён о недоплате:\n%s", fm.joined())
	}
}

// Промежуточный статус не должен ни выдавать подписку, ни гасить счёт:
// переход check → paid легален, и снятие pending потеряло бы оплату.
func TestHeleketWebhook_IntermediateKeepsPending(t *testing.T) {
	a := hlStub(t, `{"state":0,"result":{"uuid":"u-3","status":"check","amount":"500.00","currency":"RUB","is_final":false,"additional_data":"tg=555&kp=50000"}}`)
	fs := a.store.(*fakeStore)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodHeleket, ExtID: "hl:u-3", TelegramID: u,
		Purpose: purposeTopUp, Kopecks: 50000,
	})

	if _, err := a.HandleHeleketWebhook(ctx, []byte(`{"type":"payment","uuid":"u-3","status":"check"}`)); err != nil {
		t.Fatalf("вебхук: %v", err)
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 0 {
		t.Fatalf("промежуточный статус зачислил баланс: %+v", got)
	}
	if p, _ := fs.PendingByExtID(ctx, "hl:u-3"); p == nil {
		t.Fatal("счёт снят с ожидания на промежуточном статусе — последующая оплата потеряется")
	}
}

// Регресс: у пополнения могла потеряться pending-запись (реконсилятор снимает
// счета старше суток). Тогда получателя восстанавливаем из additional_data — и
// это ОБЯЗАНО быть зачислением на баланс, а не подпиской на срок по умолчанию.
func TestHeleketTopUpWithoutPendingCreditsBalance(t *testing.T) {
	a := hlStub(t, `{"state":0,"result":{"uuid":"u-4","order_id":"hlt-555-50000-abcd","status":"paid","amount":"500.00","currency":"RUB","is_final":true,"additional_data":"tg=555&kp=50000"}}`)
	fs := a.store.(*fakeStore)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	// pending-записи намеренно НЕТ.

	if _, err := a.HandleHeleketWebhook(ctx, []byte(`{"type":"payment","uuid":"u-4","status":"paid"}`)); err != nil {
		t.Fatalf("вебхук: %v", err)
	}
	got, _ := fs.GetUser(ctx, u)
	if got == nil || got.Balance != 50000 {
		t.Fatalf("пополнение без pending должно зачислиться на баланс: %+v", got)
	}
}
