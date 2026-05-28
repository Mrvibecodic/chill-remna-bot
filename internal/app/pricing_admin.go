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
	lang := a.lang(chatID)
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", prefix+":price:"+strconv.Itoa(mo)))
	}
	back := "menu:pricing"
	switch prefix {
	case "yk":
		back = "menu:yookassa"
	case "cb":
		back = "menu:cryptobot"
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "admin.ask_price_month"), [][]models.InlineKeyboardButton{row, navBack(lang, back)})
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

// formatDeviceLimits — сводка лимита устройств (HWID): per-tariff значения, если
// заданы; иначе общий DeviceLimit; иначе «дефолт панели».
func (a *App) formatDeviceLimits(lang string) string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if d := pr.Devices[mo]; d > 0 {
			parts = append(parts, strconv.Itoa(mo)+"м="+strconv.Itoa(d))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	if pr.DeviceLimit > 0 {
		return strconv.Itoa(pr.DeviceLimit)
	}
	return i18n.T(lang, "pricing.hwid_default")
}

// showPricing — базовый прайс, валюта и сводка по тарифам (per-month
// цена + трафик), плюс кнопка «🪄 Быстрая настройка тарифа» — пошаговый
// помощник, проставляющий цену и трафик за один проход.
func (a *App) showPricing(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	pr := a.pricing()
	cur := pr.Currency
	if cur == "" {
		cur = i18n.T(lang, "admin.none")
	}
	table := a.formatPlansTable(lang)
	a.sendKB(ctx, chatID, i18n.T(lang, "pricing.title", cur, pr.ResetStrategy(), table), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "pricing.btn_quick"), "prc:quick")},
		{btn(i18n.T(lang, "pricing.btn_base"), "prc:base"), btn(i18n.T(lang, "pricing.btn_cur"), "prc:cur")},
		{btn(i18n.T(lang, "pricing.btn_traffic"), "prc:traffic"), btn(i18n.T(lang, "pricing.btn_devices"), "prc:devices")},
		{btn(i18n.T(lang, "pricing.btn_strategy"), "prc:strategy")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// formatPlansTable — фикс-ширина <pre>-таблицы: «Months | Price | Traffic».
func (a *App) formatPlansTable(lang string) string {
	pr := a.pricing()
	var sb strings.Builder
	sb.WriteString("<pre>")
	sb.WriteString(padRight("Plan", 6) + "  " + padRight("Price", 12) + "  " + padRight("Traffic", 10) + "  HWID\n")
	sb.WriteString(strings.Repeat("─", 40) + "\n")
	for _, mo := range model.PlanMonths {
		price := pr.Base[mo]
		if price == "" {
			price = "—"
		} else {
			price += curSuffix(pr.Currency)
		}
		traffic := i18n.T(lang, "trial.unlimited")
		if gb := pr.Traffic[mo]; gb > 0 {
			traffic = strconv.Itoa(gb) + " GB"
		}
		hwid := "—"
		if d := pr.DeviceLimitFor(mo); d > 0 {
			hwid = strconv.Itoa(d)
		}
		sb.WriteString(padRight(strconv.Itoa(mo)+"m", 6) + "  " + padRight(price, 12) + "  " + padRight(traffic, 10) + "  " + hwid + "\n")
	}
	sb.WriteString("</pre>")
	return sb.String()
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
	case "quick":
		a.startPlanQuick(ctx, chatID)
	case "qmo":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "plan_q_price"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "pricing.q_price", mo), "menu:pricing")
	case "traffic":
		// «prc:traffic» → выбор месяца → «prc:trafmo:<mo>».
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "prc:trafmo:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_traffic_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:pricing")})
	case "trafmo":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "traffic_gb"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "pricing.ask_traffic_gb", mo), "menu:pricing")
	case "devices":
		// Лимит устройств (HWID) — ПО ТАРИФАМ (как трафик): выбор срока → число.
		// Можно дать, например, на годовой тариф больше устройств.
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "prc:devmo:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_devices_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:pricing")})
	case "devmo":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "device_per"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(lang, "pricing.ask_devices", mo), "menu:pricing")
	case "strategy":
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.ask_strategy"), [][]models.InlineKeyboardButton{
			{btn("MONTH", "prc:setstrat:MONTH"), btn("MONTH_ROLLING", "prc:setstrat:MONTH_ROLLING")},
			{btn("WEEK", "prc:setstrat:WEEK"), btn("DAY", "prc:setstrat:DAY")},
			{btn("NO_RESET", "prc:setstrat:NO_RESET")},
			navBack(lang, "menu:pricing"),
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

// setDevicesPer задаёт лимит устройств (HWID) для конкретного срока.
func (a *App) setDevicesPer(months, n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	if a.botCfg.Pricing.Devices == nil {
		a.botCfg.Pricing.Devices = map[int]int{}
	}
	if n < 0 {
		n = 0
	}
	a.botCfg.Pricing.Devices[months] = n
}

// startPlanQuick — быстрая настройка одного тарифа (последовательно
// спрашиваем месяц → цену → трафик; результат сразу применяется).
func (a *App) startPlanQuick(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", "prc:qmo:"+strconv.Itoa(mo)))
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "pricing.q_month"), [][]models.InlineKeyboardButton{
		row,
		{btn(i18n.T(lang, "btn.back"), "menu:pricing"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}
