package app

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/moynalog"
)

func (a *App) moynalogCfg() model.MoyNalogConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.MoyNalogConfig{}
	}
	return a.botCfg.MoyNalog
}

func (a *App) mnClientGet() (*moynalog.Client, model.MoyNalogConfig) {
	cfg := a.moynalogCfg()
	if !cfg.Enabled || cfg.Login == "" || cfg.Password == "" {
		return nil, cfg
	}
	a.mnMu.Lock()
	defer a.mnMu.Unlock()
	key := cfg.Login + "|" + cfg.Password
	if a.mnClient == nil || a.mnKey != key {
		a.mnClient = moynalog.New(cfg.Login, cfg.Password)
		a.mnKey = key
	}
	return a.mnClient, cfg
}

func (a *App) fiscalize(amountRub float64, detail string) {
	client, cfg := a.mnClientGet()
	if client == nil || amountRub <= 0 {
		return
	}
	name := detail
	if cfg.ServiceName != "" {
		name = cfg.ServiceName
	}
	go func() {
		ctx, cancel := context.WithTimeout(a.bgContext(), 30*time.Second)
		defer cancel()
		id, err := client.CreateIncome(ctx, amountRub, name)
		if err != nil {
			a.log.Error("moynalog income", "err", err, "amount", amountRub)
			return
		}
		a.log.Info("moynalog receipt registered", "id", id, "amount", amountRub)
	}()
}

func parseAmountRub(s string) float64 {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.ReplaceAll(f[0], ",", "."), 64)
	return v
}

func (a *App) showMoyNalogAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.moynalogCfg()
	mark := "❌"
	if cfg.Enabled {
		mark = "✅"
	}
	login := i18n.T(lang, "mn.not_set")
	if cfg.Login != "" {
		login = cfg.Login
	}
	pass := i18n.T(lang, "mn.not_set")
	if cfg.Password != "" {
		pass = "•••••"
	}
	name := cfg.ServiceName
	if name == "" {
		name = i18n.T(lang, "mn.default_name")
	}
	text := i18n.T(lang, "mn.title", mark, login, pass, name)
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "mn.btn_toggle"), "mn:toggle")},
		{btn(i18n.T(lang, "mn.btn_login"), "mn:login"), btn(i18n.T(lang, "mn.btn_pass"), "mn:pass")},
		{btn(i18n.T(lang, "mn.btn_name"), "mn:name")},
		navBack(lang, "menu:pay"),
	})
}

func (a *App) onMoyNalogAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.MoyNalog.Enabled = !a.botCfg.MoyNalog.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showMoyNalogAdmin(ctx, chatID)
	case "login":
		a.getUI(chatID).adminInput = "mn_login"
		a.askInput(ctx, chatID, i18n.T(lang, "mn.ask_login"), "menu:moynalog")
	case "pass":
		a.getUI(chatID).adminInput = "mn_pass"
		a.askInput(ctx, chatID, i18n.T(lang, "mn.ask_pass"), "menu:moynalog")
	case "name":
		a.getUI(chatID).adminInput = "mn_name"
		a.askInput(ctx, chatID, i18n.T(lang, "mn.ask_name"), "menu:moynalog")
	}
}

func (a *App) setMoyNalogField(ctx context.Context, chatID int64, field, text string) {
	text = strings.TrimSpace(text)
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "mn_login":
			a.botCfg.MoyNalog.Login = text
		case "mn_pass":
			a.botCfg.MoyNalog.Password = text
		case "mn_name":
			a.botCfg.MoyNalog.ServiceName = text
		}
	}
	a.mu.Unlock()
	a.mnMu.Lock()
	a.mnClient = nil
	a.mnKey = ""
	a.mnMu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showMoyNalogAdmin(ctx, chatID)
}
