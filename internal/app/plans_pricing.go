package app

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Редактор цен, лимитов и сквадов в карточке тарифа. Экраны общие для всех
// тарифов, включая «Базовый»: пишут они через те же плановые сеттеры, что и
// старые экраны («Цены и лимиты», Stars, ЮKassa, P2P, Сквады), поэтому две
// поверхности не спорят — под ними один путь записи.
//
// Сроки пока ограничены сеткой model.PlanMonths: витрина, фискализация и
// маппинги платёжек считают именно эти четыре срока; произвольные длительности
// приедут вместе с новой витриной.

// planEditHash — короткий отпечаток кода тарифа для callback'ов, куда сам код
// не влезает: у тумблера сквада внутри 36 символов UUID, а лимит Telegram — 64
// байта на всю строку. Код тумблер берёт из ui.planEdit, отпечаток же ловит
// нажатие на СТАРОМ сообщении, когда админ уже редактирует другой тариф:
// молча переключить сквад не тому тарифу — хуже, чем отказать.
func planEditHash(code string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(code))
	return fmt.Sprintf("%08x", h.Sum32())
}

// planForEdit читает тариф для экрана редактора; пустой код — «Базовый».
func (a *App) planForEdit(ctx context.Context, chatID int64, code string) *model.Plan {
	if code == "" {
		code = model.PlanCodeBase
	}
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.planEditFailed(ctx, chatID, code, planGoneOr(err))
		return nil
	}
	return p
}

// showPlanPricing — коммерческий экран тарифа: сроки с ценами, валюта,
// стратегия, лимит устройств и сквады уровня тарифа.
func (a *App) showPlanPricing(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	a.getUI(chatID).planEdit = p.Code

	var rows [][]models.InlineKeyboardButton
	for _, mo := range planEditorMonths(p) {
		label := "—"
		if d := p.Duration(mo); d != nil && d.Base != "" {
			label = d.Base + curSuffix(p.Currency)
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.month_btn", mo, label), "pln:prm:"+strconv.Itoa(mo)+":"+p.Code),
		})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.btn_currency"), "pln:cur:"+p.Code),
			btn(i18n.T(lang, "pricing.btn_strategy"), "pln:str:"+p.Code),
		},
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.btn_devlimit"), "pln:dvl:"+p.Code),
			btn(i18n.T(lang, "plans.btn_plan_squads"), "pln:sqm:0:"+p.Code),
		},
		navBack(lang, "pln:open:"+p.Code),
	)

	cur := strings.TrimSpace(p.Currency)
	if cur == "" {
		cur = i18n.T(lang, "admin.none")
	}
	devlim := i18n.T(lang, "pricing.hwid_default")
	if p.DeviceLimit > 0 {
		devlim = strconv.Itoa(p.DeviceLimit)
	}
	squads := strconv.Itoa(len(p.IntSquads))
	if p.ExtSquad != "" {
		squads += " + 1"
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.pr_title",
		planTitleHTML(lang, p), html_(cur), p.Strategy, devlim, squads), rows)
}

// showPlanMonth — экран одной длительности: цены по способам, трафик,
// устройства и сквады этого срока.
func (a *App) showPlanMonth(ctx context.Context, chatID int64, code string, mo int) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !monthEditable(p, mo) {
		a.showPlanPricing(ctx, chatID, code)
		return
	}
	a.getUI(chatID).planEdit = p.Code
	d := p.Duration(mo)

	price := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "—"
		}
		// Валюту тоже вводит человек — экранируется вместе со значением.
		return html_(v + curSuffix(p.Currency))
	}
	base, p2p, yk := "", "", ""
	stars := 0
	traffic := i18n.T(lang, "plans.inherit")
	devices := i18n.T(lang, "plans.inherit")
	squads := i18n.T(lang, "plans.inherit")
	if d != nil {
		base, p2p, yk, stars = d.Base, d.P2P, d.YooKassa, d.Stars
		if d.TrafficGB != nil {
			traffic = strconv.Itoa(*d.TrafficGB) + " GB"
			if *d.TrafficGB == 0 {
				traffic = i18n.T(lang, "trial.unlimited")
			}
		}
		if d.DeviceLimit != nil && *d.DeviceLimit > 0 {
			devices = strconv.Itoa(*d.DeviceLimit)
		}
		if d.IntSquads != nil || d.ExtSquad != nil {
			n := 0
			if d.IntSquads != nil {
				n = len(*d.IntSquads)
			}
			sq := strconv.Itoa(n)
			if d.ExtSquad != nil && *d.ExtSquad != "" {
				sq += " + 1"
			}
			squads = sq
		}
	}
	starsStr := "—"
	if stars > 0 {
		starsStr = strconv.Itoa(stars) + " ⭐"
	}

	moStr := strconv.Itoa(mo)
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "plans.btn_base_price"), "pln:in:b:"+moStr+":"+p.Code),
			btn(i18n.T(lang, "plans.btn_stars_price"), "pln:in:s:"+moStr+":"+p.Code)},
		{btn(i18n.T(lang, "plans.btn_p2p_price"), "pln:in:p:"+moStr+":"+p.Code),
			btn(i18n.T(lang, "plans.btn_yk_price"), "pln:in:y:"+moStr+":"+p.Code)},
		{btn(i18n.T(lang, "plans.btn_traffic"), "pln:in:t:"+moStr+":"+p.Code),
			btn(i18n.T(lang, "plans.btn_devices"), "pln:in:d:"+moStr+":"+p.Code)},
		{btn(i18n.T(lang, "plans.btn_month_squads"), "pln:sqm:"+moStr+":"+p.Code)},
		navBack(lang, "pln:pr:"+p.Code),
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.prm_title",
		planTitleHTML(lang, p), mo, price(base), price(p2p), price(yk), starsStr,
		traffic, devices, squads), rows)
}

// monthAllowed ограничивает НОВЫЕ сроки редактора текущей сеткой: витрина,
// фискализация и маппинги платёжек считают ровно эти четыре срока.
func monthAllowed(mo int) bool {
	for _, m := range model.PlanMonths {
		if m == mo {
			return true
		}
	}
	return false
}

// monthEditable — срок доступен в редакторе: стандартная сетка ИЛИ срок, уже
// существующий у тарифа. Второе — про легаси-месяцы (импорт, старые версии):
// перенос сетки в тариф сохраняет их, по ним продлеваются автосписания, и
// редактор обязан уметь их показать и поправить.
func monthEditable(p *model.Plan, mo int) bool {
	return monthAllowed(mo) || (p != nil && p.Duration(mo) != nil)
}

// planEditorMonths — сроки для экрана тарифа: стандартная четвёрка плюс
// легаси-месяцы, уже существующие у тарифа.
func planEditorMonths(p *model.Plan) []int {
	out := append([]int(nil), model.PlanMonths...)
	for i := range p.Durations {
		mo := p.Durations[i].Months
		if mo > 0 && !monthAllowed(mo) {
			out = append(out, mo)
		}
	}
	sort.Ints(out)
	return out
}

// planPriceInputs — какое поле спрашивает какой ввод.
var planPriceInputs = map[string]struct {
	input string
	key   string
}{
	"b": {"baseprice", "plans.ask_base_price"},
	"p": {"price", "plans.ask_p2p_price"},
	"y": {"ykprice", "plans.ask_yk_price"},
	"s": {"starprice", "plans.ask_stars_price"},
	"t": {"traffic_gb", "pricing.ask_traffic_gb"},
	"d": {"device_per", "pricing.ask_devices"},
}

// askPlanPriceInput взводит ввод значения для длительности тарифа.
func (a *App) askPlanPriceInput(ctx context.Context, chatID int64, kind string, mo int, code string) {
	spec, ok := planPriceInputs[kind]
	if !ok || code == "" {
		a.showPlanPricing(ctx, chatID, code)
		return
	}
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !monthEditable(p, mo) {
		a.showPlanPricing(ctx, chatID, code)
		return
	}
	ui := a.getUI(chatID)
	// Незакрытые ожидания текста других экранов перехватили бы ответ раньше
	// (см. askPlanText) — снимаем их и здесь.
	ui.welcomeAwait = ""
	ui.awaitEmojiFor = ""
	ui.awaitTopUp = false
	ui.rejectReq = 0
	ui.adminInput = spec.input
	ui.priceMonths = mo
	ui.planCode = code
	a.askInput(ctx, chatID, i18n.T(a.lang(chatID), spec.key, mo), "pln:prm:"+strconv.Itoa(mo)+":"+code)
}

// askPlanValue взводит ввод значения уровня тарифа (валюта, лимит устройств).
func (a *App) askPlanValue(ctx context.Context, chatID int64, code, input, key string) {
	if code == "" {
		a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
		return
	}
	ui := a.getUI(chatID)
	ui.welcomeAwait = ""
	ui.awaitEmojiFor = ""
	ui.awaitTopUp = false
	ui.rejectReq = 0
	ui.adminInput = input
	ui.priceMonths = 0
	ui.planCode = code
	a.askInput(ctx, chatID, i18n.T(a.lang(chatID), key), "pln:pr:"+code)
}

// errPlanPriceInvalid — цена не разобрана как число.
var errPlanPriceInvalid = errors.New("цена не число")

// planInputFailed сообщает админу, что правка цены не применилась.
func (a *App) planInputFailed(ctx context.Context, chatID int64, err error) {
	lang := a.lang(chatID)
	if err == nil {
		return
	}
	if errors.Is(err, errPlanGone) {
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.gone"))
		return
	}
	if errors.Is(err, errPlanPriceInvalid) {
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.price_invalid"))
		return
	}
	a.log.Warn("цена тарифа не сохранена", "err", err)
	a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
}

// afterPlanPriceEdit возвращает админа на экран, с которого он правил: правка
// без контекста тарифа (старые prc:-кнопки, быстрая настройка) ведёт в
// редактор цен «Базового», иначе — на экран редактора того же тарифа.
func (a *App) afterPlanPriceEdit(ctx context.Context, chatID int64, code string, mo int) {
	switch {
	case code == "":
		a.showPlanPricing(ctx, chatID, model.PlanCodeBase)
	case mo > 0:
		a.showPlanMonth(ctx, chatID, code, mo)
	default:
		a.showPlanPricing(ctx, chatID, code)
	}
}

// showPlanSquadEditor — сквады тарифа (mo == 0) или переопределение
// длительности (mo > 0): списки из панели, отметки — текущий выбор тарифа.
func (a *App) showPlanSquadEditor(ctx context.Context, chatID int64, code string, mo int) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if mo != 0 && !monthEditable(p, mo) {
		a.showPlanPricing(ctx, chatID, code)
		return
	}
	a.getUI(chatID).planEdit = p.Code

	back := "pln:pr:" + p.Code
	if mo > 0 {
		back = "pln:prm:" + strconv.Itoa(mo) + ":" + p.Code
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	if panel == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.no_panel"),
			[][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	intSquads, err := panel.ListSquads(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.err", err.Error()),
			[][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	extSquads, _ := panel.ListExternalSquads(ctx)

	// Текущий выбор: у длительности — её переопределение (пусто = наследует
	// уровень тарифа), у тарифа — его собственный набор.
	var selInt []string
	selExt := ""
	inherited := false
	if mo == 0 {
		selInt, selExt = p.IntSquads, p.ExtSquad
	} else if d := p.Duration(mo); d != nil && (d.IntSquads != nil || d.ExtSquad != nil) {
		if d.IntSquads != nil {
			selInt = *d.IntSquads
		}
		if d.ExtSquad != nil {
			selExt = *d.ExtSquad
		}
	} else {
		inherited = true
	}
	isSel := func(uuid string) bool {
		for _, u := range selInt {
			if u == uuid {
				return true
			}
		}
		return false
	}

	h := planEditHash(p.Code)
	moStr := strconv.Itoa(mo)
	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+len(extSquads)+3)
	for _, sq := range intSquads {
		mark := "⬜"
		if isSel(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark+" 🏠 "+sq.Name, "plq:i:"+moStr+":"+h+":"+sq.UUID),
		})
	}
	for _, sq := range extSquads {
		mark := "⬜"
		if selExt == sq.UUID {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark+" 📡 "+sq.Name, "plq:e:"+moStr+":"+h+":"+sq.UUID),
		})
	}
	if mo > 0 {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "pricing.sq_clear"), "pln:sqc:"+moStr+":"+p.Code),
		})
	}
	rows = append(rows, navBack(lang, back))

	title := i18n.T(lang, "plans.sq_plan_title", planTitleHTML(lang, p))
	if mo > 0 {
		state := i18n.T(lang, "plans.sq_override")
		if inherited {
			state = i18n.T(lang, "plans.sq_inherited")
		}
		title = i18n.T(lang, "plans.sq_month_title", planTitleHTML(lang, p), mo, state)
	}
	a.sendPayKB(ctx, chatID, title, rows)
}

// onPlanSquadToggle — нажатие тумблера сквада: plq:<i|e>:<mo>:<hash>:<uuid>.
// Код тарифа берётся из состояния экрана, отпечаток защищает от нажатия на
// старом сообщении, когда админ уже редактирует другой тариф.
func (a *App) onPlanSquadToggle(ctx context.Context, chatID int64, val string) {
	// Нажатие тумблера — уход с экрана ввода: взведённый вопрос снимается, как
	// при любой другой навигации по тарифам.
	a.forgetPlanInput(chatID)
	parts := strings.SplitN(val, ":", 4)
	if len(parts) != 4 {
		return
	}
	kind, moStr, h, uuid := parts[0], parts[1], parts[2], parts[3]
	mo, _ := strconv.Atoi(moStr)
	code := a.getUI(chatID).planEdit
	if code == "" || planEditHash(code) != h {
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "plans.sq_stale"))
		return
	}
	// Месяц из callback-данных не принимается на веру: подделанный или
	// оставшийся от прежней сетки месяц завёл бы длительность-призрак.
	if mo != 0 {
		p, err := a.planByCode(ctx, code)
		if err != nil || p == nil || !monthEditable(p, mo) {
			a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "plans.sq_stale"))
			return
		}
	}
	if err := a.togglePlanSquad(ctx, code, mo, uuid, kind == "e"); err != nil {
		a.planInputFailed(ctx, chatID, err)
		return
	}
	a.showPlanSquadEditor(ctx, chatID, code, mo)
}

// html_ — короткий алиас экранирования для экранов тарифов: значения вводит
// человек, а экраны уходят с разметкой.
func html_(s string) string { return html.EscapeString(s) }

// showPlanStrategy — выбор стратегии сброса трафика для тарифа.
func (a *App) showPlanStrategy(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	a.getUI(chatID).planEdit = p.Code
	a.sendPayKB(ctx, chatID, i18n.T(lang, "pricing.ask_strategy"), [][]models.InlineKeyboardButton{
		{btn("📅 MONTH", "pln:sts:MONTH:"+p.Code), btn("🔁 MONTH_ROLLING", "pln:sts:MONTH_ROLLING:"+p.Code)},
		{btn("🗓 WEEK", "pln:sts:WEEK:"+p.Code), btn("📆 DAY", "pln:sts:DAY:"+p.Code)},
		{btn("♾ NO_RESET", "pln:sts:NO_RESET:"+p.Code)},
		navBack(lang, "pln:pr:"+p.Code),
	})
}
