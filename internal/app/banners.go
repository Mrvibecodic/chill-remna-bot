package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
)

func (a *App) showSectionBanners(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	rows := make([][]models.InlineKeyboardButton, 0, len(assets.AllSections)+2)
	for _, sec := range assets.UserSections() {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(assets.LabelByKey(sec.Key, lang), "sec:open:"+sec.Key),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:iface"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendIfaceKB(ctx, chatID, i18n.T(lang, "banners.title"), rows)
}

func (a *App) onSectionBanner(ctx context.Context, chatID int64, val string) {
	action, key, _ := cut3(val)
	switch action {
	case "open":
		a.showSectionBanner(ctx, chatID, key)
	case "upload":
		a.askSectionBanner(ctx, chatID, key)
	case "reset":
		a.resetSectionBanner(ctx, chatID, key)
	case "cancel":
		a.getUI(chatID).awaitSectionBanner = ""
		a.showSectionBanner(ctx, chatID, key)
	}
}

func cut3(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

func (a *App) showSectionBanner(ctx context.Context, chatID int64, section string) {
	lang := a.lang(chatID)
	label := assets.LabelByKey(section, lang)
	url := assets.URL(section)
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "banners.btn_upload"), "sec:upload:"+section)},
		{btn(i18n.T(lang, "banners.btn_reset"), "sec:reset:"+section)},
		{btn(i18n.T(lang, "btn.back"), "menu:welcome_sections"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	caption := i18n.T(lang, "banners.section_caption", label)

	if url == "" {

		a.sendKB(ctx, chatID, caption, rows)
		return
	}

	var cached string
	if a.store != nil {
		if id, ok, _ := a.store.LoadMediaFileID(ctx, section); ok {
			cached = id
		}
	}
	var newFileID string
	embed := assets.Bytes(section)
	a.emit(ctx, chatID, func() int {
		id, nf := a.msg.SendPhotoCacheable(ctx, chatID, cached, embed, url, caption, rows)
		newFileID = nf
		return id
	})
	if a.store != nil && newFileID != "" && newFileID != cached {
		_ = a.store.SaveMediaFileID(ctx, section, newFileID)
	}
}

func (a *App) askSectionBanner(ctx context.Context, chatID int64, section string) {
	lang := a.lang(chatID)
	a.getUI(chatID).awaitSectionBanner = section
	label := assets.LabelByKey(section, lang)
	a.sendKB(ctx, chatID, i18n.T(lang, "banners.ask_upload", label), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "sec:cancel:"+section)},
	})
}

func (a *App) setSectionBannerFile(ctx context.Context, chatID int64, section, fileID string) {
	if a.store == nil {
		return
	}
	if err := a.store.SaveMediaFileID(ctx, section, fileID); err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}

	a.showSectionBanner(ctx, chatID, section)
}

func (a *App) resetSectionBanner(ctx context.Context, chatID int64, section string) {
	if a.store == nil {
		return
	}
	if err := a.store.DeleteMediaFileID(ctx, section); err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	a.showSectionBanner(ctx, chatID, section)
}
