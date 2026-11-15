package app

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Платежи одного пользователя: список из карточки и карточка платежа с
// условиями сделки. CSV на телефоне читать неудобно, а сумма без тарифа не
// говорит, за что заплачено.

// userPaysPageSize — платежей на странице: каждый — отдельная кнопка.
const userPaysPageSize = 10

// userPaysTotalsLimit — сколько последних платежей человека складывать в
// итоги. У живого человека их десятки; потолок — от записи, раздутой ошибкой.
const userPaysTotalsLimit = 5000

// payKind — что это за строка платежей.
type payKind int

const (
	payKindSub     payKind = iota // покупка подписки
	payKindTopUp                  // пополнение баланса
	payKindTrial                  // запись о пробном периоде
	payKindGrant                  // выдано администратором
	payKindBalance                // покупка с баланса
)

func payKindOf(p *model.Payment) payKind {
	switch {
	case p.Method == model.PayMethodTrial:
		return payKindTrial
	case p.Method == model.PayMethodGrant:
		return payKindGrant
	case p.Months <= 0:
		return payKindTopUp
	case p.Method == model.PayMethodBalance:
		return payKindBalance
	}
	return payKindSub
}

// payStamp — время записи платежа по Москве.
func payStamp(raw string) (time.Time, bool) {
	t, ok := parseStamp(raw)
	if !ok {
		return time.Time{}, false
	}
	return t.In(displayTZ), true
}

// payTermLabel — срок сделки: дни, если они известны точнее месяцев.
func payTermLabel(lang string, p *model.Payment) string {
	if p.Snapshot != nil && p.Snapshot.Days > 0 {
		return i18n.T(lang, "grant.days", p.Snapshot.Days)
	}
	return i18n.T(lang, "grant.months", p.Months)
}

// payPlanName — имя тарифа сделки: текущее, если тариф жив (переименование
// видно сразу), иначе то, под которым купили. "" — тариф не записан.
func (a *App) payPlanName(ctx context.Context, lang string, s *model.PlanSnapshot, cache map[string]string) string {
	code := planCodeOf(s)
	if code == "" {
		return ""
	}
	if n, ok := cache[code]; ok {
		return n
	}
	name := strings.TrimSpace(s.Name)
	if p, err := a.planOrBase(ctx, code); err == nil && p != nil {
		name = planTitle(lang, p)
	}
	if name == "" {
		name = code
	}
	cache[code] = name
	return name
}

func payStatusIcon(status string) string {
	switch status {
	case model.PaymentRejected:
		return "❌"
	case model.PaymentRefunded:
		return "↩️"
	}
	return "✅"
}

// userPayLabel — подпись кнопки платежа в списке (обычный текст, без разметки).
func (a *App) userPayLabel(ctx context.Context, lang string, p *model.Payment, cache map[string]string) string {
	date := "—"
	if t, ok := payStamp(p.CreatedAt); ok {
		date = t.Format("02.01.06")
	}
	parts := []string{date}
	icon := payStatusIcon(p.Status)
	switch payKindOf(p) {
	case payKindTrial:
		return "🆓 " + date + " · " + i18n.T(lang, "upay.row_trial")
	case payKindTopUp:
		if p.Status == model.PaymentPaid {
			icon = "💳"
		}
		parts = append(parts, i18n.T(lang, "upay.row_topup"), p.Amount)
		return icon + " " + strings.Join(parts, " · ")
	case payKindGrant:
		icon = "🎁"
	}
	if name := a.payPlanName(ctx, lang, p.Snapshot, cache); name != "" {
		parts = append(parts, name)
	}
	parts = append(parts, payTermLabel(lang, p))
	switch payKindOf(p) {
	case payKindGrant:
		parts = append(parts, i18n.T(lang, "upay.row_grant"))
	case payKindBalance:
		parts = append(parts, i18n.T(lang, "upay.row_balance"))
	default:
		parts = append(parts, p.Amount)
	}
	return icon + " " + strings.Join(parts, " · ")
}

// userPayTotals — сколько человек заплатил и сколько ему вернули. Оплата с
// баланса не складывается: эти деньги уже пришли пополнением.
func userPayTotals(ps []model.Payment) (paid, refunded string, hasBalance bool) {
	var paidList, refList []model.Payment
	for i := range ps {
		p := &ps[i]
		switch payKindOf(p) {
		case payKindTrial, payKindGrant:
			continue
		case payKindBalance:
			hasBalance = true
			continue
		}
		switch p.Status {
		case model.PaymentPaid:
			paidList = append(paidList, *p)
		case model.PaymentRefunded:
			refList = append(refList, *p)
		}
	}
	_, paid = paymentTotals(paidList)
	_, refunded = paymentTotals(refList)
	return paid, refunded, hasBalance
}

// userPayCount — сколько записей у человека в платежах (0 при ошибке).
func (a *App) userPayCount(ctx context.Context, uid int64) int {
	st := a.storeRef()
	if st == nil {
		return 0
	}
	_, total, err := st.ListUserPayments(ctx, uid, 1, 0)
	if err != nil {
		return 0
	}
	return total
}

// showUserPayments — список платежей человека: usr:pays:<uid>:<стр>.
func (a *App) showUserPayments(ctx context.Context, chatID, uid int64, page int) {
	lang := a.lang(chatID)
	st := a.storeRef()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	id := strconv.FormatInt(uid, 10)
	back := []models.InlineKeyboardButton{btn(i18n.T(lang, "upay.btn_card"), "usr:view:"+id), btn(i18n.T(lang, "btn.home"), "menu:home")}
	label := a.userLabelByID(ctx, uid)
	all, total, err := st.ListUserPayments(ctx, uid, userPaysTotalsLimit, 0)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if total == 0 {
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "upay.empty", label), [][]models.InlineKeyboardButton{back})
		return
	}
	pages := (total + userPaysPageSize - 1) / userPaysPageSize
	page = max(0, min(page, pages-1))
	items, _, err := st.ListUserPayments(ctx, uid, userPaysPageSize, page*userPaysPageSize)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}

	var b strings.Builder
	b.WriteString(i18n.T(lang, "upay.title", label, total, page+1, pages))
	paid, refunded, hasBalance := userPayTotals(all)
	if paid == "" {
		paid = "—"
	}
	b.WriteString("\n")
	b.WriteString(i18n.T(lang, "upay.paid", html.EscapeString(paid)))
	if refunded != "" {
		b.WriteString(" · ")
		b.WriteString(i18n.T(lang, "upay.refunded", html.EscapeString(refunded)))
	}
	if hasBalance {
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "upay.balance_note"))
	}
	b.WriteString("\n\n")
	b.WriteString(i18n.T(lang, "upay.hint"))

	cache := map[string]string{}
	var rows [][]models.InlineKeyboardButton
	for i := range items {
		p := &items[i]
		rows = append(rows, []models.InlineKeyboardButton{
			btn(a.userPayLabel(ctx, lang, p, cache), "usr:pay:"+strconv.FormatInt(p.ID, 10)+":"+strconv.Itoa(page)),
		})
	}
	if nav := paginationRow("usr:pays:"+id+":", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, back)
	a.sendUsrKB(ctx, chatID, b.String(), rows)
}

// showUserPayment — карточка платежа: usr:pay:<id платежа>:<стр списка>.
func (a *App) showUserPayment(ctx context.Context, chatID, pid int64, page int) {
	lang := a.lang(chatID)
	st := a.storeRef()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	p, err := st.GetPayment(ctx, pid)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if p == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "admin.not_found"))
		return
	}
	kind := payKindOf(p)
	date := "—"
	if t, ok := payStamp(p.CreatedAt); ok {
		date = t.Format("02.01.2006 15:04") + " " + i18n.T(lang, "upay.msk")
	}
	var lines []string
	titleKey := "upay.card_title"
	if kind == payKindGrant {
		titleKey = "upay.card_title_grant"
	}
	lines = append(lines, i18n.T(lang, titleKey, date, a.userLabelByID(ctx, p.TelegramID)), "")
	switch kind {
	case payKindTrial:
		lines = append(lines, i18n.T(lang, "upay.row_trial_full"))
	case payKindGrant:
		lines = append(lines, i18n.T(lang, "upay.grant_line"))
	default:
		lines = append(lines, i18n.T(lang, "upay.status", i18n.T(lang, payStatusKey(p.Status))))
		lines = append(lines, i18n.T(lang, "upay.method", html.EscapeString(methodLabel(p.Method))))
		if p.Amount != "" {
			lines = append(lines, i18n.T(lang, "upay.amount", html.EscapeString(p.Amount)))
		}
	}
	if kind == payKindTopUp {
		lines = append(lines, i18n.T(lang, "upay.row_topup_full"))
	}
	if kind != payKindTrial && kind != payKindTopUp {
		lines = append(lines, i18n.T(lang, "upay.term", payTermLabel(lang, p)))
		if p.Snapshot != nil && p.Snapshot.SwitchDays != 0 {
			lines = append(lines, i18n.T(lang, "upay.switch", p.Snapshot.SwitchDays))
		}
		lines = append(lines, "")
		if name := a.payPlanName(ctx, lang, p.Snapshot, map[string]string{}); name != "" {
			lines = append(lines, i18n.T(lang, "upay.plan", html.EscapeString(name)))
			lines = append(lines, a.snapTermsLines(ctx, lang, p.Snapshot)...)
		} else {
			lines = append(lines, i18n.T(lang, "upay.plan_none"))
		}
	}
	switch {
	case p.Comment == "remnashop":
		lines = append(lines, "", i18n.T(lang, "upay.imported"))
	case p.Comment != "":
		lines = append(lines, "", i18n.T(lang, "upay.comment", html.EscapeString(p.Comment)))
	}
	if p.ExtID != "" {
		lines = append(lines, "", i18n.T(lang, "upay.ext", html.EscapeString(p.ExtID)))
	}
	uid := strconv.FormatInt(p.TelegramID, 10)
	a.sendUsrKB(ctx, chatID, strings.Join(lines, "\n"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "upay.btn_list"), "usr:pays:"+uid+":"+strconv.Itoa(page)), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func payStatusKey(status string) string {
	switch status {
	case model.PaymentRejected:
		return "payments.st_rejected"
	case model.PaymentRefunded:
		return "payments.st_refunded"
	}
	return "payments.st_paid"
}

// startPayUserSearch — «👤 Платежи пользователя» из общей истории платежей.
func (a *App) startPayUserSearch(ctx context.Context, chatID int64) {
	a.getUI(chatID).adminInput = "pay_user"
	a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "upay.ask"), "menu:payments")
}

// applyPayUserSearch — найти человека и открыть его платежи. Один найденный —
// сразу список, несколько — выбор.
func (a *App) applyPayUserSearch(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	ui.adminInput = ""
	lang := a.lang(chatID)
	st := a.storeRef()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	q := strings.TrimSpace(text)
	if id, err := strconv.ParseInt(q, 10, 64); err == nil && id != 0 {
		if u, _ := st.GetUser(ctx, id); u != nil {
			a.showUserPayments(ctx, chatID, id, 0)
			return
		}
	}
	users, total, err := st.SearchUsers(ctx, strings.TrimPrefix(q, "@"), usersPageSize, 0)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	back := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:payments"), btn(i18n.T(lang, "btn.home"), "menu:home")}
	switch {
	case total == 0:
		a.sendPayKB(ctx, chatID, i18n.T(lang, "upay.not_found", html.EscapeString(q)), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "paylog.btn_user"), "pay:user")}, back,
		})
	case total == 1 && len(users) == 1:
		a.showUserPayments(ctx, chatID, users[0].TelegramID, 0)
	default:
		var rows [][]models.InlineKeyboardButton
		for _, u := range users {
			rows = append(rows, []models.InlineKeyboardButton{
				btn("👤 "+userLabelWith(&u, func(s string) string { return s }), "usr:pays:"+strconv.FormatInt(u.TelegramID, 10)+":0"),
			})
		}
		rows = append(rows, back)
		a.sendPayKB(ctx, chatID, i18n.T(lang, "upay.pick", total), rows)
	}
}
