package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) askPriceMonth(ctx context.Context, chatID int64, prefix string) {
	lang := a.lang(chatID)
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", prefix+":price:"+strconv.Itoa(mo)))
	}
	back := "menu:pricing"
	if prefix == "yk" {
		back = "menu:yookassa"
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "admin.ask_price_month"), [][]models.InlineKeyboardButton{row, navBack(lang, back)})
}

func (a *App) showPlanSquads(ctx context.Context, chatID int64, mo int) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	var actInt, gInt []string
	actExt, gExt := "", ""
	if a.botCfg != nil {
		a.botCfg.NormalizePricing()
		actInt = append([]string(nil), a.botCfg.Pricing.SquadsInt[mo]...)
		actExt = a.botCfg.Pricing.SquadsExt[mo]
		gInt = append([]string(nil), a.botCfg.Plan.ActiveInternalSquads...)
		gExt = a.botCfg.Plan.ExternalSquadUUID
	}
	a.mu.Unlock()
	back := navBack(lang, "prc:squads")
	if panel == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.no_panel"), [][]models.InlineKeyboardButton{back})
		return
	}
	intSquads, _ := panel.ListSquads(ctx)
	extSquads, _ := panel.ListExternalSquads(ctx)
	isActive := func(uuid string) bool {
		for _, u := range actInt {
			if u == uuid {
				return true
			}
		}
		return false
	}
	moStr := strconv.Itoa(mo)
	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+len(extSquads)+3)
	for _, sq := range intSquads {
		mark := "⬜"
		if isActive(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+" 🏠 "+sq.Name, "prc:sqi:"+moStr+":"+sq.UUID)})
	}
	if len(extSquads) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn("— 📡 External —", "prc:sqm:"+moStr)})
		for _, sq := range extSquads {
			mark := "⚪"
			if actExt == sq.UUID {
				mark = "🟢"
			}
			rows = append(rows, []models.InlineKeyboardButton{btn(mark+" 📡 "+sq.Name, "prc:sqe:"+moStr+":"+sq.UUID)})
		}
	}
	if len(actInt) > 0 || actExt != "" {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "pricing.sq_clear"), "prc:sqclear:"+moStr)})
	}
	rows = append(rows, back)
	gIntCSV, gExtName := a.squadNames(ctx, gInt, gExt)
	extState := i18n.T(lang, "admin.none")
	if actExt != "" {
		_, extState = a.squadNames(ctx, nil, actExt)
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "pricing.sq_title", mo, gIntCSV, gExtName, len(actInt), extState), rows)
}

func (a *App) togglePlanSquadInt(mo int, uuid string) {
	if uuid == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	cur := a.botCfg.Pricing.SquadsInt[mo]
	for i, u := range cur {
		if u == uuid {
			next := append(append([]string(nil), cur[:i]...), cur[i+1:]...)
			if len(next) == 0 {
				delete(a.botCfg.Pricing.SquadsInt, mo)
			} else {
				a.botCfg.Pricing.SquadsInt[mo] = next
			}
			return
		}
	}
	a.botCfg.Pricing.SquadsInt[mo] = append(cur, uuid)
}

func (a *App) togglePlanSquadExt(mo int, uuid string) {
	if uuid == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	if a.botCfg.Pricing.SquadsExt[mo] == uuid {
		delete(a.botCfg.Pricing.SquadsExt, mo)
	} else {
		a.botCfg.Pricing.SquadsExt[mo] = uuid
	}
}

func (a *App) clearPlanSquads(mo int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizePricing()
	delete(a.botCfg.Pricing.SquadsInt, mo)
	delete(a.botCfg.Pricing.SquadsExt, mo)
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

func (a *App) showPricing(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	pr := a.pricing()
	table := a.formatPlansTable(lang)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "pricing.title", curRUB, pr.ResetStrategy(), table), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "pricing.btn_quick"), "prc:quick")},
		{btn(i18n.T(lang, "pricing.btn_base"), "prc:base")},
		{btn(i18n.T(lang, "pricing.btn_traffic"), "prc:traffic"), btn(i18n.T(lang, "pricing.btn_devices"), "prc:devices")},
		{btn(i18n.T(lang, "pricing.btn_strategy"), "prc:strategy")},
		{btn(i18n.T(lang, "pricing.btn_squads"), "prc:squads")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

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
	case "squads":
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "prc:sqm:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "pricing.sq_pick_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:pricing")})
	case "sqm":
		mo, _ := strconv.Atoi(arg)
		a.showPlanSquads(ctx, chatID, mo)
	case "sqi":
		moStr, uuid, _ := strings.Cut(arg, ":")
		mo, _ := strconv.Atoi(moStr)
		a.togglePlanSquadInt(mo, uuid)
		_ = a.saveBotConfig(ctx)
		a.showPlanSquads(ctx, chatID, mo)
	case "sqe":
		moStr, uuid, _ := strings.Cut(arg, ":")
		mo, _ := strconv.Atoi(moStr)
		a.togglePlanSquadExt(mo, uuid)
		_ = a.saveBotConfig(ctx)
		a.showPlanSquads(ctx, chatID, mo)
	case "sqclear":
		mo, _ := strconv.Atoi(arg)
		a.clearPlanSquads(mo)
		_ = a.saveBotConfig(ctx)
		a.showPlanSquads(ctx, chatID, mo)
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
			{btn("📅 MONTH", "prc:setstrat:MONTH"), btn("🔁 MONTH_ROLLING", "prc:setstrat:MONTH_ROLLING")},
			{btn("🗓 WEEK", "prc:setstrat:WEEK"), btn("📆 DAY", "prc:setstrat:DAY")},
			{btn("♾ NO_RESET", "prc:setstrat:NO_RESET")},
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
