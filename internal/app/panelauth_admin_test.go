package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Ключ и куку можно поменять после установки: значение сохраняется, клиент
// панели пересобирается сразу, и бот тут же докладывает о связи.
func TestPanelAuthEditsSecrets(t *testing.T) {
	srv, seen := keyStub(t, "new-key", 9)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "T", APIKey: "old-key",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:panelauth"))
	if !strings.Contains(fm.joined(), "Доступ к панели") {
		t.Fatalf("экран не открылся; лог:\n%s", fm.joined())
	}
	// Старый ключ показан замаскированным, а не целиком.
	if strings.Contains(fm.joined(), "old-key") {
		t.Fatalf("ключ показан открытым текстом; лог:\n%s", fm.joined())
	}

	a.handleCallback(ctx, cb(100, "pauth:key"))
	a.handleMessage(ctx, msgText(100, "new-key"))

	if fs.cfg == nil || fs.cfg.Panel.APIKey != "new-key" {
		t.Fatalf("ключ не сохранён: %+v", fs.cfg)
	}
	if got := seen(); len(got) == 0 || got[len(got)-1] != "new-key" {
		t.Fatalf("панель не получила новый ключ: %v", got)
	}
	if !strings.Contains(fm.joined(), "Панель на связи") {
		t.Fatalf("связь не проверена после сохранения; лог:\n%s", fm.joined())
	}

	// Кука правится там же.
	a.handleCallback(ctx, cb(100, "pauth:cookie"))
	a.handleMessage(ctx, msgText(100, "XkPmtZQr=fNbWqLpA"))
	if fs.cfg.Panel.Cookie != "XkPmtZQr=fNbWqLpA" {
		t.Fatalf("кука не сохранена: %q", fs.cfg.Panel.Cookie)
	}

	// «-» убирает секрет.
	a.handleCallback(ctx, cb(100, "pauth:cookieclear"))
	if fs.cfg.Panel.Cookie != "" {
		t.Fatalf("кука не убрана: %q", fs.cfg.Panel.Cookie)
	}
}

// Пока ключ приходит из переменной, редактировать его в админке нельзя —
// иначе правка молча ничего бы не меняла.
func TestPanelAuthKeyLockedByEnv(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.cfg = &config.Config{AdminID: 100, CaddyAuthToken: "env-key"}
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://panel.example.com", APIToken: "T",
	}}
	a.store = fs
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:panelauth"))
	if !strings.Contains(fm.joined(), "CADDY_AUTH_API_TOKEN") {
		t.Fatalf("не сказано, что ключ из переменной; лог:\n%s", fm.joined())
	}
	a.handleCallback(ctx, cb(100, "pauth:key"))
	if a.getUI(100).adminInput != "" {
		t.Fatal("бот ждёт ввода ключа, хотя он задан переменной")
	}
	a.setPanelSecret(ctx, 100, "panel_apikey", "manual")
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg.Panel.APIKey != "" {
		t.Fatalf("ключ записан поверх переменной: %q", a.botCfg.Panel.APIKey)
	}
}

// Локальной панели в общей docker-сети экран не нужен — не показываем кнопку.
func TestPanelAuthHiddenForLocal(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Panel: model.PanelConfig{Mode: model.ModeLocal, BaseURL: remnawave.LocalBaseURL}}
	if a.panelAuthRelevant() {
		t.Fatal("кнопка показана для локальной панели без секретов")
	}
	a.botCfg.Panel.Cookie = "A=B"
	if !a.panelAuthRelevant() {
		t.Fatal("кнопка скрыта, хотя кука задана")
	}
	a.botCfg.Panel.Cookie = ""
	a.cfg.CaddyAuthToken = "env-key"
	if !a.panelAuthRelevant() {
		t.Fatal("кнопка скрыта, хотя ключ задан переменной")
	}
}

func TestMaskSecret(t *testing.T) {
	for in, want := range map[string]string{
		"":                 "",
		"ab":               "••••••••", // короткое значение закрываем целиком
		"abcde":            "••••••••",
		"abcdefghijklmnop": "ab••••••••op",
		"<b=x&y>1234567":   "&lt;b••••••••67", // HTML экранируется, иначе экран не откроется
	} {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, ожидалось %q", in, got, want)
		}
	}
	if strings.Contains(maskSecret("supersecretvalue"), "persecretvalu") {
		t.Error("маска раскрывает середину")
	}
}

// Вердикт проверки связи должен пережить перерисовку экрана: если слать его
// отдельным сообщением, следующий же экран его удалит и админ ничего не увидит.
func TestPanelAuthVerdictSurvives(t *testing.T) {
	srv, _ := keyStub(t, "new-key", 9)
	defer srv.Close()
	a, fm, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "T",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "pauth:key"))
	a.handleMessage(ctx, msgText(100, "new-key"))

	last := fm.lastLive()
	if !strings.Contains(last, "Панель на связи") {
		t.Fatalf("вердикт не в экране; последнее живое сообщение:\n%s", last)
	}
	if !strings.Contains(last, "Доступ к панели") {
		t.Fatalf("экран не отрисован вместе с вердиктом:\n%s", last)
	}
}

// Уход с экрана без «Отмены» снимает ожидание секрета: иначе первое случайное
// сообщение молча стало бы ключом панели и отрезало бы бота от неё.
func TestPanelAuthInputNotSticky(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://panel.example.com", APIToken: "T", APIKey: "good-key",
	}}
	a.store = fs
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "pauth:key"))
	if a.getUI(100).adminInput != "panel_apikey" {
		t.Fatal("ожидание ключа не взведено")
	}
	a.handleCallback(ctx, cb(100, "menu:home"))
	if got := a.getUI(100).adminInput; got != "" {
		t.Fatalf("ожидание ключа пережило уход в меню: %q", got)
	}
	a.handleMessage(ctx, msgText(100, "просто заметка"))
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg.Panel.APIKey != "good-key" {
		t.Fatalf("случайный текст перезаписал ключ: %q", a.botCfg.Panel.APIKey)
	}
}

// Неудачное сохранение не должно оставлять память, БД и живого клиента с
// разными значениями.
func TestPanelAuthRollsBackOnSaveError(t *testing.T) {
	a, _, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://panel.example.com", APIToken: "T", APIKey: "old-key",
	}}
	a.store = nil // сохранять некуда
	a.panel = a.newPanel(a.botCfg.Panel)
	before := a.panel

	a.setPanelSecret(context.Background(), 100, "panel_apikey", "typo-key")

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg.Panel.APIKey != "old-key" {
		t.Fatalf("в памяти осталось несохранённое значение: %q", a.botCfg.Panel.APIKey)
	}
	if a.panel != before {
		t.Fatal("клиент пересобран, хотя сохранить не удалось")
	}
}

// Ключ, оставшийся в БД с прежней установки, при активной переменной можно
// убрать — иначе он воскреснет в день, когда переменную снимут.
func TestPanelAuthClearsStaleKeyUnderEnv(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.cfg = &config.Config{AdminID: 100, CaddyAuthToken: "env-key"}
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: "https://panel.example.com", APIToken: "T", APIKey: "stale",
	}}
	a.store = fs
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:panelauth"))
	if !strings.Contains(fm.joined(), "старый ключ") {
		t.Fatalf("экран не предупредил про старый ключ; лог:\n%s", fm.joined())
	}
	a.handleCallback(ctx, cb(100, "pauth:keyclear"))
	if fs.cfg == nil || fs.cfg.Panel.APIKey != "" {
		t.Fatalf("старый ключ не убран: %+v", fs.cfg)
	}
}

// Лог API живёт внутри клиента: пересборка при смене секрета не должна стирать
// историю, ради которой админ туда и смотрит.
func TestPanelAuthKeepsAPILog(t *testing.T) {
	srv, _ := keyStub(t, "new-key", 4)
	defer srv.Close()
	a, _, fs := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Panel: model.PanelConfig{
		Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "T",
	}}
	a.store = fs
	a.panel = a.newPanel(a.botCfg.Panel)
	ctx := context.Background()

	_ = a.panel.Health(ctx) // одна запись в логе
	before := len(a.panel.Logs())
	if before == 0 {
		t.Fatal("лог пуст — тест ничего не проверит")
	}
	a.setPanelSecret(ctx, 100, "panel_apikey", "new-key")
	if got := len(a.panel.Logs()); got < before {
		t.Fatalf("лог обрезан пересборкой клиента: было %d, стало %d", before, got)
	}
}
