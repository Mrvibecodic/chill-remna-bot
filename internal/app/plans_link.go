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
	b.WriteString(i18n.T(lang, "plans.offer_title", planTitleHTML(lang, p)))
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
	if cs, _ := a.squadCountries(ctx, p.IntSquadsFor(nil)); len(cs) > 0 {
		if line := countriesText(lang, cs); line != "" {
			b.WriteString("\n\n")
			b.WriteString(line)
		}
	}
	b.WriteString("\n\n")
	b.WriteString(i18n.T(lang, "plans.offer_choose"))
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, b.String(), rows)
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
	// ссылкой. Для остальных — обычный гейт доступности.
	if p == nil || !p.Enabled || !planSellsAnything(p) ||
		model.NormalizeAvailability(p.Availability) == model.PlanAvailLink ||
		!a.planAccessibleFor(ctx, p, chatID) {
		a.planLinkFail(chatID)
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
	// доступ отозвать.
	if p == nil || !p.Enabled || !a.planAccessibleFor(ctx, p, chatID) {
		a.planLinkFail(chatID)
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.link_unknown"))
		return
	}
	d := p.Duration(mo)
	if d == nil || d.Base == "" {
		a.planLinkFail(chatID)
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
