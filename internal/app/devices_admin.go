package app

import (
	"context"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
)

// showDevicesAdmin — экран «Устройства»: владелец решает, что человек видит про
// свои подключения. Панель знает про каждое устройство отпечаток, платформу,
// версию ОС, модель и две даты; показывать всё подряд незачем, а кому-то нужен
// именно отпечаток — клиенты часто не присылают ни модели, ни платформы.
func (a *App) showDevicesAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.devicesConfig()

	state := i18n.T(lang, "devadm.off")
	toggle := i18n.T(lang, "devadm.btn_on")
	if cfg.List {
		state = i18n.T(lang, "devadm.on")
		toggle = i18n.T(lang, "devadm.btn_off")
	}
	text := i18n.T(lang, "devadm.title", state)
	if cfg.List && !cfg.AnyField() {
		text += i18n.T(lang, "devadm.empty")
	}
	text += i18n.T(lang, "devadm.note")

	rows := [][]models.InlineKeyboardButton{
		{btn(toggle, "devadm:list")},
		{btn(fieldMark(lang, "devadm.f_platform", cfg.Platform), "devadm:platform"),
			btn(fieldMark(lang, "devadm.f_model", cfg.Model), "devadm:model")},
		{btn(fieldMark(lang, "devadm.f_ua", cfg.UA), "devadm:ua"),
			btn(fieldMark(lang, "devadm.f_hwid", cfg.HWID), "devadm:hwid")},
		{btn(fieldMark(lang, "devadm.f_dates", cfg.Dates), "devadm:dates")},
		{btn(i18n.T(lang, "btn.back"), "menu:iface"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	a.sendKBSection(ctx, chatID, assets.SectionMainMenu, text, rows)
}

// fieldMark — подпись тумблера поля с отметкой включённости.
func fieldMark(lang, key string, on bool) string {
	mark := "☐ "
	if on {
		mark = "☑️ "
	}
	return mark + i18n.T(lang, key)
}

// onDevicesAdmin переключает тумблеры экрана «Устройства».
func (a *App) onDevicesAdmin(ctx context.Context, chatID int64, val string) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeDevices()
		d := &a.botCfg.Devices
		switch val {
		case "list":
			d.List = !d.List
		case "platform":
			d.Platform = !d.Platform
		case "model":
			d.Model = !d.Model
		case "ua":
			d.UA = !d.UA
		case "hwid":
			d.HWID = !d.HWID
		case "dates":
			d.Dates = !d.Dates
		}
		d.Init = true
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showDevicesAdmin(ctx, chatID)
}
