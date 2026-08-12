package app

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Тариф по прямой ссылке: t.me/<бот>?start=plan_<код>.
//
// Ссылка — единственная дверь к тарифу в режиме «по ссылке» и удобная дверь к
// любому другому доступному тарифу. Код неперебираем (12 знаков из 27-буквенного
// алфавита), но перебор всё равно троттлится: неудачные попытки открыть тариф
// считаются на человека, и после лимита бот замолкает на окно.

const (
	// planLinkFailLimit / planLinkFailWindow — сколько неудачных попыток
	// открыть тариф по коду разрешено в окно. Лимит щедрый для человека с
	// опечаткой в ссылке и тесный для перебора.
	planLinkFailLimit  = 5
	planLinkFailWindow = 10 * time.Minute
)

// planLinkThrottled — превышен ли лимит неудачных попыток.
func (a *App) planLinkThrottled(chatID int64) bool {
	now := time.Now()
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	fails := a.planLinkFails[chatID]
	kept := fails[:0]
	for _, t := range fails {
		if now.Sub(t) < planLinkFailWindow {
			kept = append(kept, t)
		}
	}
	if a.planLinkFails != nil {
		a.planLinkFails[chatID] = kept
	}
	return len(kept) >= planLinkFailLimit
}

// planLinkFail фиксирует неудачную попытку.
func (a *App) planLinkFail(chatID int64) {
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	if a.planLinkFails == nil {
		a.planLinkFails = map[int64][]time.Time{}
	}
	a.planLinkFails[chatID] = append(a.planLinkFails[chatID], time.Now())
}

// planLink — прямая ссылка на тариф ("" — имя бота неизвестно).
func (a *App) planLink(ctx context.Context, code string) string {
	u := a.botUsername(ctx)
	if u == "" {
		return ""
	}
	return "https://t.me/" + u + "?start=plan_" + code
}

// openPlanLink обрабатывает /start plan_<код>.
//
// Ответ на «не нашли» ровно один — «тариф недоступен»: несуществующий код,
// выключенный тариф и тариф, недоступный этому покупателю, снаружи выглядят
// одинаково, иначе ссылки перебором подтверждали бы существование тарифов.
func (a *App) openPlanLink(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	if a.planLinkThrottled(chatID) {
		// Лимит перебора: молчим. Легитимный человек с опечаткой уже пять раз
		// видел «тариф недоступен» — тишина здесь понятнее, чем шестое.
		a.log.Warn("ссылка на тариф: лимит попыток", "user", chatID)
		return
	}
	if !model.ValidPlanCode(code) {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if p == nil || !p.Enabled || !a.planAccessibleFor(ctx, p, chatID) {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	a.showPlanOffer(ctx, chatID, p)
}

// offerView — как показать карточку тарифа: с плашкой (продление), с «назад
// к списку» (пришли из витрины) и с кнопкой «выбрать другой тариф».
type offerView struct {
	// note — строка над карточкой (например, «условия изменились»).
	note string
	// backToList — кнопка «назад» к списку тарифов.
	backToList bool
	// switchPlan — кнопка «выбрать другой тариф» (продление).
	switchPlan bool
}

// showPlanOffer — экран одного тарифа: описание и кнопки сроков с ценами.
func (a *App) showPlanOffer(ctx context.Context, chatID int64, p *model.Plan) {
	a.showPlanOfferView(ctx, chatID, p, offerView{})
}

func (a *App) showPlanOfferView(ctx context.Context, chatID int64, p *model.Plan, view offerView) {
	lang := a.lang(chatID)
	if a.trialLockNotice(ctx, chatID) {
		return
	}
	if text, need := a.termsRequired(ctx, chatID); need {
		// После «Принимаю» человека вернёт на этот же экран (см. onTerms):
		// пришедший по ссылке на скрытый тариф не должен потерять его на
		// витрине «Базового».
		a.getUI(chatID).pendingPlanOffer = p.Code
		a.askTerms(ctx, chatID, text)
		return
	}
	a.getUI(chatID).pendingPlanOffer = ""

	var rows [][]models.InlineKeyboardButton
	cur := planCurrencyOr(p, a.pricing().Currency)
	for i := range p.Durations {
		d := &p.Durations[i]
		if d.Months <= 0 || d.Base == "" {
			continue
		}
		label := i18n.T(lang, "buy.plan_btn", d.Months, d.Base+curSuffix(cur))
		rows = append(rows, []models.InlineKeyboardButton{
			btn(label, "plb:"+p.Code+":"+strconv.Itoa(d.Months)),
		})
	}
	if len(rows) == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "buy.no_plans"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	if view.switchPlan {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "buy.btn_switch_plan"), "menu:buy")})
	}
	if view.backToList {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:buy"), btn(i18n.T(lang, "btn.home"), "menu:home")})
	} else {
		rows = append(rows, homeRow(lang))
	}

	var b strings.Builder
	if view.note != "" {
		b.WriteString(view.note)
		b.WriteString("\n\n")
	}
	if a.store != nil {
		// «Чаще всего выбирают» — только если такой срок есть у ЭТОГО тарифа:
		// подпись по чужому сроку читалась бы как сбой.
		if months, total, err := a.store.MostPopularPlan(ctx); err == nil && months > 0 && total >= popularThreshold {
			if d := p.Duration(months); d != nil && d.Base != "" {
				b.WriteString(i18n.T(lang, "buy.popular", months))
				b.WriteString("\n\n")
			}
		}
	}
	b.WriteString(i18n.T(lang, "plans.offer_title", planCardTitleHTML(lang, p)))
	if desc := strings.TrimSpace(p.Description); desc != "" {
		b.WriteString("\n\n")
		b.WriteString(html.EscapeString(desc))
	}
	// Опция доп-подписки: покупатель видит её только там, где она продаётся.
	if a.planAddSubOn(p) {
		name, desc := a.addSubTexts(lang, p)
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "buy.addsub_line", html.EscapeString(name)))
		if desc != "" {
			b.WriteString("\n")
			b.WriteString(html.EscapeString(desc))
		}
	}
	// Смена тарифа: остаток не сгорает — говорим об этом прямо на карточке.
	// Точный сдвиг в днях (по соотношению цен) покажет экран способов оплаты:
	// он зависит от выбранного срока.
	if a.switchingFrom(ctx, chatID, p.Code) != "" {
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "buy.switch_note"))
	}
	if cs, _ := a.squadCountries(ctx, p.IntSquadsFor(nil)); len(cs) > 0 {
		if line := countriesText(lang, cs); line != "" {
			b.WriteString("\n\n")
			b.WriteString(line)
		}
	}
	if terms := planTermsText(lang, p); terms != "" {
		b.WriteString("\n\n")
		b.WriteString(terms)
	}
	b.WriteString("\n\n")
	b.WriteString(i18n.T(lang, "plans.offer_choose"))
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, b.String(), rows)
}

// planCardTitleHTML — заголовок карточки тарифа для покупателя: значок,
// заданный в админке, а без него — дефолтная коробка.
func planCardTitleHTML(lang string, p *model.Plan) string {
	if strings.TrimSpace(p.Icon) != "" {
		return planTitleHTML(lang, p)
	}
	cp := *p
	cp.Icon = "📦"
	return planTitleHTML(lang, &cp)
}

// planTermsText — условия тарифа для карточки покупателя: трафик и устройства.
// Значения одинаковы у всех продаваемых сроков — одной строкой; различаются —
// с разбивкой по срокам. Лимит устройств 0 означает дефолт панели — это число
// боту неизвестно, поэтому тариф без единого своего лимита строку устройств
// не показывает.
func planTermsText(lang string, p *model.Plan) string {
	type cond struct{ months, traffic, devices int }
	var conds []cond
	for i := range p.Durations {
		d := &p.Durations[i]
		if d.Months <= 0 || d.Base == "" {
			continue
		}
		conds = append(conds, cond{d.Months, p.TrafficGBFor(d), p.DeviceLimitFor(d)})
	}
	if len(conds) == 0 {
		return ""
	}
	sameTraffic, sameDevices, anyDevices := true, true, false
	for _, c := range conds {
		if c.traffic != conds[0].traffic {
			sameTraffic = false
		}
		if c.devices != conds[0].devices {
			sameDevices = false
		}
		if c.devices > 0 {
			anyDevices = true
		}
	}
	var b strings.Builder
	if sameTraffic {
		b.WriteString(i18n.T(lang, "buy.traffic", trafficValue(lang, p.Strategy, conds[0].traffic)))
	} else {
		parts := make([]string, 0, len(conds))
		for _, c := range conds {
			parts = append(parts, i18n.T(lang, "buy.per_months", c.months, trafficValue(lang, p.Strategy, c.traffic)))
		}
		b.WriteString(i18n.T(lang, "buy.traffic", strings.Join(parts, " · ")))
	}
	if anyDevices {
		b.WriteString("\n")
		if sameDevices {
			b.WriteString(i18n.T(lang, "buy.devices", devicesValue(lang, conds[0].devices)))
		} else {
			parts := make([]string, 0, len(conds))
			for _, c := range conds {
				parts = append(parts, i18n.T(lang, "buy.per_months", c.months, devicesValue(lang, c.devices)))
			}
			b.WriteString(i18n.T(lang, "buy.devices", strings.Join(parts, " · ")))
		}
	}
	return b.String()
}

// trafficValue — «безлимит» или «N ГБ …» с периодом сброса трафика.
func trafficValue(lang, strategy string, gb int) string {
	if gb <= 0 {
		return i18n.T(lang, "trial.unlimited")
	}
	// Незнакомую строку панель считает MONTH (см. model.ValidStrategy) — здесь
	// то же самое; MONTH_ROLLING для покупателя — тот же «в месяц».
	key := "buy.tr_month"
	switch strategy {
	case "NO_RESET":
		key = "buy.tr_total"
	case "DAY":
		key = "buy.tr_day"
	case "WEEK":
		key = "buy.tr_week"
	}
	return i18n.T(lang, key, gb)
}

// devicesValue — лимит устройств; 0 — дефолт панели, числа у бота нет.
func devicesValue(lang string, n int) string {
	if n <= 0 {
		return i18n.T(lang, "buy.dev_default")
	}
	return i18n.T(lang, "buy.dev_n", n)
}

// onPlanView — карточка тарифа из списка витрины: plo:<код>. Тот же троттлинг
// и тот же единый отказ, что у ссылки: callback подделываем, а список — не
// допуск к скрытому.
func (a *App) onPlanView(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	if a.planLinkThrottled(chatID) {
		a.log.Warn("карточка тарифа: лимит попыток", "user", chatID)
		return
	}
	if !model.ValidPlanCode(code) {
		a.planLinkFail(chatID)
		a.showPlans(ctx, chatID)
		return
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if p == nil && code == model.PlanCodeBase {
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	// Тариф «по ссылке» с витрины не открывается: список — не обладание
	// ссылкой. Несуществующий код и скрытый «по ссылке» похожи на перебор —
	// считаются в лимит.
	if p == nil || model.NormalizeAvailability(p.Availability) == model.PlanAvailLink {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	// Существующий тариф, закрытый покупателю, — обычно устаревшая кнопка
	// витрины, а не перебор: отказ БЕЗ счётчика, иначе пять нажатий на старое
	// сообщение молча выключали бы все кнопки тарифов на окно троттлинга.
	if !p.Enabled || !planSellsAnything(p) || !a.planAccessibleFor(ctx, p, chatID) {
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	a.showPlanOfferView(ctx, chatID, p, offerView{backToList: true})
}

// onPlanBuy — нажатие срока на экране тарифа: plb:<код>:<месяцы>.
//
// Callback-данные подделываемы, поэтому здесь тот же троттлинг, что у ссылки:
// без него plb был бы неметеным оракулом перебора коротких кодов (админ и
// импорт могут завести код в 3 символа, а не только неперебираемый
// сгенерированный).
func (a *App) onPlanBuy(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	if a.planLinkThrottled(chatID) {
		a.log.Warn("кнопка тарифа: лимит попыток", "user", chatID)
		return
	}
	code, moStr, _ := strings.Cut(val, ":")
	mo, err := strconv.Atoi(moStr)
	if err != nil || mo <= 0 || !model.ValidPlanCode(code) {
		a.planLinkFail(chatID)
		a.showPlans(ctx, chatID)
		return
	}
	p, perr := a.planByCode(ctx, code)
	if perr != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if p == nil && code == model.PlanCodeBase {
		// «Базовый» без строки — сбой стартовой синхронизации: продаёт сетка,
		// и кнопки его карточки обязаны работать (та же логика, что в
		// editPlanPricing).
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	// Вторая точка гейта: создание намерения. Кнопка могла пролежать в
	// переписке сколько угодно — тариф успели выключить, срок снять с продажи,
	// доступ отозвать. Неизвестный код — похоже на перебор, считается в лимит;
	// существующий, но закрытый тариф — устаревшая кнопка, отказ без счётчика
	// (см. onPlanView).
	if p == nil {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	d := p.Duration(mo)
	if !p.Enabled || !a.planAccessibleFor(ctx, p, chatID) || d == nil || d.Base == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	if a.trialLockNotice(ctx, chatID) {
		return
	}
	if text, need := a.termsRequired(ctx, chatID); need {
		a.getUI(chatID).pendingPlanOffer = p.Code
		a.askTerms(ctx, chatID, text)
		return
	}
	// Не записалось — дальше не идём: экран способов подписан ценами, и
	// показать его после несостоявшейся записи значит продать прошлый выбор.
	if err := a.setBuyIntent(ctx, chatID, p.Code, mo); err != nil {
		a.log.Warn("намерение покупки не сохранено", "err", err, "user", chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.showMethodsSale(ctx, chatID, &sale{Plan: p, D: d, Months: mo})
}
