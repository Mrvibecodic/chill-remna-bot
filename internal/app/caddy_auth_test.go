package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
)

// keyStub — панель за «Caddy with security»: без X-Api-Key прокси заворачивает
// запрос на портал входа (302), с ключом — пропускает.
func keyStub(t *testing.T, key string, users int) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("X-Api-Key"))
		mu.Unlock()
		if r.Header.Get("X-Api-Key") != key {
			w.Header().Set("Location", "/r")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/system/stats") {
			_, _ = w.Write([]byte(`{"response":{"users":{"totalUsers":` + itoa(users) + `}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"isConnected":true}}`))
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// С заданным CADDY_AUTH_API_TOKEN мастер про ключ не спрашивает: проверка связи
// проходит сразу после токена панели, а ключ уезжает в заголовке.
func TestWizardUsesCaddyTokenFromEnv(t *testing.T) {
	srv, seen := keyStub(t, "env-key", 7)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	a.cfg.CaddyAuthToken = "env-key"
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:ru"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:remote"))
	a.handleCallback(ctx, cb(100, "inst:docs"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	a.handleMessage(ctx, msgText(100, "api-token-xyz"))

	if !a.installed() {
		t.Fatalf("бот не установлен без вопроса про ключ; лог:\n%s", fm.joined())
	}
	log := fm.joined()
	if strings.Contains(log, "Защищён ли путь") {
		t.Fatalf("мастер всё равно спросил про X-API-Key; лог:\n%s", log)
	}
	if !strings.Contains(log, "CADDY_AUTH_API_TOKEN") {
		t.Fatalf("мастер не сказал, откуда взят ключ; лог:\n%s", log)
	}
	for _, got := range seen() {
		if got != "env-key" {
			t.Fatalf("панель получила X-Api-Key = %q", got)
		}
	}
	// Секрет из окружения не должен оседать в БД: там он устареет при смене
	// переменной и начнёт молча спорить с ней.
	if fs.cfg == nil || fs.cfg.Panel.APIKey != "" {
		t.Fatalf("ключ из env сохранён в конфиг: %+v", fs.cfg)
	}
}

// Ключ из окружения работает и там, где мастер про него не спрашивает вовсе:
// установка eGames, локальная панель, уже настроенный бот.
func TestPanelWithEnvOverridesStoredKey(t *testing.T) {
	a := &App{cfg: &config.Config{CaddyAuthToken: "env-key"}}
	got := a.panelWithEnv(model.PanelConfig{APIKey: "old-key", APIToken: "T"})
	if got.APIKey != "env-key" {
		t.Fatalf("APIKey = %q, ожидался ключ из окружения", got.APIKey)
	}
	if got.APIToken != "T" {
		t.Fatalf("остальной конфиг панели пострадал: %+v", got)
	}

	// Без переменной остаётся то, что ввели в мастере.
	a = &App{cfg: &config.Config{}}
	if got = a.panelWithEnv(model.PanelConfig{APIKey: "old-key"}); got.APIKey != "old-key" {
		t.Fatalf("APIKey = %q, ожидалось сохранённое значение", got.APIKey)
	}
	if a.caddyKeyFromEnv() {
		t.Fatal("caddyKeyFromEnv() = true при пустой переменной")
	}
}

// Мастер без переменной ведёт себя по-старому: спрашивает про защиту /api.
func TestWizardStillAsksWithoutEnv(t *testing.T) {
	srv, _ := keyStub(t, "", 3)
	defer srv.Close()
	a, fm, _ := newTestApp(t)
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:ru"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:remote"))
	a.handleCallback(ctx, cb(100, "inst:docs"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	a.handleMessage(ctx, msgText(100, "api-token-xyz"))

	if !strings.Contains(fm.joined(), "Защищён ли путь") {
		t.Fatalf("мастер не спросил про защиту /api; лог:\n%s", fm.joined())
	}
}

// Страница подписки может жить на домене панели — тогда её тоже закрывает
// Caddy, и ключ нужен. На чужой хост секрет уходить не должен.
func TestCaddyKeyOnlyForPanelHost(t *testing.T) {
	a := &App{
		cfg:    &config.Config{CaddyAuthToken: "env-key"},
		botCfg: &model.BotConfig{Panel: model.PanelConfig{BaseURL: "https://panel.example.com/"}},
	}
	base := a.panelBaseURL()
	if got := a.caddyKeyFor("https://PANEL.example.com/api/sub/abc", base); got != "env-key" {
		t.Fatalf("для домена панели ключ = %q", got)
	}
	leaky := []string{
		"https://sub.example.com/abc",     // другой хост
		"https://evil.example.net/x",      // чужой домен
		"http://panel.example.com/api",    // тот же хост, но открытый http
		"https://panel.example.com.evil/", // хост-обманка
		"", "://broken",
	}
	for _, u := range leaky {
		if got := a.caddyKeyFor(u, base); got != "" {
			t.Fatalf("ключ утёк на %q: %q", u, got)
		}
	}

	// Без переменной окружения не отдаём ключ даже своему хосту.
	a.cfg = &config.Config{}
	if got := a.caddyKeyFor("https://panel.example.com/api/sub/abc", base); got != "" {
		t.Fatalf("ключ взялся из ниоткуда: %q", got)
	}

	// Незаполненный конфиг не должен ронять вызов.
	empty := &App{cfg: &config.Config{CaddyAuthToken: "env-key"}}
	if got := empty.caddyKeyFor("https://panel.example.com/", empty.panelBaseURL()); got != "" {
		t.Fatalf("ключ отдан без конфигурации панели: %q", got)
	}
}

// Ключ Caddy не должен уезжать на чужой хост по редиректу: Go копирует
// произвольные заголовки на любой домен, снимая только Authorization и Cookie.
func TestCaddyKeyDroppedOnCrossHostRedirect(t *testing.T) {
	var foreign http.Header
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreign = r.Header.Clone()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer other.Close()

	var panelHits int
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panelHits++
		http.Redirect(w, r, other.URL+"/moved", http.StatusFound)
	}))
	defer panelSrv.Close()

	a, _, _ := newTestApp(t)
	a.cfg.CaddyAuthToken = "env-key"
	a.botCfg = &model.BotConfig{Panel: model.PanelConfig{BaseURL: panelSrv.URL}}

	a.fetchAppConfig(context.Background(), panelSrv.URL, panelSrv.URL+"/sub/abc")

	if panelHits == 0 {
		t.Fatal("панель не опрошена — тест ничего не проверил")
	}
	if foreign == nil {
		t.Fatal("редирект не отработал")
	}
	if got := foreign.Get("X-Api-Key"); got != "" {
		t.Fatalf("ключ уехал на чужой хост: %q", got)
	}
}

// Переустановка не должна возвращать в БД ключ, введённый когда-то руками:
// пока переменная задана, он всё равно не работает, а всплывёт ровно в тот
// день, когда переменную уберут. Проверяем ветку eGames — там мастер про ключ
// не спрашивает вовсе.
func TestReconfigureDropsStoredKeyWhenEnvSet(t *testing.T) {
	srv, _ := keyStub(t, "env-key", 5)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	a.cfg.CaddyAuthToken = "env-key"
	ctx := context.Background()

	a.handleMessage(ctx, msgText(100, "/start"))
	a.handleCallback(ctx, cb(100, "lang:ru"))
	a.handleCallback(ctx, cb(100, "db:sqlite"))
	a.handleCallback(ctx, cb(100, "loc:remote"))
	a.handleCallback(ctx, cb(100, "inst:egames"))
	a.handleMessage(ctx, msgText(100, srv.URL))
	a.handleMessage(ctx, msgText(100, "api-token-xyz"))

	// Так выглядит переустановка: конфиг мастера засеян прошлой установкой.
	a.mu.Lock()
	a.wiz[100].cfg.Panel.APIKey = "STALE-OLD-KEY"
	a.mu.Unlock()
	a.handleMessage(ctx, msgText(100, "XkPmtZQr=fNbWqLpA"))

	if !a.installed() {
		t.Fatalf("установка не завершилась; лог:\n%s", fm.joined())
	}
	if fs.cfg == nil || fs.cfg.Panel.APIKey != "" {
		t.Fatalf("старый ключ сохранён в БД: %q", fs.cfg.Panel.APIKey)
	}
	if fs.cfg.Panel.Cookie != "XkPmtZQr=fNbWqLpA" {
		t.Fatalf("кука eGames потерялась: %q", fs.cfg.Panel.Cookie)
	}
}
