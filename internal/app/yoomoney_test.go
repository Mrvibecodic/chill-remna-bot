package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/yoomoney"
)

// ymStubAPI — заглушка API кошелька: история по метке отдаёт ops, номер
// кошелька — account, обмен кода — токен.
type ymStubAPI struct {
	ops     []map[string]any
	account string
	tokenOf string // токен за любой код; "" — отказ invalid_grant
	calls   int
	// noFilter — история отдаёт все операции, игнорируя фильтр label (как
	// если бы фильтр на стороне ЮMoney не сработал).
	noFilter bool
}

func ymStub(t *testing.T) (*App, *fakeMsg, *fakeStore, *ymStubAPI) {
	t.Helper()
	st := &ymStubAPI{account: "410011000000001", tokenOf: "tok-ok"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		st.calls++
		switch r.URL.Path {
		case "/api/operation-history":
			if r.Header.Get("Authorization") != "Bearer tok-ok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var out []map[string]any
			for _, o := range st.ops {
				if st.noFilter || o["label"] == r.PostForm.Get("label") {
					out = append(out, o)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"operations": out})
		case "/api/account-info":
			_ = json.NewEncoder(w).Encode(map[string]any{"account": st.account})
		case "/oauth/token":
			if st.tokenOf == "" {
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": st.tokenOf})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	old := yoomoney.APIBaseURL
	yoomoney.APIBaseURL = api.URL
	t.Cleanup(func() { yoomoney.APIBaseURL = old })

	var patched map[string]any
	panel := snapPanel(t, &patched)
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		store: fs,
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Pricing:  model.Pricing{Currency: "₽", Base: map[int]string{1: "150"}},
		Wallet:   model.WalletConfig{TopUp: true, Init: true},
		Webhook:  model.WebhookConfig{PublicBaseURL: "https://bot.example.test"},
		YooMoney: model.YooMoneyConfig{Enabled: true, Receiver: "410011000000001", NotifySecret: "s3cret", Token: "tok-ok", ClientID: "cid"},
	}
	a.botCfg.NormalizePricing()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	return a, fm, fs, st
}

// ymForm — подписанное уведомление ЮMoney.
func ymForm(secret, opID, label, paid, credited string) url.Values {
	f := url.Values{
		"notification_type": {"card-incoming"}, "operation_id": {opID}, "amount": {credited},
		"withdraw_amount": {paid}, "currency": {"643"}, "datetime": {"2026-10-24T10:00:00Z"},
		"sender": {""}, "codepro": {"false"}, "label": {label}, "unaccepted": {"false"},
	}
	f.Set("sign", yoomoney.Sign(f, secret))
	return f
}

// ymUserPays — записанные платежи пользователя.
func ymUserPays(fs *fakeStore, u int64) []model.Payment {
	all, _ := fs.PaidPayments(context.Background())
	var out []model.Payment
	for _, p := range all {
		if p.TelegramID == u {
			out = append(out, p)
		}
	}
	return out
}

func ymOp(id, label string, amount float64) map[string]any {
	return map[string]any{"operation_id": id, "status": "success", "direction": "in", "amount": amount, "label": label, "type": "deposition"}
}

func TestYooMoney_PurchaseWebhookDedupAndDuplicate(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)

	payURL, label, err := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	if err != nil || !strings.HasPrefix(label, "ym:555:") || payURL != "https://bot.example.test/webhook/yoomoney/pay/"+url.PathEscape(label) {
		t.Fatalf("счёт: url=%q label=%q err=%v", payURL, label, err)
	}
	p, _ := fs.PendingByExtID(ctx, label)
	if p == nil || p.Kopecks != 15000 || p.Months != 1 || p.Snapshot == nil {
		t.Fatalf("строка счёта: %+v", p)
	}
	action, fields, ok := a.YooMoneyPayForm(ctx, label)
	if !ok || action != yoomoney.FormURL {
		t.Fatalf("форма оплаты: ok=%v action=%q", ok, action)
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f[0]] = f[1]
	}
	if got["sum"] != "150.00" || got["label"] != label || got["receiver"] != "410011000000001" || got["paymentType"] != "AC" {
		t.Fatalf("поля формы: %v", got)
	}
	if _, _, ok := a.YooMoneyPayForm(ctx, "ym:1:nope"); ok {
		t.Fatal("форма по чужой метке")
	}

	// Уведомление пришло раньше истории — ждём повтора.
	form := ymForm("s3cret", "op-1", label, "150.00", "145.50")
	if handled, err := a.HandleYooMoneyWebhook(ctx, form); err == nil || handled {
		t.Fatalf("операции ещё нет в истории, а вебхук принят: handled=%v err=%v", handled, err)
	}
	st.ops = []map[string]any{ymOp("op-1", label, 145.50)}
	if handled, err := a.HandleYooMoneyWebhook(ctx, form); err != nil || !handled {
		t.Fatalf("вебхук: handled=%v err=%v", handled, err)
	}
	if done, _ := fs.PaymentByExtID(ctx, label); !done {
		t.Fatalf("платёж не записан; лог:\n%s", fm.joined())
	}
	if !strings.Contains(fm.joined(), "активна") && !strings.Contains(strings.ToLower(fm.joined()), "подписка") {
		t.Fatalf("покупатель не получил подтверждение:\n%s", fm.joined())
	}
	pays := ymUserPays(fs, u)
	if len(pays) != 1 || pays[0].Amount != "150.00 RUB" || pays[0].Method != model.PayMethodYooMoney {
		t.Fatalf("запись платежа: %+v", pays)
	}
	if _, _, ok := a.YooMoneyPayForm(ctx, label); ok {
		t.Fatal("форма оплаты по уже оплаченному счёту")
	}

	// Повторная доставка того же уведомления — тишина.
	if _, err := a.HandleYooMoneyWebhook(ctx, form); err != nil {
		t.Fatal(err)
	}
	if pays := ymUserPays(fs, u); len(pays) != 1 {
		t.Fatalf("дубль доставки записал второй платёж: %+v", pays)
	}
	if strings.Contains(fm.joined(), "повторная оплата") {
		t.Fatal("повтор доставки принят за повторную оплату")
	}

	// Человек оплатил ту же ссылку второй раз: второй операции выдачи нет,
	// админу — уведомление.
	st.ops = append([]map[string]any{ymOp("op-2", label, 145.50)}, st.ops...)
	if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-2", label, "150.00", "145.50")); err != nil {
		t.Fatal(err)
	}
	if pays := ymUserPays(fs, u); len(pays) != 1 {
		t.Fatalf("повторная оплата выдана второй раз: %+v", pays)
	}
	if !strings.Contains(fm.joined(), "повторная оплата") {
		t.Fatalf("админ не узнал о повторной оплате:\n%s", fm.joined())
	}
}

func TestYooMoney_WebhookAuthAndTest(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = []map[string]any{ymOp("op-1", label, 145.50)}

	bad := ymForm("other", "op-1", label, "150.00", "145.50")
	if handled, err := a.HandleYooMoneyWebhook(ctx, bad); err == nil || handled || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("чужая подпись принята: handled=%v err=%v", handled, err)
	}
	good := ymForm("s3cret", "op-1", label, "150.00", "145.50")
	tampered := url.Values{}
	for k, v := range good {
		tampered[k] = v
	}
	tampered.Set("withdraw_amount", "1500.00")
	if _, err := a.HandleYooMoneyWebhook(ctx, tampered); err == nil {
		t.Fatal("подменённая сумма прошла подпись")
	}
	if done, _ := fs.PaymentByExtID(ctx, label); done {
		t.Fatal("выдано по неподписанному уведомлению")
	}
	// Без секрета — 401, не 200: иначе ЮMoney решит, что доставлено.
	a.botCfg.YooMoney.NotifySecret = ""
	if _, err := a.HandleYooMoneyWebhook(ctx, good); err == nil {
		t.Fatal("без секрета уведомление принято")
	}
	a.botCfg.YooMoney.NotifySecret = "s3cret"

	// Тестовое уведомление из настроек кошелька: подпись проверяется, выдачи нет.
	tf := url.Values{"test_notification": {"true"}, "amount": {"1.00"}, "label": {""}, "operation_id": {"test"}, "codepro": {"false"}, "unaccepted": {"false"}}
	tf.Set("sign", yoomoney.Sign(tf, "s3cret"))
	if handled, err := a.HandleYooMoneyWebhook(ctx, tf); err != nil || !handled {
		t.Fatalf("тестовое уведомление: handled=%v err=%v", handled, err)
	}
	// Перевод без нашей метки — не наш платёж, но 200.
	if handled, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-9", "", "10.00", "9.70")); err != nil || !handled {
		t.Fatalf("перевод без метки: handled=%v err=%v", handled, err)
	}
	if pays := ymUserPays(fs, u); len(pays) != 0 {
		t.Fatalf("что-то выдано: %+v", pays)
	}
	_ = fm
}

func TestYooMoney_UnderpaidNotIssued(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = []map[string]any{ymOp("op-1", label, 97.00)}
	if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-1", label, "100.00", "97.00")); err != nil {
		t.Fatal(err)
	}
	if done, _ := fs.PaymentByExtID(ctx, label); done {
		t.Fatal("недоплата выдана")
	}
	if !strings.Contains(fm.joined(), "НЕ выданы") || !strings.Contains(fm.joined(), "100.00") {
		t.Fatalf("админ не узнал о недоплате:\n%s", fm.joined())
	}
	// Сверка по API видит только зачисленное: 145.50 при счёте 150 — это
	// комиссия, выдаём; 140 — недоплата.
	if !ymAmountOK(15000, 0, 14550) || ymAmountOK(15000, 0, 14000) || !ymAmountOK(15000, 15000, 14550) || ymAmountOK(15000, 14999, 15000) {
		t.Fatal("допуск на комиссию считается неверно")
	}
	// Допуск всегда по карте (3 %): чем платил покупатель, бот не знает.
	if !ymAmountOK(15000, 0, 14549) || ymAmountOK(15000, 0, 14548) {
		t.Fatal("допуск не по комиссии карты")
	}
	// «Заплачено» из уведомления не перекрывает историю кошелька.
	if ymAmountOK(15000, 15000, 97) {
		t.Fatal("выдано по сумме из уведомления при копейках в истории")
	}
	// Недоплата через кнопку «Проверить»: покупатель получает ответ, а не тишину.
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	if !strings.Contains(fm.last(), "97.00 ₽") || !strings.Contains(fm.last(), "150.00 ₽") || !strings.Contains(fm.last(), "не выдана") {
		t.Fatalf("покупатель не увидел недоплату: %s", fm.last())
	}
}

func TestYooMoney_CheckButtonAndReconcile(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)

	// Кнопка «Проверить» через диспетчер колбэков: ещё не оплачено.
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	if !strings.Contains(fm.last(), "не подтверждена") {
		t.Fatalf("ожидали «не подтверждена»: %s", fm.last())
	}
	// Уведомление потерялось, человек жмёт «Проверить» — выдаём по API.
	st.ops = []map[string]any{ymOp("op-1", label, 145.50)}
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	if done, _ := fs.PaymentByExtID(ctx, label); !done {
		t.Fatalf("ручная проверка не выдала:\n%s", fm.joined())
	}
	// В запись платежа — цена сделки, а не зачисленное (иначе выручка зависела
	// бы от пути доставки оплаты).
	if pays := ymUserPays(fs, u); len(pays) != 1 || pays[0].Amount != "150.00 RUB" {
		t.Fatalf("запись платежа по API: %+v", pays)
	}

	// Сверка: второй счёт, оплачен с комиссией — выдаётся и закрывается.
	_, label2, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = append(st.ops, ymOp("op-2", label2, 145.50))
	p2, _ := fs.PendingByExtID(ctx, label2)
	a.reconcileInvoice(ctx, fs, p2)
	if done, _ := fs.PaymentByExtID(ctx, label2); !done {
		t.Fatal("сверка не выдала оплаченный счёт")
	}
	if p, _ := fs.PendingByExtID(ctx, label2); p == nil || !p.Resolved {
		t.Fatalf("счёт после сверки не закрыт: %+v", p)
	}
	// Сверка: недоплата — не выдано, админ, счёт закрыт.
	_, label3, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = append(st.ops, ymOp("op-3", label3, 120.00))
	p3, _ := fs.PendingByExtID(ctx, label3)
	a.reconcileInvoice(ctx, fs, p3)
	if done, _ := fs.PaymentByExtID(ctx, label3); done {
		t.Fatal("сверка выдала недоплату")
	}
	if p, _ := fs.PendingByExtID(ctx, label3); p == nil || !p.Resolved {
		t.Fatal("недоплаченный счёт не закрыт — сверка будет дёргать админа каждые две минуты")
	}
	if !strings.Contains(fm.joined(), "120.00") {
		t.Fatalf("админ не узнал о недоплате из сверки:\n%s", fm.joined())
	}
	// Сверка без оплаты счёт не трогает.
	_, label4, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	p4, _ := fs.PendingByExtID(ctx, label4)
	a.reconcileInvoice(ctx, fs, p4)
	if p, _ := fs.PendingByExtID(ctx, label4); p == nil || p.Resolved {
		t.Fatal("неоплаченный счёт закрыт сверкой")
	}
	if !methodSelfHeals(model.PayMethodYooMoney) {
		t.Fatal("ЮMoney должен считаться способом со сверкой")
	}
}

func TestYooMoney_TopUp(t *testing.T) {
	a, _, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 556
	_ = fs.UpsertUser(ctx, u)
	if !a.topUpMethodOn("ym") {
		t.Fatal("ЮMoney нет среди способов пополнения")
	}
	payURL, label, err := a.topUpCreate(ctx, u, 12345, "ym", false)
	if err != nil || !strings.HasPrefix(payURL, "https://bot.example.test/webhook/yoomoney/pay/") || !strings.HasPrefix(label, "ym:") {
		t.Fatalf("счёт на пополнение: %q %q %v", payURL, label, err)
	}
	p, _ := fs.PendingByExtID(ctx, label)
	if p == nil || p.Purpose != purposeTopUp || p.Kopecks != 12345 || p.Months != 0 {
		t.Fatalf("строка счёта: %+v", p)
	}
	if _, fields, ok := a.YooMoneyPayForm(ctx, label); !ok || fields[3] != [2]string{"sum", "123.45"} {
		t.Fatalf("форма пополнения: ok=%v %v", ok, fields)
	}
	st.ops = []map[string]any{ymOp("op-t", label, 119.75)}
	form := ymForm("s3cret", "op-t", label, "123.45", "119.75")
	for i := 0; i < 2; i++ {
		if handled, err := a.HandleYooMoneyWebhook(ctx, form); err != nil || !handled {
			t.Fatalf("вебхук: handled=%v err=%v", handled, err)
		}
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 12345 {
		t.Fatalf("баланс после двух доставок: %+v", got)
	}
	if p, _ := fs.PendingByExtID(ctx, label); p == nil || !p.Resolved {
		t.Fatal("счёт пополнения не закрыт")
	}
	// Без публичного адреса способ пополнения исчезает, счёт не выставить.
	a.botCfg.Webhook.PublicBaseURL = ""
	if a.topUpMethodOn("ym") {
		t.Fatal("ЮMoney предлагается без публичного адреса")
	}
	if _, _, err := a.topUpCreate(ctx, u, 100, "ym", false); err == nil {
		t.Fatal("счёт без публичного адреса выставлен")
	}
}

func TestYooMoney_OAuthFlow(t *testing.T) {
	a, fm, _, st := ymStub(t)
	ctx := context.Background()
	a.botCfg.YooMoney.Token = ""
	a.botCfg.YooMoney.Enabled = false

	// Редирект, которого никто не ждал.
	if msg := a.HandleYooMoneyOAuth(ctx, "", "c0de", ""); !strings.Contains(msg, "не ждал") {
		t.Fatalf("незапрошенный редирект: %q", msg)
	}
	if _, _, ok := a.YooMoneyAuthForm("anything"); ok {
		t.Fatal("форма авторизации без взвода")
	}
	// Админ жмёт «Авторизовать».
	a.handleCallback(ctx, cb(100, "ym:auth"))
	state := a.ymAuthState
	if state == "" {
		t.Fatalf("ожидание не взведено; лог:\n%s", fm.joined())
	}
	if _, _, ok := a.YooMoneyAuthForm("wrong"); ok {
		t.Fatal("форма по чужому состоянию")
	}
	action, fields, ok := a.YooMoneyAuthForm(state)
	if !ok || action != yoomoney.AuthorizeURL {
		t.Fatalf("форма авторизации: ok=%v action=%q", ok, action)
	}
	got := map[string]string{}
	for _, f := range fields {
		got[f[0]] = f[1]
	}
	if got["client_id"] != "cid" || got["redirect_uri"] != "https://bot.example.test/webhook/yoomoney/oauth" || got["response_type"] != "code" || got["scope"] != yoomoney.Scope {
		t.Fatalf("поля авторизации: %v", got)
	}

	// Чужой GET без cookie состояния ничего не разоружает и админа не дёргает.
	n := len(fm.joined())
	if msg := a.HandleYooMoneyOAuth(ctx, "", "", "x"); !strings.Contains(msg, "не ждал") || a.ymAuthState != state || len(fm.joined()) != n {
		t.Fatalf("посторонний запрос сорвал ожидание: %q armed=%v", msg, a.ymAuthState == state)
	}
	if msg := a.HandleYooMoneyOAuth(ctx, "wrong", "c0de", ""); !strings.Contains(msg, "не ждал") || a.ymAuthState != state {
		t.Fatalf("чужое состояние принято: %q", msg)
	}
	// Токен выдан чужому кошельку — не сохраняем.
	st.account = "410019999999999"
	if msg := a.HandleYooMoneyOAuth(ctx, state, "c0de", ""); !strings.Contains(msg, "не сохранён") || a.ymConfig().Token != "" {
		t.Fatalf("чужой кошелёк принят: %q token=%q", msg, a.ymConfig().Token)
	}
	// Ожидание одноразовое: повтор без нового взвода отвергается.
	if msg := a.HandleYooMoneyOAuth(ctx, state, "c0de", ""); !strings.Contains(msg, "не ждал") {
		t.Fatalf("повторный редирект принят: %q", msg)
	}
	// Удачный заход.
	st.account = "410011000000001"
	a.handleCallback(ctx, cb(100, "ym:auth"))
	if msg := a.HandleYooMoneyOAuth(ctx, a.ymAuthState, "c0de", ""); !strings.Contains(msg, "сохранён") || a.ymConfig().Token != "tok-ok" {
		t.Fatalf("токен не сохранён: %q token=%q", msg, a.ymConfig().Token)
	}
	// Отказ пользователя на стороне ЮMoney.
	a.handleCallback(ctx, cb(100, "ym:auth"))
	if msg := a.HandleYooMoneyOAuth(ctx, a.ymAuthState, "", "access_denied"); !strings.Contains(msg, "access_denied") {
		t.Fatalf("отказ: %q", msg)
	}
}

func TestYooMoney_AdminScreenAndToggle(t *testing.T) {
	a, fm, _, st := ymStub(t)
	ctx := context.Background()
	a.botCfg.YooMoney = model.YooMoneyConfig{}

	// Без секретов включить нельзя.
	a.handleCallback(ctx, cb(100, "ym:toggle"))
	if a.ymConfig().Enabled {
		t.Fatal("ЮMoney включилась без настроек")
	}
	if !strings.Contains(fm.last(), "все три настройки") {
		t.Fatalf("нет объяснения, чего не хватает: %s", fm.last())
	}
	// Поля вводятся через adminInput.
	set := func(action, value string) {
		a.handleCallback(ctx, cb(100, "ym:"+action))
		a.handleMessage(ctx, msgText(100, value))
	}
	set("receiver", "4100 1100 0000 0001")
	if a.ymConfig().Receiver != "" {
		t.Fatal("номер с пробелами принят")
	}
	set("receiver", "410011000000001")
	set("secret", "s3cret")
	set("token", "tok-ok")
	set("client", "cid")
	set("return", "https://t.me/mybot")
	// Кошелёк токена не совпал с получателем — включить нельзя, объяснение есть.
	st.account = "410019999999999"
	a.handleCallback(ctx, cb(100, "ym:toggle"))
	if a.ymConfig().Enabled || !strings.Contains(fm.last(), "410019999999999") {
		t.Fatalf("включилось при чужом кошельке токена: on=%v %s", a.ymConfig().Enabled, fm.last())
	}
	st.account = "410011000000001"
	cfg := a.ymConfig()
	if cfg.Receiver != "410011000000001" || cfg.NotifySecret != "s3cret" || cfg.Token != "tok-ok" || cfg.ClientID != "cid" || cfg.ReturnURL != "https://t.me/mybot" {
		t.Fatalf("поля не сохранились: %+v", cfg)
	}
	a.handleCallback(ctx, cb(100, "ym:toggle"))
	if !a.ymConfig().Enabled {
		t.Fatal("ЮMoney не включилась")
	}
	a.handleCallback(ctx, cb(100, "ym:paytype"))
	if a.ymConfig().PayType != yoomoney.PayTypeWallet {
		t.Fatalf("тип оплаты: %q", a.ymConfig().PayType)
	}
	// Экран показывает адреса уведомлений и редиректа.
	a.handleCallback(ctx, cb(100, "menu:yoomoney"))
	if !strings.Contains(fm.last(), "https://bot.example.test/webhook/yoomoney") || !strings.Contains(fm.last(), "https://bot.example.test/webhook/yoomoney/oauth") {
		t.Fatalf("экран без адресов:\n%s", fm.last())
	}
	// Проверка токена ходит в API и сверяет кошелёк.
	a.handleCallback(ctx, cb(100, "ym:verify"))
	if !strings.Contains(fm.last(), "совпадают") {
		t.Fatalf("проверка токена: %s", fm.last())
	}
	// Не-админ до админки не дотягивается.
	a.handleCallback(ctx, cb(555, "ym:toggle"))
	if !a.ymConfig().Enabled {
		t.Fatal("не-админ выключил ЮMoney")
	}
	// Кнопка способа на экране оплаты и список мини-аппа.
	if !a.methodEnabled(model.PayMethodYooMoney) {
		t.Fatal("methodEnabled не видит ЮMoney")
	}
}

func TestYooMoney_BuyScreenButtonAndMiniURL(t *testing.T) {
	a, fm, fs, _ := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)
	a.handleCallback(ctx, cb(u, "buy:1"))
	has := func() bool {
		for _, d := range fm.allCallbackData() {
			if d == "method:ym" {
				return true
			}
		}
		return false
	}
	if !has() {
		t.Fatalf("нет кнопки ЮMoney на экране оплаты; кнопки: %v", fm.allCallbackData())
	}
	a.handleCallback(ctx, cb(u, "method:ym"))
	if !strings.Contains(fm.last(), "150 ₽") {
		t.Fatalf("экран оплаты ЮMoney: %s", fm.last())
	}
	// Без публичного адреса кнопки нет и покупка из мини-аппа отказывает.
	a.botCfg.Webhook.PublicBaseURL = ""
	fm2 := &fakeMsg{}
	a.msg = fm2
	a.handleCallback(ctx, cb(u, "buy:1"))
	for _, d := range fm2.allCallbackData() {
		if d == "method:ym" {
			t.Fatal("кнопка ЮMoney без публичного адреса")
		}
	}
	s := baseSale(1)
	if _, _, err := a.miniPayURLCore(ctx, u, s, model.PayMethodYooMoney, false); err == nil {
		t.Fatal("мини-апп выставил счёт без публичного адреса")
	}
	a.botCfg.Webhook.PublicBaseURL = "https://bot.example.test"
	urlStr, invoice, err := a.miniPayURLCore(ctx, u, s, model.PayMethodYooMoney, true)
	if err != nil || invoice || !strings.HasPrefix(urlStr, "https://bot.example.test/webhook/yoomoney/pay/ym:") {
		t.Fatalf("мини-апп: %q %v %v", urlStr, invoice, err)
	}
}

// Уведомления потерялись, в истории две оплаты: зачитывается самая ранняя,
// покрывающая счёт; о лишней узнаёт админ — и с кнопки, и из сверки.
func TestYooMoney_TwoOpsWithoutWebhook(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	// История новыми вперёд: op-2 (полная) новее op-1 (недоплата).
	st.ops = []map[string]any{ymOp("op-3", label, 145.50), ymOp("op-2", label, 145.50), ymOp("op-1", label, 120.00)}
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	if done, _ := fs.PaymentByExtID(ctx, label); !done {
		t.Fatalf("не выдано по первой покрывающей операции:\n%s", fm.joined())
	}
	j := fm.joined()
	if !strings.Contains(j, "op-3") || !strings.Contains(j, "op-1") || strings.Count(j, "лишняя") > 0 || strings.Count(j, "повторная оплата") != 2 {
		t.Fatalf("админ должен узнать о двух лишних операциях (op-1, op-3):\n%s", j)
	}
	// Ретрай уведомления по зачтённой op-2 — не дубль; по op-3 — дубль, но
	// уведомление админу не повторяется (дедуп по операции).
	n := strings.Count(fm.joined(), "повторная оплата")
	for _, id := range []string{"op-2", "op-3"} {
		if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", id, label, "150.00", "145.50")); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(fm.joined(), "повторная оплата") != n {
		t.Fatalf("повторные уведомления продублировали тревогу:\n%s", fm.joined())
	}

	// Сверка: недоплата op-1 раньше полной op-2 — выдаём по op-2.
	_, label2, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.ops = append(st.ops, ymOp("op-b", label2, 145.50), ymOp("op-a", label2, 120.00))
	p2, _ := fs.PendingByExtID(ctx, label2)
	a.reconcileInvoice(ctx, fs, p2)
	if done, _ := fs.PaymentByExtID(ctx, label2); !done {
		t.Fatal("сверка закрыла счёт как недоплату, хотя есть полная оплата")
	}
}

// Аккаунт кабинета без Telegram (отрицательный id): метка строится,
// разбирается и проходит путь оплаты.
func TestYooMoney_NegativeCabinetID(t *testing.T) {
	a, _, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = -7
	_ = fs.UpsertUser(ctx, u)
	payURL, label, err := a.ymCreateTopUp(ctx, u, 5000)
	if err != nil || !strings.HasPrefix(label, "ym:-7:") || ymLabelHint(label) != -7 || !strings.Contains(payURL, url.PathEscape(label)) {
		t.Fatalf("метка для отрицательного id: %q %q %v", payURL, label, err)
	}
	if _, fields, ok := a.YooMoneyPayForm(ctx, label); !ok || fields[4] != [2]string{"label", label} {
		t.Fatalf("форма: ok=%v %v", ok, fields)
	}
	st.ops = []map[string]any{ymOp("op-n", label, 48.50)}
	if _, err := a.HandleYooMoneyWebhook(ctx, ymForm("s3cret", "op-n", label, "50.00", "48.50")); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 5000 {
		t.Fatalf("баланс кабинетного аккаунта: %+v", got)
	}
}

// Фильтр по метке на стороне ЮMoney не сработал — чужой перевод на кошелёк
// подписку не выдаёт ни кнопкой, ни сверкой.
func TestYooMoney_ForeignLabelIgnored(t *testing.T) {
	a, fm, fs, st := ymStub(t)
	ctx := context.Background()
	const u int64 = 555 // панель-заглушка знает только tg_555
	_ = fs.UpsertUser(ctx, u)
	_, label, _ := a.ymCreateInvoiceSnap(ctx, u, 1, 15000, nil)
	st.noFilter = true
	st.ops = []map[string]any{ymOp("op-x", "ym:555:other", 145.50), ymOp("op-y", "", 150.00)}
	a.handleCallback(ctx, cb(u, "ymc:"+label))
	if done, _ := fs.PaymentByExtID(ctx, label); done || !strings.Contains(fm.last(), "не подтверждена") {
		t.Fatalf("выдано по чужой операции: %s", fm.last())
	}
	p, _ := fs.PendingByExtID(ctx, label)
	a.reconcileInvoice(ctx, fs, p)
	if done, _ := fs.PaymentByExtID(ctx, label); done {
		t.Fatal("сверка выдала по чужой операции")
	}
}
