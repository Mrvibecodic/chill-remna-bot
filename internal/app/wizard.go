package app

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

type step int

const (
	stepNone step = iota
	stepLang
	stepDB
	stepPGDSN
	stepLocation
	stepInstall
	stepURL
	stepToken
	stepCookie
	stepAPIKeyAsk
	stepAPIKey
)

type wizard struct {
	step step
	cfg  model.BotConfig
}

func (a *App) startWizard(ctx context.Context, chatID int64) {
	a.mu.Lock()
	a.wiz[chatID] = &wizard{step: stepLang}
	a.mu.Unlock()

	a.sendKB(ctx, chatID, i18n.T(i18n.Fallback, "setup.welcome"), [][]models.InlineKeyboardButton{
		{btn("Русский", "lang:ru"), btn("English", "lang:en")},
	})
}

func (a *App) handleCallback(ctx context.Context, cq *models.CallbackQuery) {
	a.msg.AnswerCallback(ctx, cq.ID)
	chatID := cq.From.ID
	isAdmin := chatID == a.cfg.AdminID
	a.rememberUser(ctx, chatID, cq.From.Username, cq.From.FirstName)
	if !isAdmin && a.userBlocked(ctx, chatID) {
		a.send(ctx, chatID, i18n.T(a.lang(chatID), "user.you_blocked"))
		return
	}
	key, val, _ := strings.Cut(cq.Data, ":")

	switch key {
	case "lang", "db", "loc", "inst", "apiprot":
		if !isAdmin {
			return
		}
		a.mu.Lock()
		w := a.wiz[chatID]
		a.mu.Unlock()
		if w == nil {
			return
		}
		a.wizardCallback(ctx, chatID, w, key, val)
	case "menu":
		a.onMenu(ctx, chatID, val, isAdmin, cq.From.FirstName, cq.From.Username)
	case "buy":
		a.onBuyPlan(ctx, chatID, val)
	case "method":
		a.onMethod(ctx, chatID, val)
	case "p2p":
		a.onP2PUser(ctx, chatID, val)
	case "adm":
		if isAdmin {
			a.onAdmin(ctx, chatID, val, cqMsgID(cq))
		}
	case "star":
		if isAdmin {
			a.onStars(ctx, chatID, val)
		}
	case "yk":
		if isAdmin {
			a.onYKAdmin(ctx, chatID, val)
		}
	case "ykc":
		a.onYKCheck(ctx, chatID, val)
	case "cbc":
		a.onCBCheck(ctx, chatID, val)
	case "cb":
		if isAdmin {
			a.onCBAdmin(ctx, chatID, val)
		}
	case "wh":
		if isAdmin {
			a.onWebhooksAdmin(ctx, chatID, val)
		}
	case "ntf":
		if isAdmin {
			a.onNotifyAdmin(ctx, chatID, val)
		}
	case "prc":
		if isAdmin {
			a.onPricing(ctx, chatID, val)
		}
	case "pay":
		if isAdmin {
			a.onPayments(ctx, chatID, val)
		}
	case "emo":
		if isAdmin {
			a.onEmoji(ctx, chatID, val)
		}
	case "wel":
		if isAdmin {
			a.onWelcome(ctx, chatID, val)
		}
	case "usr":
		if isAdmin {
			a.onUsers(ctx, chatID, val)
		}
	case "sq":
		if isAdmin {
			a.onSquad(ctx, chatID, val)
		}
	case "sec":
		if isAdmin {
			a.onSectionBanner(ctx, chatID, val)
		}
	case "subd":
		if isAdmin {
			a.onSubdomain(ctx, chatID, val)
		}
	case "alog":
		if isAdmin {
			a.onAPILog(ctx, chatID, val)
		}
	case "ctc":
		if isAdmin {
			a.onContacts(ctx, chatID, val)
		}
	case "trial":
		// admin sub-actions go to admin handler; user just clicks "menu:trial"
		if isAdmin {
			a.onTrialAdmin(ctx, chatID, val)
		}
	case "sqd":
		if isAdmin {
			a.onSquads(ctx, chatID, val)
		}
	case "terms":
		// «Принимаю/Отказаться» соглашения — может нажать обычный пользователь.
		a.onTerms(ctx, chatID, val, cq.From.FirstName, cq.From.Username)
	case "inp":
		if isAdmin && val == "cancel" {
			a.cancelInput(ctx, chatID, isAdmin, cq.From.FirstName, cq.From.Username)
		}
	case "x":
		// Управление notify-сообщением: «На главную» удаляет уведомление и
		// открывает главный экран (привычная навигация). «close» — legacy,
		// просто удаляет (используется редко, если где-то ещё прицеплен).
		switch val {
		case "home":
			a.msg.Delete(ctx, chatID, cqMsgID(cq))
			a.enterHome(ctx, chatID, isAdmin, cq.From.FirstName, cq.From.Username)
		case "close":
			a.msg.Delete(ctx, chatID, cqMsgID(cq))
		}
	}
}

// cqMsgID — id сообщения, к которому привязана инлайн-кнопка (0, если недоступно).
func cqMsgID(cq *models.CallbackQuery) int {
	if cq.Message.Message != nil {
		return cq.Message.Message.ID
	}
	return 0
}

func (a *App) wizardCallback(ctx context.Context, chatID int64, w *wizard, key, val string) {
	switch key {
	case "lang":
		w.cfg.Language = val
		a.gotoDB(ctx, chatID, w)
	case "db":
		a.onDBChosen(ctx, chatID, w, val)
	case "loc":
		a.onLocationChosen(ctx, chatID, w, val)
	case "inst":
		w.cfg.Panel.InstallType = val
		a.gotoURL(ctx, chatID, w)
	case "apiprot":
		if val == "yes" {
			w.step = stepAPIKey
			a.send(ctx, chatID, i18n.T(w.cfg.Language, "step.apikey.ask"))
		} else {
			a.verify(ctx, chatID, w)
		}
	}
}

func (a *App) handleWizardText(ctx context.Context, chatID int64, text string) {
	a.mu.Lock()
	w := a.wiz[chatID]
	a.mu.Unlock()
	if w == nil {
		return
	}
	switch w.step {
	case stepPGDSN:
		if err := a.openStore(model.DBPostgres, text); err != nil {
			a.send(ctx, chatID, "❌ "+err.Error())
			return
		}
		a.gotoLocation(ctx, chatID, w)
	case stepURL:
		w.cfg.Panel.BaseURL = text
		a.gotoToken(ctx, chatID, w)
	case stepToken:
		w.cfg.Panel.APIToken = text
		a.afterToken(ctx, chatID, w)
	case stepCookie:
		w.cfg.Panel.Cookie = text
		a.verify(ctx, chatID, w)
	case stepAPIKey:
		w.cfg.Panel.APIKey = text
		a.verify(ctx, chatID, w)
	}
}

func (a *App) gotoDB(ctx context.Context, chatID int64, w *wizard) {
	w.step = stepDB
	lang := w.cfg.Language
	a.send(ctx, chatID, i18n.T(lang, "step.db.title"))
	a.sendKB(ctx, chatID, i18n.T(lang, "step.db.body"), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "step.db.choose_sqlite"), "db:sqlite"),
		btn(i18n.T(lang, "step.db.choose_postgres"), "db:postgres"),
	}})
}

func (a *App) onDBChosen(ctx context.Context, chatID int64, w *wizard, kind string) {
	w.cfg.DBKind = kind
	if kind == model.DBSQLite {
		if err := a.openStore(model.DBSQLite, a.dsnForEnv(model.DBSQLite)); err != nil {
			a.send(ctx, chatID, "❌ "+err.Error())
			return
		}
		a.gotoLocation(ctx, chatID, w)
		return
	}

	lang := w.cfg.Language
	if a.ctl != nil && a.ctl.Available() {
		a.send(ctx, chatID, i18n.T(lang, "step.db.pg_starting"))
		dsn, err := a.ctl.EnablePostgres(ctx)
		if err == nil {
			err = a.switchStore(ctx, model.DBPostgres, dsn)
		}
		if err != nil {
			a.send(ctx, chatID, i18n.T(lang, "step.db.pg_failed", err.Error()))
			w.cfg.DBKind = model.DBSQLite
			if a.store == nil {
				if e := a.openStore(model.DBSQLite, a.dsnForEnv(model.DBSQLite)); e != nil {
					a.send(ctx, chatID, "❌ "+e.Error())
					return
				}
			}
			a.gotoLocation(ctx, chatID, w)
			return
		}
		a.send(ctx, chatID, i18n.T(lang, "step.db.pg_ok"))
		a.gotoLocation(ctx, chatID, w)
		return
	}

	if a.cfg.DatabaseURL != "" {
		if err := a.openStore(model.DBPostgres, a.cfg.DatabaseURL); err != nil {
			a.send(ctx, chatID, "❌ "+err.Error())
			return
		}
		a.gotoLocation(ctx, chatID, w)
		return
	}
	w.step = stepPGDSN
	a.send(ctx, chatID, i18n.T(lang, "step.pgdsn.ask"))
}

func (a *App) gotoLocation(ctx context.Context, chatID int64, w *wizard) {
	w.step = stepLocation
	lang := w.cfg.Language
	a.sendKB(ctx, chatID, i18n.T(lang, "step.location.title"), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "step.location.choose_local"), "loc:local"),
		btn(i18n.T(lang, "step.location.choose_remote"), "loc:remote"),
	}})
}

func (a *App) onLocationChosen(ctx context.Context, chatID int64, w *wizard, val string) {
	w.cfg.Panel.Mode = val
	if val == model.ModeLocal {
		w.cfg.Panel.BaseURL = remnawave.LocalBaseURL
		if a.ctl != nil && a.ctl.Available() {
			if err := a.ctl.ConnectPanelNetwork(ctx); err != nil {
				a.log.Warn("подключение к сети панели", "err", err)
			}
		}
		a.gotoToken(ctx, chatID, w)
		return
	}
	w.step = stepInstall
	lang := w.cfg.Language
	a.sendKB(ctx, chatID, i18n.T(lang, "step.install.title"), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "step.install.choose_docs"), "inst:docs"),
		btn(i18n.T(lang, "step.install.choose_egames"), "inst:egames"),
	}})
}

func (a *App) gotoURL(ctx context.Context, chatID int64, w *wizard) {
	w.step = stepURL
	a.send(ctx, chatID, i18n.T(w.cfg.Language, "step.url.ask"))
}

func (a *App) gotoToken(ctx context.Context, chatID int64, w *wizard) {
	w.step = stepToken
	a.send(ctx, chatID, i18n.T(w.cfg.Language, "step.token.ask"))
}

func (a *App) afterToken(ctx context.Context, chatID int64, w *wizard) {
	lang := w.cfg.Language
	if w.cfg.Panel.Mode == model.ModeRemote {
		switch w.cfg.Panel.InstallType {
		case model.InstallEGames:
			w.step = stepCookie
			a.send(ctx, chatID, i18n.T(lang, "step.cookie.ask"))
			return
		case model.InstallDocs:
			w.step = stepAPIKeyAsk
			a.sendKB(ctx, chatID, i18n.T(lang, "step.apikey.ask_protected"), [][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "step.apikey.yes"), "apiprot:yes"),
					btn(i18n.T(lang, "step.apikey.no"), "apiprot:no")},
			})
			return
		}
	}
	a.verify(ctx, chatID, w)
}

func (a *App) verify(ctx context.Context, chatID int64, w *wizard) {
	lang := w.cfg.Language
	a.send(ctx, chatID, i18n.T(lang, "step.verify.checking"))

	client := remnawave.New(w.cfg.Panel)
	if err := client.Health(ctx); err != nil {
		a.send(ctx, chatID, i18n.T(lang, "step.verify.fail", err.Error()))
		return
	}
	count, err := client.SystemStats(ctx)
	if err != nil {
		a.send(ctx, chatID, i18n.T(lang, "step.verify.fail", err.Error()))
		return
	}

	w.cfg.Installed = true
	w.cfg.NormalizePricing()
	w.cfg.NormalizeReminders()
	if a.store == nil {
		a.send(ctx, chatID, i18n.T(lang, "step.verify.fail", "БД не инициализирована"))
		return
	}
	if err := a.store.SaveConfig(ctx, &w.cfg); err != nil {
		a.send(ctx, chatID, i18n.T(lang, "step.verify.fail", err.Error()))
		return
	}

	a.mu.Lock()
	saved := w.cfg
	a.botCfg = &saved
	a.panel = client
	delete(a.wiz, chatID)
	a.mu.Unlock()

	a.send(ctx, chatID, i18n.T(lang, "step.verify.ok", count))
	a.log.Info("установка завершена", "db", a.store.Kind(), "mode", saved.Panel.Mode)
}
