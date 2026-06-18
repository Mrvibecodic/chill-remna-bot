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
	"remnabot/internal/cryptobot"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func fullPanel(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(p, "/by-telegram-id/"):
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
		case strings.HasSuffix(p, "/system/stats"):
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":5}}}`))
		case strings.HasSuffix(p, "/internal-squads"):
			_, _ = w.Write([]byte(`{"response":{"internalSquads":[{"uuid":"s1","name":"Squad-1"}]}}`))
		case strings.HasSuffix(p, "/external-squads"):
			_, _ = w.Write([]byte(`{"response":{"externalSquads":[{"uuid":"e1","name":"Ext-1"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fullyConfigured() *model.BotConfig {
	c := &model.BotConfig{
		Installed: true, Language: "ru",
		P2P:       model.P2PConfig{Enabled: true, Cards: []string{"CARD-1"}, Prices: map[int]string{1: "100"}},
		Stars:     model.StarsConfig{Enabled: true, Prices: map[int]int{1: 100}},
		YooKassa:  model.YooKassaConfig{Enabled: true, ShopID: "s", SecretKey: "k", Prices: map[int]string{1: "150.00"}},
		CryptoBot: model.CryptoBotConfig{Enabled: true, Token: "t", Asset: "USDT"},
		Trial:     model.TrialConfig{Enabled: true, Days: 3},
		Pricing:   model.Pricing{Currency: "RUB", Base: map[int]string{1: "150"}},
	}
	c.NormalizePricing()
	return c
}

func TestAdminButtonWalk(t *testing.T) {
	srv := fullPanel(t)
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = fullyConfigured()
	a.botCfg.Contact = model.ContactConfig{GroupURL: "https://t.me/g", SupportURL: "https://t.me/s", TermsText: "terms"}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)

	click := func(data string) {
		before := len(fm.texts)
		a.handleCallback(ctx, cb(100, data))
		if len(fm.texts) <= before {
			t.Errorf("админ-кнопка %q ничего не отрисовала (мёртвый/незарегистрированный callback?)", data)
		}
	}

	buttons := []string{

		"menu:iface", "menu:pay", "menu:manage", "menu:buy", "menu:home",

		"menu:welcome", "wel:img", "wel:txt", "wel:cancel",
		"menu:emoji", "emo:done",
		"menu:welcome_sections",
		"menu:contacts", "ctc:group", "ctc:cancel",

		"menu:pay", "prc:quick",
		"menu:pricing", "prc:base", "prc:price:1", "prc:cur",
		"prc:traffic", "prc:trafmo:1", "prc:devices", "prc:devmo:1",
		"prc:strategy", "prc:setstrat:MONTH", "prc:setstrat:MONTH_ROLLING", "prc:qmo:1",

		"menu:trial", "trial:toggle", "trial:days", "trial:gb",
		"trial:hwid", "trial:hwidset:1", "trial:hwidset:custom", "trial:squads", "trial:quick",

		"menu:squads", "sqd:refresh", "sqd:int:s1", "sqd:ext:e1",

		"menu:addsub", "addsub:toggle", "addsub:gb", "addsub:squads", "addsub:refresh", "addsub:int:s1",

		"menu:p2p", "adm:toggle", "adm:rotate", "adm:cards", "adm:prices", "adm:price:1", "sq:pick",
		"menu:stars", "star:toggle", "star:prices", "star:price:1",
		"menu:yookassa", "yk:toggle", "yk:shop", "yk:secret", "yk:return", "yk:prices", "yk:price:1",
		"menu:cryptobot", "cb:toggle", "cb:token", "cb:asset",

		"menu:manage", "menu:users", "usr:view:555", "usr:block:555", "usr:unblock:555",
		"usr:del:555", "usr:list",
		"menu:payments",
		"menu:status",
		"menu:subdomain", "subd:edit", "subd:cancel",
		"menu:apilog", "alog:refresh",
		"menu:webhooks", "wh:guide", "wh:public", "wh:domain", "wh:apply", "wh:base", "wh:secret",
		"menu:notify", "ntf:trial", "ntf:sub", "ntf:w:7", "ntf:w:3", "ntf:trialdays",
		"menu:update",
		"menu:reconf",
	}
	for _, b := range buttons {
		click(b)
	}
}

func cryptoBotStub(t *testing.T, status string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/createInvoice"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"invoice_id":42,"status":"active","asset":"USDT","amount":"1.5","mini_app_invoice_url":"https://t.me/inv"}}`))
		case strings.HasSuffix(r.URL.Path, "/getInvoices"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"items":[{"invoice_id":42,"status":"` + status + `","asset":"USDT","amount":"1.5"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	prev := cryptobot.BaseURL
	cryptobot.BaseURL = srv.URL
	t.Cleanup(func() { cryptobot.BaseURL = prev; srv.Close() })
}

func TestUserButtonWalk(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://sub/abc","expireAt":"2030-01-01T00:00:00Z"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/abc","expireAt":"2030-01-01T00:00:00Z"}}`))
	}))
	defer panel.Close()
	defer mockYooKassa(t, map[string]any{
		"id": "pay_1", "status": "pending",
		"confirmation": map[string]string{"confirmation_url": "https://pay/redirect"},
		"metadata":     map[string]string{"telegram_id": "555", "months": "1"},
	})()
	cryptoBotStub(t, "active")

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = fullyConfigured()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	ctx := context.Background()
	const u int64 = 555

	a.handleMessage(ctx, msgText(u, "/start"))
	if len(fm.texts) == 0 {
		t.Fatalf("/start ничего не показал новому пользователю")
	}
	a.handleCallback(ctx, cb(u, "menu:register"))

	a.handleCallback(ctx, cb(u, "menu:buy"))
	a.handleCallback(ctx, cb(u, "buy:1"))
	if !strings.Contains(fm.joined(), "оплат") {
		t.Fatalf("экран выбора способа оплаты не показан:\n%s", fm.joined())
	}

	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:stars"))
	if len(fm.invoices) == 0 {
		t.Fatalf("Stars: инвойс не выставлен")
	}

	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:yk"))

	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:cb"))

	pend, _ := fs.ListUnresolvedPending(ctx, "9999-12-31T23:59:59Z", 100)
	var hasYK, hasCB bool
	for _, p := range pend {
		if p.Method == model.PayMethodYooKassa {
			hasYK = true
		}
		if p.Method == model.PayMethodCryptoBot {
			hasCB = true
		}
	}
	if !hasYK || !hasCB {
		t.Fatalf("pending-инвойсы записаны не для всех платёжек: yk=%v cb=%v", hasYK, hasCB)
	}

	before := len(fm.texts)
	a.handleCallback(ctx, cb(u, "cbc:42:1"))
	if len(fm.texts) <= before {
		t.Errorf("кнопка cbc (проверка CryptoBot) ничего не показала")
	}
	before = len(fm.texts)
	a.handleCallback(ctx, cb(u, "ykc:pay_1"))
	if len(fm.texts) <= before {
		t.Errorf("кнопка ykc (проверка YooKassa) ничего не показала")
	}

	a.handleCallback(ctx, cb(u, "buy:1"))
	a.handleCallback(ctx, cb(u, "method:p2p"))

	before = len(fm.texts)
	a.handleCallback(ctx, cb(u, "menu:topup"))
	a.handleCallback(ctx, cb(u, "top:amt:15000"))
	a.handleCallback(ctx, cb(u, "top:custom"))
	a.handleCallback(ctx, cb(u, "top:cancel"))
	if len(fm.texts) <= before {
		t.Errorf("экраны пополнения баланса ничего не показали")
	}

	a.handleCallback(ctx, cb(u, "menu:trial"))
	if usr, _ := fs.GetUser(ctx, u); usr == nil || usr.TrialUsedAt == "" {
		t.Fatalf("триал не активировался (TrialUsedAt пуст)")
	}

	before = len(fm.texts)
	a.handleCallback(ctx, cb(u, "menu:mysubs"))
	if len(fm.texts) <= before {
		t.Errorf("«Мои подписки» ничего не показали")
	}

	a.handleCallback(ctx, cb(u, "menu:home"))
}
