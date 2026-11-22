package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

// Выдача подписки администратором: тариф и срок из витрины, без оплаты.
//
// Выдача — это сделка, а не подарок дней: она идёт через то же ядро, что и
// покупка (зачёт остатка, снимок условий, доп-подписка, автопродление), и
// записывается в платежи способом grant. По этой записи человек дальше сам
// продлевает выданный тариф, а сверка лимитов не откатывает его к прошлой
// покупке. Выручкой и «человек платил» выдача не считается (см. storage).

// grantMaxDays — потолок своего срока в днях.
const grantMaxDays = 3650

// grantSel — что админ выбрал на экранах выдачи. Живёт в памяти: код тарифа в
// callback рядом с id не помещается, а номер сделки (nonce) нельзя отдавать
// кнопке, пока экран подтверждения не показан.
type grantSel struct {
	uid    int64
	code   string
	months int
	days   int
	// nonce рождается при показе подтверждения и входит в ext_id выдачи:
	// повторное нажатие той же кнопки гасит барьер по ext_id.
	nonce string
	// fp — отпечаток условий, показанных на подтверждении: тариф могли
	// поменять, пока экран висел.
	fp      string
	eternal bool
	pack    string
	gb      int
}

var (
	errGrantPlanGone = errors.New("тариф выключен, удалён или срок снят с продажи")
)

// grantRefusal — почему выдать нельзя прямо сейчас ("" — можно).
func (a *App) grantRefusal(ctx context.Context, lang string, uid int64) string {
	if a.panelClient() == nil {
		return i18n.T(lang, "grant.no_panel")
	}
	botBlocked, _, subBlocked := a.userBlockState(ctx, uid)
	if botBlocked || subBlocked {
		return i18n.T(lang, "grant.blocked")
	}
	return ""
}

// plansOnSale — тарифы в продаже: включённые и с продаваемыми сроками, в
// любом режиме доступности. Их же можно выдать.
func (a *App) plansOnSale(ctx context.Context) ([]model.Plan, error) {
	list, err := a.planList(ctx)
	if err != nil {
		return nil, err
	}
	hasBase := false
	for i := range list {
		if list[i].Code == model.PlanCodeBase {
			hasBase = true
		}
	}
	if !hasBase {
		if bp, _ := a.planOrBase(ctx, model.PlanCodeBase); bp != nil {
			list = append([]model.Plan{*bp}, list...)
		}
	}
	out := list[:0]
	for i := range list {
		p := &list[i]
		if p.Enabled && p.KnownKind() && (len(a.grantTerms(p)) > 0 || trafficPlanSells(p)) {
			out = append(out, *p)
		}
	}
	return out, nil
}

// grantTerms — продаваемые сроки тарифа в месяцах, по возрастанию. «Базовый»
// продаётся по сетке конфига — сроки берутся из неё, как у витрины.
func (a *App) grantTerms(p *model.Plan) []int {
	var out []int
	if p.Code == model.PlanCodeBase {
		for m, price := range a.pricing().Base {
			if m > 0 && price != "" {
				out = append(out, m)
			}
		}
	} else {
		for i := range p.Durations {
			if d := &p.Durations[i]; d.Months > 0 && d.Base != "" {
				out = append(out, d.Months)
			}
		}
	}
	sort.Ints(out)
	return out
}

// grantSnapshot снимает условия выдачи. days > 0 — свой срок в днях: условия
// берутся с наименьшего продаваемого срока, стоимость окна — долей его цены
// (как у недельной подписки Tribute), иначе выданные дни при смене тарифа
// зачлись бы как целый срок.
func (a *App) grantSnapshot(ctx context.Context, code string, months, days int) (*model.PlanSnapshot, *model.Plan, error) {
	p, err := a.planOrBase(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	if p == nil || !p.Enabled || !p.KnownKind() {
		return nil, nil, errGrantPlanGone
	}
	if p.IsTraffic() {
		if months != 0 || days != 0 || !trafficPlanSells(p) {
			return nil, nil, errGrantPlanGone
		}
		return a.trafficSnapshotOf(p, nil), p, nil
	}
	terms := a.grantTerms(p)
	if len(terms) == 0 {
		return nil, nil, errGrantPlanGone
	}
	if days > 0 {
		months = terms[0]
	} else if !containsInt(terms, months) {
		return nil, nil, errGrantPlanGone
	}
	var snap *model.PlanSnapshot
	if p.Code == model.PlanCodeBase {
		snap = a.planSnapshot(months)
	} else {
		snap = a.planSnapshotOf(p, p.Duration(months), months)
	}
	if days > 0 {
		snap.Days = days
		snap.Paid = scaledPrice(snap.Price, days, months*30)
	}
	return snap, p, nil
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// grantTermWords — срок для клиента словами: «3 месяца», «10 дней».
func grantTermWords(lang string, months, days int) string {
	if days > 0 {
		return daysWord(lang, days)
	}
	return monthsWord(lang, months)
}

// daysWord — «1 день» / «3 дня» / «10 дней».
func daysWord(lang string, days int) string {
	if lang == model.LangEN {
		if days == 1 {
			return "1 day"
		}
		return strconv.Itoa(days) + " days"
	}
	switch {
	case days%10 == 1 && days%100 != 11:
		return strconv.Itoa(days) + " день"
	case days%10 >= 2 && days%10 <= 4 && (days%100 < 12 || days%100 > 14):
		return strconv.Itoa(days) + " дня"
	default:
		return strconv.Itoa(days) + " дней"
	}
}

// grantTermLabel — «3 мес» или «10 дн.».
func grantTermLabel(lang string, months, days int) string {
	if days > 0 {
		return i18n.T(lang, "grant.days", days)
	}
	return i18n.T(lang, "grant.months", months)
}

// showGrantPlans — шаг 1: выбор тарифа.
func (a *App) showGrantPlans(ctx context.Context, chatID, uid int64) {
	lang := a.lang(chatID)
	back := "usr:view:" + strconv.FormatInt(uid, 10)
	if msg := a.grantRefusal(ctx, lang, uid); msg != "" {
		a.sendUsrKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	st := a.storeRef()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	u, err := st.GetUser(ctx, uid)
	if err != nil || u == nil {
		a.showUsers(ctx, chatID, 0)
		return
	}
	plans, err := a.plansOnSale(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if len(plans) == 0 {
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "grant.no_plans"), [][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	a.getUI(chatID).grant = &grantSel{uid: uid}
	id := strconv.FormatInt(uid, 10)
	var rows [][]models.InlineKeyboardButton
	if row := a.grantPackRow(ctx, lang, uid); row != nil {
		rows = append(rows, row)
	}
	cur := a.pricing().Currency
	for i := range plans {
		p := &plans[i]
		label := planTitle(lang, p)
		switch model.NormalizeAvailability(p.Availability) {
		case model.PlanAvailLink:
			label = "🔗 " + label
		case model.PlanAvailList:
			label = "🎟 " + label
		}
		if p.IsTraffic() {
			label += " · " + i18n.T(lang, "grant.term_eternal")
		} else if price, m := planMinPrice(p); price != "" {
			label += " · " + i18n.T(lang, "buy.from_price", price+curSuffix(planCurrencyOr(p, cur))) + termSuffix(lang, m)
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(label, "usr:grp:"+id+":"+planEditHash(p.Code)),
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), back), btn(i18n.T(lang, "btn.home"), "menu:home")})
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "grant.plans_title", a.userLabelFor(ctx, u), a.grantNowLine(ctx, lang, u)), rows)
}

// grantNowLine — что у человека сейчас: тариф и срок по зеркалу бота.
func (a *App) grantNowLine(ctx context.Context, lang string, u *model.User) string {
	if remnawave.IsEternal(u.SubExpireAt) {
		if t := a.clientPlanTitle(ctx, lang, u.TelegramID); t != "" {
			return i18n.T(lang, "grant.now_eternal", html.EscapeString(t))
		}
		return i18n.T(lang, "grant.term_eternal")
	}
	exp, err := time.Parse(time.RFC3339, u.SubExpireAt)
	if err != nil || !exp.After(time.Now()) {
		return i18n.T(lang, "grant.now_none")
	}
	date := exp.In(displayTZ).Format("02.01.2006")
	if u.NotifyKind == "trial" {
		return i18n.T(lang, "grant.now_trial", date)
	}
	if t := a.clientPlanTitle(ctx, lang, u.TelegramID); t != "" {
		return i18n.T(lang, "grant.now_plan", html.EscapeString(t), date)
	}
	return i18n.T(lang, "grant.now_until", date)
}

// pickGrantPlan — нажат тариф: arg = "<uid>:<отпечаток кода>".
func (a *App) pickGrantPlan(ctx context.Context, chatID int64, arg string) {
	uidStr, hash, _ := strings.Cut(arg, ":")
	uid, _ := strconv.ParseInt(uidStr, 10, 64)
	plans, err := a.plansOnSale(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "err.storage"))
		return
	}
	for i := range plans {
		if planEditHash(plans[i].Code) == hash {
			ui := a.getUI(chatID)
			ui.grant = &grantSel{uid: uid, code: plans[i].Code}
			a.showGrantTerms(ctx, chatID, uid, &plans[i])
			return
		}
	}
	a.showGrantPlans(ctx, chatID, uid)
}

// showGrantTerms — шаг 2: условия тарифа и срок.
func (a *App) showGrantTerms(ctx context.Context, chatID, uid int64, p *model.Plan) {
	if p.IsTraffic() {
		a.showGrantTrafficTerms(ctx, chatID, uid, p)
		return
	}
	lang := a.lang(chatID)
	id := strconv.FormatInt(uid, 10)
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, m := range a.grantTerms(p) {
		row = append(row, btn(grantTermLabel(lang, m, 0), "usr:grt:"+id+":"+strconv.Itoa(m)))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "grant.btn_days"), "usr:grd:"+id)},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "grant.btn_other_plan"), "usr:gr:"+id), btn(i18n.T(lang, "btn.home"), "menu:home")},
	)
	def, defOK := a.hwidDefaults(ctx)
	terms := planTermsText(lang, p, func(d *model.PlanDuration) devShown { return planDevShown(p, d, def, defOK) })
	if terms != "" {
		terms = "\n\n" + terms
	}
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "grant.terms_title", planTitleHTML(lang, p), a.userLabelByID(ctx, uid), terms), rows)
}

// grantSelFor — выбор админа для этого пользователя; nil — его нет (рестарт,
// другой экран, другой человек).
func (a *App) grantSelFor(chatID, uid int64) *grantSel {
	g := a.getUI(chatID).grant
	if g == nil || g.uid != uid || g.code == "" {
		return nil
	}
	return g
}

// grantStale — выбор потерян: говорим об этом и возвращаем в карточку.
func (a *App) grantStale(ctx context.Context, chatID, uid int64) {
	a.noticeThen(ctx, chatID, i18n.T(a.lang(chatID), "grant.stale"), func() { a.showUser(ctx, chatID, uid) })
}

// pickGrantTerm — выбран срок в месяцах: arg = "<uid>:<месяцы>".
func (a *App) pickGrantTerm(ctx context.Context, chatID int64, arg string) {
	uidStr, mStr, _ := strings.Cut(arg, ":")
	uid, _ := strconv.ParseInt(uidStr, 10, 64)
	months, _ := strconv.Atoi(mStr)
	g := a.grantSelFor(chatID, uid)
	if g == nil || months < 0 || g.pack != "" {
		a.grantStale(ctx, chatID, uid)
		return
	}
	p, _ := a.planByCode(ctx, g.code)
	traffic := p.IsTraffic()
	if (months == 0) != traffic {
		a.grantStale(ctx, chatID, uid)
		return
	}
	g.months, g.days, g.eternal = months, 0, traffic
	a.showGrantConfirm(ctx, chatID, uid, "")
}

// askGrantDays — свой срок в днях.
func (a *App) askGrantDays(ctx context.Context, chatID, uid int64) {
	g := a.grantSelFor(chatID, uid)
	if g == nil {
		a.grantStale(ctx, chatID, uid)
		return
	}
	lang := a.lang(chatID)
	name := g.code
	if p, _ := a.planOrBase(ctx, g.code); p != nil {
		name = planTitleHTML(lang, p)
	}
	a.getUI(chatID).adminInput = "grant_days"
	a.askInput(ctx, chatID, i18n.T(lang, "grant.days_ask", name, grantMaxDays),
		"usr:grp:"+strconv.FormatInt(uid, 10)+":"+planEditHash(g.code))
}

// applyGrantDays принимает число дней.
func (a *App) applyGrantDays(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	ui.adminInput = ""
	g := ui.grant
	if g == nil || g.code == "" || g.eternal {
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "grant.stale"))
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || days < 1 || days > grantMaxDays {
		lang := a.lang(chatID)
		ui.adminInput = "grant_days"
		a.askInput(ctx, chatID, i18n.T(lang, "grant.days_bad", grantMaxDays),
			"usr:grp:"+strconv.FormatInt(g.uid, 10)+":"+planEditHash(g.code))
		return
	}
	g.months, g.days = 0, days
	a.showGrantConfirm(ctx, chatID, g.uid, "")
}

// showGrantConfirm — шаг 3: что получится. note — пояснение сверху (например,
// «условия изменились»).
func (a *App) showGrantConfirm(ctx context.Context, chatID, uid int64, note string) {
	lang := a.lang(chatID)
	g := a.grantSelFor(chatID, uid)
	if g == nil || (g.months <= 0 && g.days <= 0 && !g.eternal) {
		a.grantStale(ctx, chatID, uid)
		return
	}
	id := strconv.FormatInt(uid, 10)
	back := "usr:view:" + id
	if msg := a.grantRefusal(ctx, lang, uid); msg != "" {
		a.sendUsrKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, back)})
		return
	}
	snap, p, err := a.grantSnap(ctx, g)
	if errors.Is(err, errGrantPlanGone) {
		a.getUI(chatID).grant = nil
		a.noticeThen(ctx, chatID, i18n.T(lang, "grant.plan_gone"), func() { a.showGrantPlans(ctx, chatID, uid) })
		return
	}
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	st := a.storeRef()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	u, err := st.GetUser(ctx, uid)
	if err != nil || u == nil {
		a.showUsers(ctx, chatID, 0)
		return
	}
	g.nonce = newGrantNonce()
	g.fp = snap.Fingerprint()
	if snap.IsTraffic() {
		a.showGrantConfirmTraffic(ctx, chatID, u, g, snap, p, note)
		return
	}

	var b strings.Builder
	if note != "" {
		b.WriteString(note)
		b.WriteString("\n\n")
	}
	b.WriteString(i18n.T(lang, "grant.confirm_title", a.userLabelFor(ctx, u), planTitleHTML(lang, p),
		grantTermLabel(lang, g.months, g.days), a.grantNowLine(ctx, lang, u)))
	extra := a.switchCreditDays(ctx, uid, snap)
	if extra != 0 && u.Snapshot != nil {
		if exp, err := time.Parse(time.RFC3339, u.SubExpireAt); err == nil {
			left := int(math.Round(time.Until(exp).Hours() / 24))
			b.WriteString("\n")
			b.WriteString(i18n.T(lang, "grant.confirm_switch", left, max(0, left+extra)))
		}
	}
	b.WriteString("\n")
	b.WriteString(i18n.T(lang, "grant.confirm_until", formatExpire(grantEstimate(u.SubExpireAt, g.months, g.days, extra, u.Snapshot.IsTraffic()).Format(time.RFC3339), lang)))
	if lines := a.snapTermsLines(ctx, lang, snap); len(lines) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(lines, "\n"))
	}
	b.WriteString("\n\n")
	b.WriteString(i18n.T(lang, "grant.confirm_note"))
	if g.days == 0 || dayDealResets(u.Snapshot, u.SubExpireAt, snap) {
		b.WriteString("\n")
		b.WriteString(i18n.T(lang, "grant.confirm_reset"))
	}
	for _, w := range a.grantWarnings(ctx, lang, u, p, snap) {
		b.WriteString("\n\n")
		b.WriteString(w)
	}
	a.sendUsrKB(ctx, chatID, b.String(), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "grant.btn_ok"), "usr:grok:"+id+":"+g.nonce)},
		{btn(i18n.T(lang, "grant.btn_other_term"), "usr:grp:"+id+":"+planEditHash(g.code)), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// grantEstimate — ожидаемый конец срока по зеркалу бота, той же формулой, что
// панель-клиент (nextExpire / nextExpireDays). Точную дату назовёт панель.
func grantEstimate(subExpireAt string, months, days, extra int, fromNow bool) time.Time {
	now := time.Now().UTC()
	base := now
	if t, err := time.Parse(time.RFC3339, subExpireAt); err == nil && t.After(base) && !(fromNow && remnawave.IsEternal(subExpireAt)) {
		base = t
	}
	if days > 0 {
		next := base.AddDate(0, 0, days+extra)
		if floor := now.AddDate(0, 0, days); next.Before(floor) {
			next = floor
		}
		return next
	}
	next := base.AddDate(0, months, 0).AddDate(0, 0, extra)
	if floor := now.AddDate(0, months, 0); next.Before(floor) {
		next = floor
	}
	return next
}

// grantWarnings — что админу важно знать до выдачи. Каждое предупреждение
// считается той же проверкой, по которой потом действует выдача.
func (a *App) grantWarnings(ctx context.Context, lang string, u *model.User, p *model.Plan, snap *model.PlanSnapshot) []string {
	var out []string
	if model.NormalizeAvailability(p.Availability) == model.PlanAvailList {
		switch t := a.grantListTarget(ctx, u.TelegramID, p.Code); t.state {
		case grantListNoEmail:
			out = append(out, i18n.T(lang, "grant.warn_list_noemail"))
		case grantListNeed:
			out = append(out, i18n.T(lang, "grant.info_list"))
		}
	}
	if ap := a.getAutoPay(ctx, u.TelegramID); ap != nil && ap.Enabled {
		switch {
		case snap.IsTraffic():
			out = append(out, i18n.T(lang, "grant.warn_autopay_off"))
		case autoPayFollows(ap, snap.Code):
			out = append(out, i18n.T(lang, "grant.warn_autopay", planTitleHTML(lang, p)))
		}
	}
	if st := a.storeRef(); st != nil {
		if last, err := st.LastPaidSubPayment(ctx, u.TelegramID); err == nil && last != nil && last.Method == model.PayMethodTribute {
			out = append(out, i18n.T(lang, "grant.warn_tribute"))
		}
	}
	acc := a.grantAccountOf(ctx, u.TelegramID, p)
	if acc.cabinet != "" {
		out = append(out, i18n.T(lang, acc.cabinet))
	}
	if line := acc.unverifiedLine(lang); line != "" {
		out = append(out, line)
	}
	return out
}

// grantAccount — что мешает аккаунту по почте без Telegram воспользоваться
// выданной подпиской. У аккаунта с Telegram всё это не действует: подписка
// видна в боте.
type grantAccount struct {
	// cabinet — ключ предупреждения «в кабинет не войти» ("" — войти можно).
	cabinet string
	// emailPending — почта не подтверждена, а письма включены: в поле email
	// учётки панели она впишется после подтверждения (см. accountEmailFor).
	emailPending bool
	// renewLocked — с неподтверждённой почтой человек не продлит подписку сам:
	// оплата в кабинете закрыта или тариф «по списку» (список сверяется с
	// подтверждённой почтой).
	renewLocked bool
}

// grantAccountOf считает grantAccount теми же проверками, которыми их потом
// применяют кабинет, панель и гейт тарифа.
func (a *App) grantAccountOf(ctx context.Context, uid int64, p *model.Plan) grantAccount {
	var acc grantAccount
	if uid >= 0 {
		return acc
	}
	switch err := a.cabinetAdmission(ctx, uid, true); {
	case !a.CabinetEnabled():
		acc.cabinet = "grant.warn_cab_off"
	case errors.Is(err, errCabinetDenied):
		acc.cabinet = "grant.warn_cab_denied"
	case errors.Is(err, errCabinetPending):
		acc.cabinet = "grant.warn_cab_pending"
	}
	if a.userEmail(ctx, uid) != "" && a.userAccessEmail(ctx, uid) == "" {
		acc.emailPending = a.MailReady()
		acc.renewLocked = model.NormalizeAvailability(p.Availability) == model.PlanAvailList || a.CabinetMoneyBlocked(ctx, uid)
	}
	return acc
}

// unverifiedLine — строка про неподтверждённую почту ("" — сказать нечего).
func (acc grantAccount) unverifiedLine(lang string) string {
	switch {
	case acc.emailPending && acc.renewLocked:
		return i18n.T(lang, "grant.warn_unverified") + " " + i18n.T(lang, "grant.warn_renew")
	case acc.emailPending:
		return i18n.T(lang, "grant.warn_unverified")
	case acc.renewLocked:
		// Письма выключены, а тариф «по списку»: подтвердить почту нечем.
		return i18n.T(lang, "grant.warn_list_nomail")
	}
	return ""
}

func newGrantNonce() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// grantExtID — номер выдачи в платежах. Префикс уникален среди способов:
// PaymentByExtID ищет без учёта способа.
func grantExtID(uid int64, nonce string) string {
	return "grant:" + strconv.FormatInt(uid, 10) + ":" + nonce
}

// confirmGrant — нажата «Выдать»: arg = "<uid>:<nonce>".
//
// Выдача идёт прямо в обработчике, как блокировка, удаление и привязка к
// панели: нажатия админа обрабатываются по одному, и повтор той же кнопки
// застанет уже записанную выдачу.
func (a *App) confirmGrant(ctx context.Context, chatID int64, arg string) {
	uidStr, nonce, _ := strings.Cut(arg, ":")
	uid, _ := strconv.ParseInt(uidStr, 10, 64)
	lang := a.lang(chatID)
	ext := grantExtID(uid, nonce)
	g := a.grantSelFor(chatID, uid)
	if g == nil || g.nonce == "" || g.nonce != nonce {
		// Кнопка старого экрана. Выдача по ней уже могла пройти — тогда
		// честнее показать карточку, чем «выбор устарел».
		if st := a.storeRef(); st != nil && nonce != "" {
			if done, _ := st.PaymentByExtID(ctx, ext); done {
				a.showUser(ctx, chatID, uid)
				return
			}
		}
		a.grantStale(ctx, chatID, uid)
		return
	}
	if msg := a.grantRefusal(ctx, lang, uid); msg != "" {
		a.sendUsrKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, "usr:view:"+uidStr)})
		return
	}
	snap, p, err := a.grantSnap(ctx, g)
	if errors.Is(err, errGrantPlanGone) {
		a.getUI(chatID).grant = nil
		a.noticeThen(ctx, chatID, i18n.T(lang, "grant.plan_gone"), func() { a.showGrantPlans(ctx, chatID, uid) })
		return
	}
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if snap.Fingerprint() != g.fp || snap.PackGB != g.gb {
		a.showGrantConfirm(ctx, chatID, uid, i18n.T(lang, "grant.changed"))
		return
	}
	sel := *g
	// Выбор отработал при любом исходе: повтор той же кнопки найдёт запись,
	// а после ошибки админ сверяет срок в карточке и начинает заново.
	a.getUI(chatID).grant = nil
	res, err := a.runGrant(ctx, sel, snap, p)
	switch {
	case errors.Is(err, storage.ErrDuplicateExtID):
		a.showUser(ctx, chatID, uid)
	case err != nil:
		// Панель могла применить продление и не успеть ответить — повтор
		// вслепую выдал бы срок дважды. Карточка ниже покажет срок из панели.
		a.log.Warn("выдача подписки", "tg_id", uid, "plan", sel.code, "err", err)
		a.noticeThen(ctx, chatID, i18n.T(lang, "grant.failed", a.errText(err)), func() { a.showUser(ctx, chatID, uid) })
	default:
		a.noticeThen(ctx, chatID, grantReport(lang, p, sel, res), func() { a.showUser(ctx, chatID, uid) })
	}
}

// grantResult — что сделала выдача. Каждый шаг после продления сообщает свой
// исход, и итог админу собирается из него, а не обещается заранее.
type grantResult struct {
	expireAt     string
	autoPayMoved bool
	list         grantListState
	client       grantClientState
	account      grantAccount
}

type grantClientState int

const (
	grantClientSent   grantClientState = iota // сообщение доставлено
	grantClientFailed                         // Telegram не принял сообщение
	grantClientNoChat                         // аккаунт кабинета без Telegram
)

// runGrant — сама выдача: сделка через общее ядро, допуск к тарифу «по
// списку», сообщение клиенту.
func (a *App) runGrant(ctx context.Context, sel grantSel, snap *model.PlanSnapshot, p *model.Plan) (grantResult, error) {
	var r grantResult
	var d dealDone
	var err error
	if snap.IsTraffic() {
		d, err = a.closeTrafficDeal(ctx, sel.uid, model.PayMethodGrant, "—", grantExtID(sel.uid, sel.nonce), snap, true)
	} else {
		d, err = a.closeDeal(ctx, sel.uid, snap.Months, model.PayMethodGrant, "—", grantExtID(sel.uid, sel.nonce), snap, true)
	}
	if err != nil {
		return r, err
	}
	r.expireAt = d.expireAt
	r.autoPayMoved = d.autoPayMoved
	r.list = grantListNone
	if sel.pack == "" && model.NormalizeAvailability(p.Availability) == model.PlanAvailList {
		r.list = a.grantListAccess(ctx, sel.uid, p.Code)
	}
	r.client = a.grantNotifyClient(ctx, sel, p, d.expireAt)
	r.account = a.grantAccountOf(ctx, sel.uid, p)
	return r, nil
}

// grantNotifyClient — сообщение клиенту о выдаче.
func (a *App) grantNotifyClient(ctx context.Context, sel grantSel, p *model.Plan, expireAt string) grantClientState {
	if !hasChat(sel.uid) {
		return grantClientNoChat
	}
	lang := a.lang(sel.uid)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = p.Code
	}
	var text string
	switch {
	case sel.pack != "":
		text = i18n.T(lang, "grant.client_pack", sel.gb)
	case sel.eternal:
		text = i18n.T(lang, "grant.client_traffic", escapeName(name))
	default:
		text = i18n.T(lang, "grant.client", escapeName(name), grantTermWords(lang, sel.months, sel.days), formatExpire(expireAt, lang))
	}
	id := a.notifyKB(ctx, sel.uid, text,
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.mysubs"), "menu:mysubs")}})
	if id == 0 {
		return grantClientFailed
	}
	return grantClientSent
}

// grantReport — итог выдачи администратору.
func grantReport(lang string, p *model.Plan, sel grantSel, r grantResult) string {
	var first string
	switch {
	case sel.pack != "":
		first = i18n.T(lang, "grant.pack_done", sel.gb, planTitleHTML(lang, p))
	case sel.eternal:
		first = i18n.T(lang, "grant.done_traffic", planTitleHTML(lang, p))
	default:
		first = i18n.T(lang, "grant.done", planTitleHTML(lang, p), grantTermLabel(lang, sel.months, sel.days), formatExpire(r.expireAt, lang))
	}
	lines := []string{first}
	switch r.client {
	case grantClientSent:
		lines = append(lines, i18n.T(lang, "grant.res_client_sent"))
	case grantClientFailed:
		lines = append(lines, i18n.T(lang, "grant.res_client_failed"))
	case grantClientNoChat:
		lines = append(lines, i18n.T(lang, "grant.res_client_nochat"))
	}
	switch r.list {
	case grantListAdded:
		lines = append(lines, i18n.T(lang, "grant.res_list_added"))
	case grantListNoEmail:
		lines = append(lines, i18n.T(lang, "grant.res_list_noemail"))
	case grantListFailed:
		lines = append(lines, i18n.T(lang, "grant.res_list_failed"))
	}
	if r.autoPayMoved {
		lines = append(lines, i18n.T(lang, "grant.res_autopay"))
	}
	if r.account.cabinet != "" {
		lines = append(lines, i18n.T(lang, r.account.cabinet))
	}
	if line := r.account.unverifiedLine(lang); line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// grantListState — человек и список тарифа «по списку».
type grantListState int

const (
	grantListNone    grantListState = iota // тариф не «по списку»
	grantListHad                           // уже в списке
	grantListNeed                          // нужно добавить
	grantListNoEmail                       // аккаунт кабинета без почты — добавить некуда
	grantListAdded                         // добавлен
	grantListFailed                        // добавить не удалось
)

// grantListEntry — кого и как класть в список тарифа.
type grantListEntry struct {
	state grantListState
	tg    int64
	email string
}

// grantListTarget — нужен ли человеку допуск к тарифу «по списку» и куда его
// писать. Одна проверка и для экрана подтверждения, и для самой выдачи. Без
// допуска выданный тариф человек не продлит: «Продлить» сочтёт его закрытым.
func (a *App) grantListTarget(ctx context.Context, uid int64, code string) grantListEntry {
	st := a.storeRef()
	if st == nil {
		return grantListEntry{state: grantListFailed}
	}
	email := a.userEmail(ctx, uid)
	tg := uid
	if uid < 0 {
		if email == "" {
			return grantListEntry{state: grantListNoEmail}
		}
		tg = 0
	}
	has, err := st.HasPlanAccess(ctx, code, uid, email)
	switch {
	case err != nil:
		return grantListEntry{state: grantListFailed}
	case has:
		return grantListEntry{state: grantListHad}
	}
	return grantListEntry{state: grantListNeed, tg: tg, email: email}
}

// grantListAccess кладёт человека в список тарифа, если его там нет.
func (a *App) grantListAccess(ctx context.Context, uid int64, code string) grantListState {
	t := a.grantListTarget(ctx, uid, code)
	if t.state != grantListNeed {
		return t.state
	}
	if err := a.grantPlanAccessChecked(ctx, code, t.tg, t.email); err != nil {
		a.log.Warn("выдача: допуск к тарифу по списку", "tg_id", uid, "plan", code, "err", err)
		return grantListFailed
	}
	return grantListAdded
}

// snapTermsLines — условия сделки строками: трафик, устройства, сквады,
// доп-подписка. Общие для подтверждения выдачи и карточки платежа.
func (a *App) snapTermsLines(ctx context.Context, lang string, s *model.PlanSnapshot) []string {
	if s == nil {
		return nil
	}
	var out []string
	if s.TrafficGB > 0 {
		out = append(out, i18n.T(lang, "upay.traffic", i18n.T(lang, "upay.gb", s.TrafficGB)))
	} else {
		out = append(out, i18n.T(lang, "upay.traffic", i18n.T(lang, "upay.unlimited")))
	}
	if s.DeviceLimit > 0 {
		out = append(out, i18n.T(lang, "upay.devices", s.DeviceLimit))
	}
	if len(s.IntSquads) > 0 || s.ExtSquad != "" {
		var intNames, extNames map[string]string
		intKnown, extKnown := false, false
		if panel := a.panelClient(); panel != nil {
			live := panel.LiveSquadsCached(ctx)
			intNames, extNames, intKnown, extKnown = live.Int, live.Ext, live.IntKnown, live.ExtKnown
		}
		name := func(names map[string]string, known bool, uuid string) string {
			if n, ok := names[uuid]; ok && n != "" {
				return html.EscapeString(n)
			}
			short := uuid
			if len(short) > 8 {
				short = short[:8]
			}
			if known {
				return i18n.T(lang, "upay.squad_gone", html.EscapeString(short))
			}
			return html.EscapeString(short)
		}
		if len(s.IntSquads) > 0 {
			parts := make([]string, 0, len(s.IntSquads))
			for _, q := range s.IntSquads {
				parts = append(parts, name(intNames, intKnown, q))
			}
			out = append(out, i18n.T(lang, "upay.squads", strings.Join(parts, ", ")))
		}
		if s.ExtSquad != "" {
			out = append(out, i18n.T(lang, "upay.ext_squad", name(extNames, extKnown, s.ExtSquad)))
		}
	}
	if s.AddSub != nil {
		if *s.AddSub {
			out = append(out, i18n.T(lang, "upay.addsub_yes"))
		} else {
			out = append(out, i18n.T(lang, "upay.addsub_no"))
		}
	}
	return out
}
