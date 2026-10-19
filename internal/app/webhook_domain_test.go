package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func TestCleanDomain(t *testing.T) {
	cases := map[string]string{
		"bot.example.com":                   "bot.example.com",
		"  Bot.Example.com  ":               "bot.example.com",
		"https://bot.example.com/":          "bot.example.com",
		"HTTPS://bot.example.com/webhook":   "bot.example.com",
		"https://https://bot.example.com":   "bot.example.com",
		"http://bot.example.com:8443/x?y=1": "bot.example.com",
		"bot.example.com.":                  "bot.example.com",
		"бот.рф":                            "xn--90a0am.xn--p1ai",
		"https://":                          "",
		"https:// ":                         "",
		"":                                  "",
		"localhost":                         "",
		"203.0.113.5":                       "",
		"bot example.com":                   "",
	}
	for in, want := range cases {
		if got := cleanDomain(in); got != want {
			t.Errorf("cleanDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidBaseURL(t *testing.T) {
	for _, s := range []string{"https://bot.example.com", "http://203.0.113.5:8080", "https://bot.example.com/sub", "http://localhost:8080"} {
		if !validBaseURL(s) {
			t.Errorf("%q должен подходить", s)
		}
	}
	for _, s := range []string{"https://", "https://https:", "ftp://bot.example.com", "bot.example.com", ""} {
		if validBaseURL(s) {
			t.Errorf("%q не должен подходить", s)
		}
	}
}

// Ввод «https://» в поле домена не сохраняется: раньше адреса для платёжек
// превращались в «https://https://».
func TestWebhookDomainInput(t *testing.T) {
	a, msg, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	ask := func(text string) {
		msg.texts = nil
		a.handleCallback(ctx, cb(100, "wh:domain"))
		a.handleMessage(ctx, msgText(100, text))
	}

	ask("https:// ")
	if d := a.botCfg.Webhook.Domain; d != "" || a.botCfg.Webhook.PublicBaseURL != "" {
		t.Fatalf("мусор сохранился: domain=%q base=%q", d, a.botCfg.Webhook.PublicBaseURL)
	}
	if !strings.Contains(strings.Join(msg.texts, "\n"), "Домен не сохранён") {
		t.Fatalf("нет объяснения отказа: %v", msg.texts)
	}

	ask("https://bot.example.com/")
	if d, b := a.botCfg.Webhook.Domain, a.botCfg.Webhook.PublicBaseURL; d != "bot.example.com" || b != "https://bot.example.com" {
		t.Fatalf("domain=%q base=%q", d, b)
	}

	msg.texts = nil
	a.handleCallback(ctx, cb(100, "wh:base"))
	a.handleMessage(ctx, msgText(100, "https://"))
	if b := a.botCfg.Webhook.PublicBaseURL; b != "https://bot.example.com" {
		t.Fatalf("негодный адрес затёр рабочий: %q", b)
	}
	if !strings.Contains(strings.Join(msg.texts, "\n"), "Адрес не сохранён") {
		t.Fatalf("нет объяснения отказа: %v", msg.texts)
	}
}

// Уже сохранённый мусор чинится при запуске.
func TestHealWebhookConfig(t *testing.T) {
	cfg := &model.BotConfig{}
	cfg.Webhook.Domain = "https://"
	cfg.Webhook.PublicBaseURL = "https://https://"
	healWebhookConfig(cfg)
	if cfg.Webhook.Domain != "" || cfg.Webhook.PublicBaseURL != "" {
		t.Fatalf("domain=%q base=%q", cfg.Webhook.Domain, cfg.Webhook.PublicBaseURL)
	}

	cfg.Webhook.Domain = "https://bot.example.com/"
	cfg.Webhook.PublicBaseURL = "https://https://bot.example.com/"
	healWebhookConfig(cfg)
	if cfg.Webhook.Domain != "bot.example.com" || cfg.Webhook.PublicBaseURL != "https://bot.example.com" {
		t.Fatalf("domain=%q base=%q", cfg.Webhook.Domain, cfg.Webhook.PublicBaseURL)
	}

	// Чинится именно при загрузке конфига.
	a, _, fs := newTestApp(t)
	a.store = fs
	fs.cfg = &model.BotConfig{Installed: true, Language: "ru"}
	fs.cfg.Webhook.Domain = "https://"
	fs.cfg.Webhook.PublicBaseURL = "https://https://"
	if err := a.loadConfigIfStore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.botCfg.Webhook.Domain != "" || a.botCfg.Webhook.PublicBaseURL != "" {
		t.Fatalf("после загрузки: domain=%q base=%q", a.botCfg.Webhook.Domain, a.botCfg.Webhook.PublicBaseURL)
	}

	// Рабочие значения не трогаются.
	cfg.Webhook.PublicBaseURL = "http://203.0.113.5:8080"
	healWebhookConfig(cfg)
	if cfg.Webhook.PublicBaseURL != "http://203.0.113.5:8080" {
		t.Fatalf("рабочий адрес изменён: %q", cfg.Webhook.PublicBaseURL)
	}
}
