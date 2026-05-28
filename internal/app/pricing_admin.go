package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// askPriceMonth — выбор срока для задания цены; callback "<prefix>:price:<mo>".
func (a *App) askPriceMonth(ctx context.Context, chatID int64, prefix string) {
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", prefix+":price:"+strconv.Itoa(mo)))
	}
	a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price_month"), [][]models.InlineKeyboardButton{row})
}

func (a *App) formatBasePrices() string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := pr.Base[mo]; v != "" {
			parts = append(parts, strconv.Itoa(mo)+"м="+v)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

func (a *App) formatTrafficLimits() string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if gb := pr.Traffic[mo]; gb > 0 {
			parts = append(parts, strconv.Itoa(mo)+"м="+strconv.Itoa(gb)+"GB")
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// showPricing — ТОЛЬКО базовый прайс и валюта (трафик/устройства/стратегия
// вынесены в «Настройки подписки», доступ к ним идёт оттуда).
func (a *App) showPricing(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cur := a.pricing().Currency
	if cur == "" {
		cur = i18n.T(lang, "admin.none")
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "pricing.title", a.formatBasePrices(), cur), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "pricing.btn_base"), "prc:base"), btn(i18n.T(lang, "pricing.btn_cur"), "prc:cur")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onPricing(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "base":
		a.askPriceMonth(ctx, chatID, "prc")
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "baseprice"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "admin.ask_base_price", mo), "menu:pricing")
	case "cur":
		a.getUI(chatID).adminInput = "currency"
		a.askInput(ctx, chatID, i18n.T(lang, "admin.ask_currency"), "menu:pricing")
	case "traffic":
		// «prc:traffic» → выбор месяца → «prc:trafmo:<mo>».
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "prc:trafmo:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_traffic_month"), [][]models.InlineKeyboardButton{row})
	case "trafmo":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "traffic_gb"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "pricing.ask_traffic_gb", mo), "menu:pricing")
	case "devices":
		// 3 кнопки: 1 / 3 устройства / свой лимит. Применяется override per-user
		// в Remnawave (hwidDeviceLimit) для всех создаваемых ботом подписок.
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_devices_preset"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "pricing.dev_1"), "prc:devset:1"),
				btn(i18n.T(lang, "pricing.dev_3"), "prc:devset:3"),
				btn(i18n.T(lang, "pricing.dev_custom"), "prc:devset:custom")},
			{btn(i18n.T(lang, "pricing.dev_default"), "prc:devset:0")},
		})
	case "devset":
		if arg == "custom" {
			ui := a.getUI(chatID)
			ui.adminInput = "device_limit"
			ui.priceMonths = 0
			a.askInput(ctx, chatID, i18n.T(lang, "pricing.ask_devices_custom"), "menu:pricing")
			return
		}
		n, _ := strconv.Atoi(arg)
		a.setDeviceLimitGlobal(n)
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	case "strategy":
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_strategy"), [][]models.InlineKeyboardButton{
			{btn("MONTH", "prc:setstrat:MONTH"), btn("WEEK", "prc:setstrat:WEEK")},
			{btn("DAY", "prc:setstrat:DAY"), btn("NO_RESET", "prc:setstrat:NO_RESET")},
		})
	case "setstrat":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Pricing.TrafficStrategy = arg
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showPricing(ctx, chatID)
	}
}

// setTrafficGB / setDeviceLimit — вызываются из handleAdminText.
func (a *App) setTrafficGB(months, gb int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	if a.botCfg.Pricing.Traffic == nil {
		a.botCfg.Pricing.Traffic = map[int]int{}
	}
	if gb < 0 {
		gb = 0
	}
	a.botCfg.Pricing.Traffic[months] = gb
}

// setDeviceLimitGlobal — общий HWID-override (hwidDeviceLimit) для всех
// подписок, создаваемых ботом. 0 = «не передавать поле», т.е. использовать
// HWID_FALLBACK_DEVICE_LIMIT панели.
func (a *App) setDeviceLimitGlobal(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	if n < 0 {
		n = 0
	}
	a.botCfg.Pricing.DeviceLimit = n
}
