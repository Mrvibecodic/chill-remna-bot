package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// tokenStub — панель, которая пускает только с заданным API-token и помнит,
// с какими токенами к ней приходили.
func tokenStub(t *testing.T, token string, users int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, got)
		mu.Unlock()
		if got != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":` + itoa(users) + `}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

func (f *fakeMsg) hasCallback(data string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.cbData, data)
}

// Новый токен проверяется на панели и только потом сохраняется; клиент
// пересобирается, и бот сразу ходит с новым токеном.
func TestPanelConnTokenChange(t *testing.T) {
	srv, seen := tokenStub(t, "new-token", 5)
	a, fm, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "old-token",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:system"))
	if !fm.hasCallback("menu:panelauth") {
		t.Fatal("в «Системе» нет кнопки подключения к панели")
	}
	a.handleCallback(ctx, cb(100, "menu:panelauth"))
	if strings.Contains(fm.joined(), "old-token") {
		t.Fatalf("токен показан открытым текстом; лог:\n%s", fm.joined())
	}
	a.handleCallback(ctx, cb(100, "pauth:token"))
	a.handleMessage(ctx, msgText(100, "Bearer new-token"))

	if fs.cfg == nil || fs.cfg.Panel.APIToken != "new-token" {
		t.Fatalf("токен не сохранён: %+v", fs.cfg)
	}
	if !strings.Contains(fm.lastLive(), "панель на связи") {
		t.Fatalf("нет вердикта; экран:\n%s", fm.lastLive())
	}
	_ = a.panel.Health(ctx)
	if s := seen(); s[len(s)-1] != "new-token" {
		t.Fatalf("клиент не пересобран, запросы идут с %q", s[len(s)-1])
	}
}

// Токен, с которым панель не пускает, не сохраняется; «Сохранить без проверки»
// записывает его по явной просьбе, а уход с экрана сбрасывает отложенное.
func TestPanelConnBadTokenPendingAndForce(t *testing.T) {
	srv, _ := tokenStub(t, "good", 5)
	a, fm, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "good",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	before := a.panel
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "pauth:token"))
	a.handleMessage(ctx, msgText(100, "wrong"))
	if fs.cfg != nil {
		t.Fatalf("непроверенный токен записан: %+v", fs.cfg.Panel)
	}
	if a.botCfg.Panel.APIToken != "good" || a.panel != before {
		t.Fatal("непроверенный токен применён к живому боту")
	}
	if !strings.Contains(fm.lastLive(), "ничего не сохранено") || !fm.hasCallback("pauth:force") {
		t.Fatalf("не предложено сохранить без проверки; экран:\n%s", fm.lastLive())
	}

	// Уход с экрана сбрасывает отложенное значение.
	a.handleCallback(ctx, cb(100, "menu:home"))
	a.handleCallback(ctx, cb(100, "pauth:force"))
	if fs.cfg != nil {
		t.Fatal("старая кнопка сохранила значение после ухода с экрана")
	}

	a.handleCallback(ctx, cb(100, "pauth:token"))
	a.handleMessage(ctx, msgText(100, "wrong"))
	a.handleCallback(ctx, cb(100, "pauth:force"))
	if fs.cfg == nil || fs.cfg.Panel.APIToken != "wrong" {
		t.Fatalf("«Сохранить без проверки» не сохранило: %+v", fs.cfg)
	}
	if a.panel == before {
		t.Fatal("клиент не пересобран после сохранения без проверки")
	}
	if a.getUI(100).panelPending != nil {
		t.Fatal("отложенное значение не снято")
	}
}

// Смена адреса переводит локальную установку в удалённую: в локальном режиме
// клиент игнорирует введённый адрес.
func TestPanelConnURLSwitchesMode(t *testing.T) {
	srv, _ := tokenStub(t, "T", 3)
	a, _, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeLocal, BaseURL: remnawave.LocalBaseURL, APIToken: "T",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "pauth:url"))
	a.handleMessage(ctx, msgText(100, srv.URL+"/api/"))
	if fs.cfg == nil || fs.cfg.Panel.BaseURL != srv.URL || fs.cfg.Panel.Mode != model.ModeRemote {
		t.Fatalf("адрес не сохранён как удалённый: %+v", fs.cfg)
	}
	if err := a.panel.Health(ctx); err != nil {
		t.Fatalf("клиент не ходит на новый адрес: %v", err)
	}

	// Обратно в локальную — только через docker-адрес.
	applyConnField(&fs.cfg.Panel, "panel_url", remnawave.LocalBaseURL)
	if fs.cfg.Panel.Mode != model.ModeLocal {
		t.Fatal("docker-адрес не включил локальный режим")
	}
}

// Неудачная запись не оставляет в памяти значение, которого нет в базе.
func TestPanelConnRollsBackOnSaveError(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://panel.example.com", APIToken: "T",
	}}
	a.store = nil
	a.panel = a.newPanel(a.botCfg.Panel)
	before := a.panel

	if err := a.commitPanelConn(context.Background(), "panel_url", "https://other.example.com"); err == nil {
		t.Fatal("ошибка записи потеряна")
	}
	if p := a.botCfg.Panel; p.BaseURL != "https://panel.example.com" || p.Mode != model.ModeRemote {
		t.Fatalf("адрес не откачен: %+v", p)
	}
	if a.panel != before {
		t.Fatal("клиент пересобран, хотя сохранить не удалось")
	}
}

func TestNormalizePanelURL(t *testing.T) {
	ok := map[string]string{
		"panel.example.com":                   "https://panel.example.com",
		" https://Panel.Example.com/ ":        "https://panel.example.com",
		"https://panel.example.com/api":       "https://panel.example.com",
		"https://panel.example.com/api/":      "https://panel.example.com",
		"http://remnawave:3000":               remnawave.LocalBaseURL,
		"https://example.com:8443/rw/":        "https://example.com:8443/rw",
		"HTTPS://panel.example.com/sub/api//": "https://panel.example.com/sub",
		"remnawave:3000":                      remnawave.LocalBaseURL,
		"https://remnawave:3000/api":          remnawave.LocalBaseURL,
		"panel.example.com.":                  "https://panel.example.com",
		"https://Панель.рф":                   "https://xn--80aksgi6f.xn--p1ai",
		"http://203.0.113.5:3000":             "http://203.0.113.5:3000",
		"https://[2001:db8::1]:3000/":         "https://[2001:db8::1]:3000",
	}
	for in, want := range ok {
		if got, good := normalizePanelURL(in); !good || got != want {
			t.Errorf("normalizePanelURL(%q) = %q, %v; ожидалось %q", in, got, good, want)
		}
	}
	for _, in := range []string{
		"", "   ", "ftp://panel.example.com", "https://", "https://user:pw@panel.example.com",
		"https://panel.example.com/?k=v", "https://panel.example.com/#x", "panel example.com",
		"javascript:alert(1)", "https://:443", "https://panel.example.com:",
		"https://panel.example.com:99999", "https://panel.example.com:0",
	} {
		if got, good := normalizePanelURL(in); good {
			t.Errorf("normalizePanelURL(%q) принял мусор: %q", in, got)
		}
	}
}

func TestNormalizePanelToken(t *testing.T) {
	for in, want := range map[string]string{
		" abc.def ":      "abc.def",
		"Bearer abc.def": "abc.def",
		"bearer  xyz":    "xyz",
	} {
		if got, ok := normalizePanelToken(in); !ok || got != want {
			t.Errorf("normalizePanelToken(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "-", "—", "Bearer ", "ab cd", "ab\ncd"} {
		if _, ok := normalizePanelToken(in); ok {
			t.Errorf("normalizePanelToken(%q) принял мусор", in)
		}
	}
}

// Домен подписки: мусор не сохраняется, кириллица уходит в punycode, «Назад»
// ведёт на экран подключения.
func TestSubdomainValidation(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, SubscriptionDomain: "sub.example.com"}
	a.store = fs
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:subdomain"))
	if !fm.hasCallback("menu:panelauth") {
		t.Fatal("«Назад» не ведёт на экран подключения")
	}
	a.handleCallback(ctx, cb(100, "subd:edit"))
	a.handleMessage(ctx, msgText(100, "<b>bad host"))
	if a.botCfg.SubscriptionDomain != "sub.example.com" || fs.cfg != nil {
		t.Fatalf("мусор сохранён: %q", a.botCfg.SubscriptionDomain)
	}
	if !strings.Contains(fm.lastLive(), "не похоже на домен") {
		t.Fatalf("нет отказа; экран:\n%s", fm.lastLive())
	}

	a.handleCallback(ctx, cb(100, "subd:edit"))
	a.handleMessage(ctx, msgText(100, "https://Впн.Пример.рф:8443/sub/abc"))
	if fs.cfg == nil || fs.cfg.SubscriptionDomain != "xn--b1awf.xn--e1afmkfd.xn--p1ai:8443" {
		t.Fatalf("домен не сохранён в punycode: %+v", fs.cfg)
	}

	a.handleCallback(ctx, cb(100, "subd:clear"))
	if fs.cfg.SubscriptionDomain != "" {
		t.Fatalf("домен не сброшен: %q", fs.cfg.SubscriptionDomain)
	}
}

// При смене сервера ключ и кука прежнего прокси не уходят на новый адрес — ни
// при проверке, ни после сохранения.
func TestPanelConnURLDropsProxySecrets(t *testing.T) {
	var mu sync.Mutex
	var leaked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if v := r.Header.Get("X-Api-Key") + r.Header.Get("Cookie"); v != "" {
			leaked = append(leaked, v)
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":1}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	defer srv.Close()
	a, _, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://old.example.com", APIToken: "T",
		APIKey: "old-key", Cookie: "AbCdEfGh=IjKlMnOp",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "pauth:url"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	if fs.cfg == nil || fs.cfg.Panel.BaseURL != srv.URL {
		t.Fatalf("адрес не сохранён: %+v", fs.cfg)
	}
	if fs.cfg.Panel.APIKey != "" || fs.cfg.Panel.Cookie != "" {
		t.Fatalf("секреты прежнего прокси остались: %+v", fs.cfg.Panel)
	}
	_ = a.panel.Health(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 {
		t.Fatalf("секреты прежнего прокси ушли на новый адрес: %v", leaked)
	}

	// Тот же сервер, другой путь — секреты остаются.
	p := model.PanelConfig{Mode: model.ModeRemote, BaseURL: "https://old.example.com", APIKey: "k", Cookie: "c"}
	applyConnField(&p, "panel_url", "https://old.example.com:8443/rw")
	if p.APIKey != "k" || p.Cookie != "c" {
		t.Fatalf("секреты сняты без смены сервера: %+v", p)
	}
}

// Отказ с HTML-страницей в ответе: экран не рвёт сущность и сохраняет
// подсказку про «Сохранить без проверки».
func TestPanelConnLongHTMLError(t *testing.T) {
	page := strings.Repeat(`<a class="x" href="/y">&</a>`, 60)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://old.example.com", APIToken: "T",
	}}
	a.store = fs
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "pauth:url"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	last := fm.lastLive()
	if !strings.Contains(last, "Сохранить без проверки") {
		t.Fatalf("подсказка потерялась:\n%s", last)
	}
	for i := 0; i < len(last); i++ {
		if last[i] != '&' {
			continue
		}
		j := strings.IndexAny(last[i:], "; ")
		if j < 0 || last[i+j] != ';' {
			t.Fatalf("разрезанная HTML-сущность: %q", last[i:min(len(last), i+12)])
		}
	}
}

func TestCutHTML(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 6, 7} {
		got := cutHTML("ab&lt;cd&amp;", n)
		if strings.Contains(got, "&") && !strings.Contains(got, ";") {
			t.Errorf("cutHTML(.., %d) = %q — сущность разрезана", n, got)
		}
	}
	if got := cutHTML("short", 10); got != "short" {
		t.Errorf("короткий текст изменён: %q", got)
	}
}

// Отказ «бот не настроен» показывается на языке админа, а не по-русски.
func TestPanelConnNotConfiguredLocalized(t *testing.T) {
	a, fm, _ := newTestApp(t)
	a.wiz[100] = &wizard{cfg: model.BotConfig{Language: model.LangEN}}
	a.getUI(100).panelPending = &panelPending{field: "panel_token", value: "x"}
	a.forcePanelConn(context.Background(), 100)
	last := fm.lastLive()
	if !strings.Contains(last, "not set up yet") || strings.Contains(last, "не настроен") {
		t.Fatalf("отказ не переведён:\n%s", last)
	}
}
