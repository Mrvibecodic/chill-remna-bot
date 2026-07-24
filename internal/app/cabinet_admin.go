package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// cabinetURL builds the public URL of the web cabinet from the webhook/public
// settings + the configured path, or "" if no public base is set.
func (a *App) cabinetURL() string {
	a.mu.Lock()
	base := ""
	path := "/cabinet/"
	if a.botCfg != nil {
		base = a.botCfg.Webhook.PublicBaseURL
		if base == "" && a.botCfg.Webhook.Domain != "" {
			base = "https://" + a.botCfg.Webhook.Domain
		}
		if a.botCfg.Cabinet.Path != "" {
			path = a.botCfg.Cabinet.Path
		}
	}
	a.mu.Unlock()
	base = normalizeBaseURL(base)
	if base == "" {
		return ""
	}
	return base + path
}

func (a *App) showCabinetAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	on := a.botCfg != nil && a.botCfg.Cabinet.Enabled
	path := "/cabinet/"
	if a.botCfg != nil && a.botCfg.Cabinet.Path != "" {
		path = a.botCfg.Cabinet.Path
	}
	a.mu.Unlock()

	state := i18n.T(lang, "cabinet.off")
	toggle := i18n.T(lang, "cabinet.btn_on")
	if on {
		state = i18n.T(lang, "cabinet.on")
		toggle = i18n.T(lang, "cabinet.btn_off")
	}
	text := i18n.T(lang, "cabinet.title", state, path)
	if url := a.cabinetURL(); url != "" {
		text += "\n\n" + i18n.T(lang, "cabinet.url", url)
	} else {
		text += "\n\n" + i18n.T(lang, "cabinet.no_url")
	}
	text += "\n\n" + i18n.T(lang, "cabinet.steps")

	appr := i18n.T(lang, "cabinet.appr_"+func() string {
		a.mu.Lock()
		m := model.CabinetApprovalOff
		if a.botCfg != nil && a.botCfg.Cabinet.Approval != "" {
			m = a.botCfg.Cabinet.Approval
		}
		a.mu.Unlock()
		return m
	}())
	text += "\n" + i18n.T(lang, "cabinet.approval", appr)
	a.mu.Lock()
	cab := model.CabinetConfig{}
	if a.botCfg != nil {
		cab = a.botCfg.Cabinet
	}
	a.mu.Unlock()
	yn := func(set bool) string {
		if set {
			return i18n.T(lang, "user.yes")
		}
		return i18n.T(lang, "user.no")
	}
	text += "\n" + i18n.T(lang, "cabinet.branding", firstNonEmpty(cab.Title, "—"), yn(cab.Desc != ""), yn(cab.Favicon != ""))
	fpLabel := i18n.T(lang, "cabinet.btn_antifp") + ": " + i18n.T(lang, "cabinet.off")
	if cab.AntiFP {
		fpLabel = i18n.T(lang, "cabinet.btn_antifp") + ": " + i18n.T(lang, "cabinet.on")
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(toggle, "menu:cabtoggle")},
		{btn(i18n.T(lang, "cabinet.btn_path"), "menu:cabpath"), btn(i18n.T(lang, "cabinet.btn_approval"), "menu:cabapprove")},
		{btn(i18n.T(lang, "cabinet.btn_title"), "menu:cabtitle"), btn(i18n.T(lang, "cabinet.btn_desc"), "menu:cabdesc")},
		{btn(i18n.T(lang, "cabinet.btn_favicon"), "menu:cabfav"), btn(fpLabel, "menu:cabfp")},
		{btn(i18n.T(lang, "btn.back"), "menu:system"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	a.sendKBSection(ctx, chatID, assets.SectionAdminStats, text, rows)
}

func (a *App) toggleCabinet(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeCabinet()
		a.botCfg.Cabinet.Enabled = !a.botCfg.Cabinet.Enabled
	}
	on := a.botCfg != nil && a.botCfg.Cabinet.Enabled
	a.mu.Unlock()
	if on {
		a.ensureFlagsAsync(a.bgContext())
	}
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}

func (a *App) setCabinetPath(ctx context.Context, chatID int64, text string) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.Cabinet.Path = strings.TrimSpace(text)
		a.botCfg.NormalizeCabinet()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}

func (a *App) notifyAdminWebRequest(ctx context.Context, userID int64, isEmail bool) {
	lang := a.lang(a.cfg.AdminID)
	id := strconv.FormatInt(userID, 10)
	label := a.userLabelByID(ctx, userID)
	kind := i18n.T(lang, "cabinet.req_tg")
	if isEmail {
		kind = i18n.T(lang, "cabinet.req_email")
		if a.store != nil {
			if wu, _ := a.store.GetWebUserByTgID(ctx, userID); wu != nil {
				label = wu.Email
			}
		}
	}
	a.notifyKB(ctx, a.cfg.AdminID, i18n.T(lang, "cabinet.req", kind, label), [][]models.InlineKeyboardButton{{
		btn(i18n.T(lang, "admin.btn_user_ok"), "adm:wok:"+id),
		btn(i18n.T(lang, "admin.btn_user_no"), "adm:wno:"+id),
	}})
}

func (a *App) adminApproveWebUser(ctx context.Context, adminChat int64, arg string, ok bool) {
	uid, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return
	}
	alang := a.lang(adminChat)
	cabNotifyMu.Lock()
	delete(cabNotified, uid)
	cabNotifyMu.Unlock()
	if !ok {
		// Отказ персистентный: юзер при повторном входе увидит «доступ
		// отклонён», а новые заявки админу слаться не будут (см. CabinetGate).
		if a.store != nil {
			_ = a.store.SetWebDenied(ctx, uid, true)
		}
		if uid > 0 {
			a.notify(ctx, uid, i18n.T(a.lang(uid), "cabinet.denied"))
		}
		a.sendHome(ctx, adminChat, i18n.T(alang, "cabinet.denied_admin"))
		return
	}
	if a.store != nil {
		_ = a.store.SetWebApproved(ctx, uid, true)
		_ = a.store.SetWebDenied(ctx, uid, false)
	}
	if uid > 0 {
		a.notify(ctx, uid, i18n.T(a.lang(uid), "cabinet.approved"))
	}
	a.sendHome(ctx, adminChat, i18n.T(alang, "admin.user_ok_done"))
}

func (a *App) cycleCabinetApproval(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeCabinet()
		switch a.botCfg.Cabinet.Approval {
		case model.CabinetApprovalOff:
			a.botCfg.Cabinet.Approval = model.CabinetApprovalAll
		case model.CabinetApprovalAll:
			a.botCfg.Cabinet.Approval = model.CabinetApprovalTG
		case model.CabinetApprovalTG:
			a.botCfg.Cabinet.Approval = model.CabinetApprovalEmail
		default:
			a.botCfg.Cabinet.Approval = model.CabinetApprovalOff
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func (a *App) toggleCabinetAntiFP(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeCabinet()
		a.botCfg.Cabinet.AntiFP = !a.botCfg.Cabinet.AntiFP
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}

func (a *App) setCabinetField(ctx context.Context, chatID int64, field, text string) {
	v := strings.TrimSpace(text)
	if v == "-" {
		v = ""
	}
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "title":
			a.botCfg.Cabinet.Title = v
		case "desc":
			a.botCfg.Cabinet.Desc = v
		case "favicon":
			a.botCfg.Cabinet.Favicon = v
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showCabinetAdmin(ctx, chatID)
}
