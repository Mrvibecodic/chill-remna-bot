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

func (a *App) formatDeviceLimits() string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if d := pr.Devices[mo]; d > 0 {
			parts = append(parts, strconv.Itoa(mo)+"м="+strconv.Itoa(d))
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// showPricing — единый прайс + per-tariff лимиты трафика/устройств + стратегия.
func (a *App) showPricing(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	pr := a.pricing()
	cur := pr.Currency
	if cur == "" {
		cur = i18n.T(lang, "admin.none")
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "pricing.title",
		a.formatBasePrices(), cur,
		a.formatTrafficLimits(), a.formatDeviceLimits(), pr.ResetStrategy(),
	), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "pricing.btn_base"), "prc:base"), btn(i18n.T(lang, "pricing.btn_cur"), "prc:cur")},
		{btn(i18n.T(lang, "pricing.btn_traffic"), "prc:traffic"), btn(i18n.T(lang, "pricing.btn_devices"), "prc:devices")},
		{btn(i18n.T(lang, "pricing.btn_strategy"), "prc:strategy")},
		homeRow(lang),
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
		a.send(ctx, chatID, i18n.T(lang, "admin.ask_base_price", mo))
	case "cur":
		a.getUI(chatID).adminInput = "currency"
		a.send(ctx, chatID, i18n.T(lang, "admin.ask_currency"))
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
		a.send(ctx, chatID, i18n.T(lang, "pricing.ask_traffic_gb", mo))
	case "devices":
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "prc:devmo:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_devices_month"), [][]models.InlineKeyboardButton{row})
	case "devmo":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "device_limit"
		ui.priceMonths = mo
		a.send(ctx, chatID, i18n.T(lang, "pricing.ask_devices", mo))
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

func (a *App) setDeviceLimit(months, n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	if a.botCfg.Pricing.Devices == nil {
		a.botCfg.Pricing.Devices = map[int]int{}
	}
	if n < 0 {
		n = 0
	}
	a.botCfg.Pricing.Devices[months] = n
}
