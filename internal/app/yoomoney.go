package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
	"remnabot/internal/yoomoney"
)

// ЮMoney — переводы на кошелёк через форму quickpay. У ЮMoney нет «создать
// счёт»: бот сам выдумывает метку (label), кладёт строку pending_invoices и
// ведёт покупателя на свою страницу /webhook/yoomoney/pay/<метка>, которая отправляет
// POST-форму в ЮMoney. Оплату подтверждают HTTP-уведомление (подпись HMAC) и
// обязательная перепроверка по API кошелька operation-history по метке —
// решение о выдаче принимается по ответу API, как у остальных способов.
const (
	ymExtPrefix = "ym:"
	// ymAuthTTL — сколько живёт ссылка «Авторизовать»: код ЮMoney одноразовый
	// и живёт меньше минуты, ссылка — чтобы админ успел дойти до браузера.
	ymAuthTTL = 15 * time.Minute
)

func (a *App) ymConfig() model.YooMoneyConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.YooMoneyConfig{}
	}
	return a.botCfg.YooMoney
}

// ymClient — клиент API кошелька; nil без токена.
func (a *App) ymClient() *yoomoney.Client {
	cfg := a.ymConfig()
	if cfg.Token == "" {
		return nil
	}
	return yoomoney.New(cfg.Token)
}

// publicBaseURL — публичный адрес бота без хвостовой косой черты; пусто, если
// не настроен. Домен с TLS важнее вручную введённого адреса.
func (a *App) publicBaseURL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.publicBaseURLLocked()
}

// publicBaseURLLocked — то же для вызывающих, которые УЖЕ держат a.mu
// (замок не рекурсивный).
func (a *App) publicBaseURLLocked() string {
	base, domain, tls := "", "", false
	if a.botCfg != nil {
		base = a.botCfg.Webhook.PublicBaseURL
		domain = a.botCfg.Webhook.Domain
		tls = a.botCfg.Webhook.TLS
	}
	if tls && domain != "" {
		base = "https://" + domain
	}
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

// ymSellable — способ включён, секреты на месте и есть публичный адрес, на
// котором живёт страница оплаты. Без адреса покупателя вести некуда.
func (a *App) ymSellable() bool {
	cfg := a.ymConfig()
	return cfg.Enabled && cfg.Ready() && a.publicBaseURL() != ""
}

// ymPayURL — страница бота, отправляющая форму quickpay.
func (a *App) ymPayURL(label string) string {
	return a.publicBaseURL() + "/webhook/yoomoney/pay/" + url.PathEscape(label)
}

// ymRedirectURI — куда ЮMoney возвращает код авторизации; этот же адрес
// указывается при регистрации приложения.
func (a *App) ymRedirectURI() string {
	base := a.publicBaseURL()
	if base == "" {
		return ""
	}
	return base + "/webhook/yoomoney/oauth"
}

// ymLabel — метка платежа и одновременно ext_id: префикс ym:, telegram id
// как подсказка для журнала и случайный хвост. Правда о сделке — в строке
// pending_invoices по этой метке.
func ymLabel(chatID int64) string {
	var b [6]byte
	tail := ""
	if _, err := rand.Read(b[:]); err != nil {
		tail = strconv.FormatInt(time.Now().UnixNano(), 36)
	} else {
		tail = hex.EncodeToString(b[:])
	}
	return ymExtPrefix + strconv.FormatInt(chatID, 10) + ":" + tail
}

// ymLabelHint — telegram id из метки (0 — метка не наша).
func ymLabelHint(label string) int64 {
	rest, ok := strings.CutPrefix(label, ymExtPrefix)
	if !ok {
		return 0
	}
	id, _, _ := strings.Cut(rest, ":")
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
}

func (a *App) startYooMoney(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	months := s.Months
	value := a.saleFiat(s, model.PayMethodYooMoney)
	// ЮMoney принимает только рубли: тариф в чужой валюте и нерублёвая сетка
	// не продаются (та же пара проверок, что у Platega).
	if !a.ymConfig().Enabled || value == "" || !a.saleGridCurrency(s) || !a.plGridCurrencyOK() {
		a.sendHome(ctx, chatID, i18n.T(lang, "ym.no_price"))
		return
	}
	if !a.ymSellable() {
		a.sendHome(ctx, chatID, i18n.T(lang, "ym.not_configured"))
		return
	}
	valueK, okV := rubToKopecks(value)
	if !okV || valueK <= 0 {
		a.payLog(ctx, model.PayMethodYooMoney, "", chatID, "invoice_error", "цена %q не разобрана — счёт не выставлен", value)
		a.sendHome(ctx, chatID, i18n.T(lang, "ym.no_price"))
		return
	}
	payURL, label, err := a.ymCreateInvoiceSnap(ctx, chatID, months, valueK, a.saleSnapshot(s))
	if err != nil {
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "ЮMoney", err))
		return
	}
	a.sendKB(ctx, chatID, withPayNote(lang, model.PayMethodYooMoney, i18n.T(lang, "ym.pay_prompt", months, value+curSuffix(curRUB))), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "ym.btn_pay"), URL: payURL}},
		{btn(i18n.T(lang, "ym.btn_check"), "ymc:"+label)},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// ymCreateInvoiceSnap — единое ядро покупки для чата, мини-аппа и кабинета:
// строка счёта с суммой и снимком условий + ссылка на страницу оплаты.
func (a *App) ymCreateInvoiceSnap(ctx context.Context, chatID int64, months int, kopecks int64, snap *model.PlanSnapshot) (payURL, label string, err error) {
	if !a.ymSellable() {
		return "", "", errUserText(i18n.T(a.lang(chatID), "ym.not_configured"))
	}
	if months <= 0 || kopecks <= 0 {
		return "", "", fmt.Errorf("yoomoney: months=%d kopecks=%d", months, kopecks)
	}
	if a.store == nil {
		return "", "", errors.New("yoomoney: хранилище недоступно")
	}
	_ = a.store.UpsertUser(ctx, chatID)
	label = ymLabel(chatID)
	if snap == nil {
		snap = a.planSnapshot(months)
	}
	if err := a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodYooMoney, ExtID: label, TelegramID: chatID, Months: months, Kopecks: kopecks, Snapshot: snap,
	}); err != nil {
		a.payLog(ctx, model.PayMethodYooMoney, label, chatID, "invoice_error", "purchase months=%d: %v", months, err)
		return "", "", fmt.Errorf("шлюз ЮMoney: строка счёта: %w", err)
	}
	a.payLog(ctx, model.PayMethodYooMoney, label, chatID, "invoice_created", "purchase months=%d amount=%s RUB type=%s", months, yoomoney.FormatSum(kopecks), yoomoney.PayTypeOrDefault(a.ymConfig().PayType))
	return a.ymPayURL(label), label, nil
}

// ymCreateTopUp — счёт на пополнение баланса (всегда в рублях).
func (a *App) ymCreateTopUp(ctx context.Context, chatID, kopecks int64) (payURL, label string, err error) {
	if !a.ymSellable() {
		return "", "", errUserText(i18n.T(a.lang(chatID), "ym.not_configured"))
	}
	if kopecks <= 0 {
		return "", "", fmt.Errorf("yoomoney: сумма пополнения %d коп", kopecks)
	}
	if a.store == nil {
		return "", "", errors.New("yoomoney: хранилище недоступно")
	}
	_ = a.store.UpsertUser(ctx, chatID)
	label = ymLabel(chatID)
	if err := a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodYooMoney, ExtID: label, TelegramID: chatID, Purpose: purposeTopUp, Kopecks: kopecks,
	}); err != nil {
		a.payLog(ctx, model.PayMethodYooMoney, label, chatID, "invoice_error", "topup kopecks=%d: %v", kopecks, err)
		return "", "", fmt.Errorf("шлюз ЮMoney: строка счёта: %w", err)
	}
	a.payLog(ctx, model.PayMethodYooMoney, label, chatID, "invoice_created", "topup kopecks=%d amount=%s RUB", kopecks, yoomoney.FormatSum(kopecks))
	return a.ymPayURL(label), label, nil
}

// YooMoneyPayForm — поля формы quickpay для страницы /webhook/yoomoney/pay/<метка>.
// ok=false — счёта нет, он уже оплачен или способ выключен.
func (a *App) YooMoneyPayForm(ctx context.Context, label string) (action string, fields [][2]string, ok bool) {
	cfg := a.ymConfig()
	if a.store == nil || !cfg.Enabled || !cfg.Ready() || !strings.HasPrefix(label, ymExtPrefix) || len(label) > yoomoney.LabelMaxLen {
		return "", nil, false
	}
	p, _ := a.store.PendingByExtID(ctx, label)
	if p == nil || p.Method != model.PayMethodYooMoney || p.Kopecks <= 0 {
		return "", nil, false
	}
	if done, _ := a.store.PaymentByExtID(ctx, label); done {
		return "", nil, false
	}
	success := ""
	if validGatewayReturnURL(strings.TrimSpace(cfg.ReturnURL)) {
		success = strings.TrimSpace(cfg.ReturnURL)
	}
	return yoomoney.FormURL, yoomoney.FormFields(cfg.Receiver, cfg.PayType, p.Kopecks, label, success), true
}

// onYMCheck — кнопка «Проверить оплату»: спрашиваем историю кошелька по метке.
func (a *App) onYMCheck(ctx context.Context, chatID int64, label string) {
	lang := a.lang(chatID)
	if label == "" || !strings.HasPrefix(label, ymExtPrefix) {
		return
	}
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, label); done {
			a.showMySubs(ctx, chatID)
			return
		}
	}
	client := a.ymClient()
	if client == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "ym.not_configured"))
		return
	}
	ops, err := client.OperationsByLabel(ctx, label)
	if err != nil {
		a.ymTokenAlert(ctx, err)
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "ЮMoney", err))
		return
	}
	paid := ymPaidOps(ops, label)
	a.payLog(ctx, model.PayMethodYooMoney, label, chatID, "manual_check", "операций=%d оплаченных=%d", len(ops), len(paid))
	if len(paid) == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "ym.pending"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "ym.btn_check"), "ymc:"+label)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
		return
	}
	// По API видна только зачисленная сумма — проверка с допуском комиссии.
	// Операцию выбираем тем же правилом, что сверка и вебхук.
	p, _ := a.store.PendingByExtID(ctx, label)
	op, extra := a.ymPickOp(paid, p)
	if op == nil {
		op = &paid[len(paid)-1]
	}
	res := a.finalizeYooMoney(ctx, label, op.ID, 0, op.AmountK())
	a.ymReportExtra(ctx, label, extra)
	switch res {
	case ymUnderpaid:
		a.sendHome(ctx, chatID, i18n.T(lang, "ym.underpaid", yoomoney.FormatSum(op.AmountK())+curSuffix(curRUB), yoomoney.FormatSum(p.Kopecks)+curSuffix(curRUB)))
	case ymNoInvoice:
		a.sendHome(ctx, chatID, i18n.T(lang, "ym.no_invoice"))
	}
}

// ymPaidOps — завершённые входящие операции именно с этой меткой, в порядке
// от старых к новым (история приходит новыми вперёд). Метка сверяется и
// здесь, а не только фильтром запроса: чужой перевод на кошелёк подписку
// выдавать не должен.
func ymPaidOps(ops []yoomoney.Operation, label string) []yoomoney.Operation {
	var out []yoomoney.Operation
	for i := len(ops) - 1; i >= 0; i-- {
		if o := ops[i]; o.Paid() && o.Label == label {
			out = append(out, o)
		}
	}
	return out
}

// ymPickOp — операция, которой зачитывается счёт: самая ранняя, покрывающая
// сумму счёта с допуском на комиссию; остальные оплаченные — лишние (повторная
// оплата или недоплата перед полной). Единое правило для кнопки «Проверить»,
// сверки и вебхука — иначе разные пути выбирали бы разные операции и спорили
// бы, что считать дублем. Без строки счёта выбирать не по чему: nil и все.
func (a *App) ymPickOp(paid []yoomoney.Operation, p *model.PendingInvoice) (*yoomoney.Operation, []yoomoney.Operation) {
	if p == nil {
		return nil, paid
	}
	payType := a.ymConfig().PayType
	for i := range paid {
		if ymAmountOK(p.Kopecks, 0, paid[i].AmountK(), payType) {
			extra := append(append([]yoomoney.Operation{}, paid[:i]...), paid[i+1:]...)
			return &paid[i], extra
		}
	}
	return nil, paid
}

// ymReportExtra — лишние оплаченные операции по метке: человек оплатил ту же
// ссылку ещё раз, второй раз не выдаём, деньги возвращает админ руками.
func (a *App) ymReportExtra(ctx context.Context, label string, extra []yoomoney.Operation) {
	for _, o := range extra {
		a.payLog(ctx, model.PayMethodYooMoney, label, ymLabelHint(label), "duplicate_payment", "лишняя оплаченная операция %s на %s RUB по той же метке — не выдано", o.ID, yoomoney.FormatSum(o.AmountK()))
		a.ymNotifyAdmin(ctx, label+":dup:"+o.ID, "ym.admin_duplicate", escapeName(o.ID), yoomoney.FormatSum(o.AmountK()), a.userLabelByID(ctx, ymLabelHint(label)))
	}
}

// ymAmountOK — заплачено не меньше счёта. paidK — сколько отдал покупатель
// (из уведомления, 0 — неизвестно); creditedK — сколько дошло до кошелька.
// Когда известна только зачисленная сумма, к счёту применяется допуск на
// комиссию получателя: 3 % с карты, 1 % с кошелька (по настройке способа).
func ymAmountOK(expectedK, paidK, creditedK int64, payType string) bool {
	if paidK > 0 {
		return paidK >= expectedK
	}
	return creditedK >= expectedK-yoomoney.MaxFee(expectedK, payType)
}

// ymResult — чем закончилась финализация, чтобы вызывающий ответил человеку.
type ymResult int

const (
	ymIssued ymResult = iota
	ymNoInvoice
	ymUnderpaid
	ymFailed
)

// finalizeYooMoney выдаёт подписку или зачисляет баланс по подтверждённой
// операции. Источник правды о сделке — строка счёта по метке; в запись
// платежа идёт цена сделки из неё, а не сумма конкретной операции: иначе
// выручка и процент рефереру зависели бы от того, каким путём дошла оплата
// (уведомление знает уплаченное, API — зачисленное).
func (a *App) finalizeYooMoney(ctx context.Context, label, opID string, paidK, creditedK int64) ymResult {
	if a.store == nil {
		return ymFailed
	}
	p, _ := a.store.PendingByExtID(ctx, label)
	hint := ymLabelHint(label)
	if p == nil {
		a.payLog(ctx, model.PayMethodYooMoney, label, hint, "error", "оплата подтверждена (операция %s, %s RUB), но строки счёта нет — получатель и назначение неизвестны", opID, yoomoney.FormatSum(creditedK))
		a.ymNotifyAdmin(ctx, label+":noinv", "ym.admin_no_invoice", escapeName(opID), yoomoney.FormatSum(creditedK), escapeName(label), a.userLabelByID(ctx, hint))
		return ymNoInvoice
	}
	if !ymAmountOK(p.Kopecks, paidK, creditedK, a.ymConfig().PayType) {
		a.payLog(ctx, model.PayMethodYooMoney, label, p.TelegramID, "underpaid", "счёт на %s RUB, заплачено %s, зачислено %s (операция %s) — не выдано", yoomoney.FormatSum(p.Kopecks), yoomoney.FormatSum(paidK), yoomoney.FormatSum(creditedK), opID)
		shown := creditedK
		if paidK > 0 {
			shown = paidK
		}
		a.ymNotifyAdmin(ctx, label+":under", "ym.admin_underpaid", escapeName(opID), yoomoney.FormatSum(shown), yoomoney.FormatSum(p.Kopecks), a.userLabelByID(ctx, p.TelegramID))
		return ymUnderpaid
	}
	switch {
	case paidK > p.Kopecks:
		a.payLog(ctx, model.PayMethodYooMoney, label, p.TelegramID, "overpaid", "счёт на %s RUB, заплачено %s (операция %s) — излишек на баланс не зачисляется", yoomoney.FormatSum(p.Kopecks), yoomoney.FormatSum(paidK), opID)
	case paidK == 0:
		a.payLog(ctx, model.PayMethodYooMoney, label, p.TelegramID, "api_amount", "выдано по истории кошелька: зачислено %s RUB при счёте %s (операция %s)", yoomoney.FormatSum(creditedK), yoomoney.FormatSum(p.Kopecks), opID)
	}
	amount := yoomoney.FormatSum(p.Kopecks) + " RUB"
	if p.Purpose == purposeTopUp {
		// Счёт гасится ТОЛЬКО после зачисления — иначе сверка его потеряет.
		if err := a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodYooMoney, amount, label); err != nil {
			a.log.Error("yoomoney topup finalize", "err", err, "ext_id", label)
			return ymFailed
		}
		_ = a.store.ResolvePending(ctx, p.ID)
		return ymIssued
	}
	if p.Months <= 0 {
		a.payLog(ctx, model.PayMethodYooMoney, label, p.TelegramID, "error", "в строке счёта нет срока")
		a.noPeriodForPayment(ctx, model.PayMethodYooMoney, label, p.TelegramID)
		return ymFailed
	}
	link, expireAt, err := a.finalizePurchase(ctx, p.TelegramID, p.Months, model.PayMethodYooMoney, amount, label, p.Snapshot)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			return ymIssued
		}
		a.log.Error("yoomoney finalize", "err", err, "ext_id", label)
		return ymFailed
	}
	a.sendSubActive(ctx, p.TelegramID, link, expireAt)
	return ymIssued
}

// ymTokenAlert — токен отозван или протух (401): без него ни одна оплата не
// подтвердится, админа зовём раз в неделю, а не на каждую попытку.
func (a *App) ymTokenAlert(ctx context.Context, err error) {
	if errors.Is(err, yoomoney.ErrInvalidToken) {
		a.ymNotifyAdmin(ctx, "token", "ym.admin_token_bad")
	}
}

// ymNotifyAdmin — уведомление админу раз на (метка, вид).
func (a *App) ymNotifyAdmin(ctx context.Context, key, i18nKey string, args ...any) {
	now := time.Now()
	a.thrMu.Lock()
	if a.ymNotified == nil {
		a.ymNotified = map[string]time.Time{}
	}
	for k, t := range a.ymNotified {
		if now.Sub(t) > 7*24*time.Hour {
			delete(a.ymNotified, k)
		}
	}
	_, dup := a.ymNotified[key]
	a.ymNotified[key] = now
	a.thrMu.Unlock()
	if dup {
		return
	}
	alang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, i18nKey, args...))
}

// ymArmAuth — взводит ожидание OAuth-редиректа и возвращает одноразовое
// состояние ссылки «Авторизовать» (crypto/rand; если он недоступен — метка
// времени, не константа).
func (a *App) ymArmAuth() string {
	var b [16]byte
	state := strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := rand.Read(b[:]); err == nil {
		state = hex.EncodeToString(b[:])
	}
	a.thrMu.Lock()
	a.ymAuthState = state
	a.ymAuthUntil = time.Now().Add(ymAuthTTL)
	a.thrMu.Unlock()
	return state
}

// ymAuthArmed — ожидание взведено и не протухло; при state != "" сверяется
// и состояние ссылки.
func (a *App) ymAuthArmed(state string) bool {
	a.thrMu.Lock()
	defer a.thrMu.Unlock()
	if a.ymAuthState == "" || time.Now().After(a.ymAuthUntil) {
		return false
	}
	return state == "" || state == a.ymAuthState
}

func (a *App) ymDisarmAuth() {
	a.thrMu.Lock()
	a.ymAuthState = ""
	a.thrMu.Unlock()
}

// YooMoneyAuthForm — поля формы запроса разрешения для страницы
// /webhook/yoomoney/auth?s=<state>. Страница отдаётся только по живой ссылке.
func (a *App) YooMoneyAuthForm(state string) (action string, fields [][2]string, ok bool) {
	cfg := a.ymConfig()
	redirect := a.ymRedirectURI()
	if state == "" || !a.ymAuthArmed(state) || cfg.ClientID == "" || redirect == "" {
		return "", nil, false
	}
	return yoomoney.AuthorizeURL, yoomoney.AuthorizeFields(cfg.ClientID, redirect), true
}

// HandleYooMoneyOAuth — редирект ЮMoney с кодом авторизации. Возвращает текст
// для страницы в браузере; итог дублируется админу в чат. Токен сохраняется
// только если принадлежит кошельку-получателю: чужой код (или чужой кошелёк)
// иначе подменил бы кошелёк, по которому проверяются оплаты.
//
// state — состояние ссылки «Авторизовать», которое страница /auth положила
// браузеру в cookie: ЮMoney своего state в редиректе не несёт, а без привязки
// любой GET на этот адрес в 15-минутное окно гасил бы ожидание админа.
// Запрос без верного state ничего не разоружает и админа не беспокоит.
func (a *App) HandleYooMoneyOAuth(ctx context.Context, state, code, errCode string) string {
	alang := a.lang(a.cfg.AdminID)
	if state == "" || !a.ymAuthArmed(state) {
		return i18n.T(alang, "ym.oauth_unexpected")
	}
	a.ymDisarmAuth()
	cfg := a.ymConfig()
	// Тексты страницы в браузере — без HTML (страница их экранирует сама),
	// уведомления в чат — с экранированием под parse_mode=HTML.
	fail := func(reason string) string {
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "ym.admin_oauth_fail", escapeHTMLText(reason)))
		return i18n.T(alang, "ym.oauth_fail", reason)
	}
	if errCode != "" {
		return fail(errCode)
	}
	if code == "" {
		return fail("нет кода")
	}
	if cfg.ClientID == "" || cfg.Receiver == "" {
		return fail("не заданы client_id или номер кошелька")
	}
	token, err := yoomoney.ExchangeCode(ctx, cfg.ClientID, cfg.ClientSecret, a.ymRedirectURI(), code)
	if err != nil {
		a.log.Warn("yoomoney oauth", "err", err)
		return fail(err.Error())
	}
	account, err := yoomoney.New(token).AccountInfo(ctx)
	if err != nil {
		a.log.Warn("yoomoney oauth: account-info", "err", err)
		return fail(err.Error())
	}
	if ymDigits(account) != ymDigits(cfg.Receiver) {
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "ym.admin_oauth_other", escapeName(account), escapeName(cfg.Receiver)))
		return i18n.T(alang, "ym.oauth_mismatch", account, cfg.Receiver)
	}
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.YooMoney.Token = token
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "ym.admin_oauth_ok", escapeName(account)))
	return i18n.T(alang, "ym.oauth_ok", account)
}

// ymDigits — номер кошелька без пробелов и прочих разделителей.
func ymDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validYMReceiver — номер кошелька ЮMoney: 11–20 цифр.
func validYMReceiver(s string) bool {
	d := ymDigits(s)
	return d == s && len(d) >= 11 && len(d) <= 20
}

func (a *App) showYooMoneyAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.ymConfig()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	yesNo := func(set bool) string {
		if set {
			return i18n.T(lang, "admin.yes")
		}
		return i18n.T(lang, "admin.no")
	}
	receiver := cfg.Receiver
	if receiver == "" {
		receiver = i18n.T(lang, "admin.none")
	}
	client := cfg.ClientID
	if client == "" {
		client = i18n.T(lang, "admin.none")
	}
	ret := cfg.ReturnURL
	if ret == "" {
		ret = i18n.T(lang, "admin.none")
	}
	payType := i18n.T(lang, "ym.paytype_card")
	if yoomoney.PayTypeOrDefault(cfg.PayType) == yoomoney.PayTypeWallet {
		payType = i18n.T(lang, "ym.paytype_wallet")
	}
	base := a.publicBaseURL()
	notifyURL := i18n.T(lang, "ym.no_base")
	redirect := notifyURL
	if base != "" {
		notifyURL = base + "/webhook/yoomoney"
		redirect = a.ymRedirectURI()
	}
	text := i18n.T(lang, "ym.title", status, escapeHTMLText(receiver), yesNo(cfg.NotifySecret != ""), yesNo(cfg.Token != ""),
		escapeHTMLText(client), yesNo(cfg.ClientSecret != ""), payType, escapeHTMLText(ret), notifyURL, redirect)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "ym:toggle"), btn(i18n.T(lang, "ym.btn_paytype"), "ym:paytype")},
		{btn(i18n.T(lang, "ym.btn_receiver"), "ym:receiver"), btn(i18n.T(lang, "ym.btn_secret"), "ym:secret")},
		{btn(i18n.T(lang, "ym.btn_client"), "ym:client"), btn(i18n.T(lang, "ym.btn_csecret"), "ym:csecret")},
		{btn(i18n.T(lang, "ym.btn_auth"), "ym:auth"), btn(i18n.T(lang, "ym.btn_token"), "ym:token")},
		{btn(i18n.T(lang, "ym.btn_verify"), "ym:verify"), btn(i18n.T(lang, "ym.btn_return"), "ym:return")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onYooMoneyAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	switch val {
	case "toggle":
		cfg := a.ymConfig()
		// Включить можно только полностью настроенный способ: без секрета
		// уведомления не проверить, без токена — оплату, без кошелька —
		// некуда платить.
		if !cfg.Enabled && !cfg.Ready() {
			a.sendKB(ctx, chatID, i18n.T(lang, "ym.not_ready"), [][]models.InlineKeyboardButton{navBack(lang, "menu:yoomoney")})
			return
		}
		// Перед включением — живая сверка: кошелёк токена обязан совпадать с
		// получателем формы. Иначе деньги приходят на один кошелёк, история
		// читается у другого, и ни одна оплата не подтвердится — молча.
		if !cfg.Enabled {
			if msg := a.ymWalletMismatch(ctx, lang, cfg); msg != "" {
				a.sendKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, "menu:yoomoney")})
				return
			}
		}
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.YooMoney.Enabled = !a.botCfg.YooMoney.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooMoneyAdmin(ctx, chatID)
	case "paytype":
		a.mu.Lock()
		if a.botCfg != nil {
			if yoomoney.PayTypeOrDefault(a.botCfg.YooMoney.PayType) == yoomoney.PayTypeWallet {
				a.botCfg.YooMoney.PayType = yoomoney.PayTypeCard
			} else {
				a.botCfg.YooMoney.PayType = yoomoney.PayTypeWallet
			}
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showYooMoneyAdmin(ctx, chatID)
	case "receiver":
		a.getUI(chatID).adminInput = "ym_receiver"
		a.askInput(ctx, chatID, i18n.T(lang, "ym.ask_receiver"), "menu:yoomoney")
	case "secret":
		a.getUI(chatID).adminInput = "ym_secret"
		a.askInput(ctx, chatID, i18n.T(lang, "ym.ask_secret"), "menu:yoomoney")
	case "client":
		a.getUI(chatID).adminInput = "ym_client"
		a.askInput(ctx, chatID, i18n.T(lang, "ym.ask_client"), "menu:yoomoney")
	case "csecret":
		a.getUI(chatID).adminInput = "ym_csecret"
		a.askInput(ctx, chatID, i18n.T(lang, "ym.ask_csecret"), "menu:yoomoney")
	case "token":
		a.getUI(chatID).adminInput = "ym_token"
		a.askInput(ctx, chatID, i18n.T(lang, "ym.ask_token"), "menu:yoomoney")
	case "return":
		a.getUI(chatID).adminInput = "ym_return"
		a.askInput(ctx, chatID, i18n.T(lang, "ym.ask_return"), "menu:yoomoney")
	case "auth":
		a.ymAuthLink(ctx, chatID)
	case "verify":
		a.ymVerifyToken(ctx, chatID)
	}
}

// ymAuthLink — ссылка «разрешить доступ»: страница бота отправит форму
// авторизации в ЮMoney, а код вернётся на /webhook/yoomoney/oauth.
func (a *App) ymAuthLink(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.ymConfig()
	base := a.publicBaseURL()
	switch {
	case base == "":
		a.sendKB(ctx, chatID, i18n.T(lang, "ym.auth_no_base"), [][]models.InlineKeyboardButton{navBack(lang, "menu:yoomoney")})
		return
	case cfg.ClientID == "" || cfg.Receiver == "":
		a.sendKB(ctx, chatID, i18n.T(lang, "ym.auth_no_client"), [][]models.InlineKeyboardButton{navBack(lang, "menu:yoomoney")})
		return
	}
	state := a.ymArmAuth()
	link := base + "/webhook/yoomoney/auth?s=" + state
	a.sendKB(ctx, chatID, i18n.T(lang, "ym.auth_text", a.ymRedirectURI()), [][]models.InlineKeyboardButton{
		{{Text: i18n.T(lang, "ym.btn_auth_open"), URL: link}},
		navBack(lang, "menu:yoomoney"),
	})
}

// ymWalletCheck — номер кошелька токена и "" , если токен работает и выдан
// кошельку-получателю; иначе готовый текст для админа (нет токена / API
// отказал / чужой кошелёк).
func (a *App) ymWalletCheck(ctx context.Context, lang string, cfg model.YooMoneyConfig) (account, msg string) {
	if cfg.Token == "" {
		return "", i18n.T(lang, "ym.verify_no_token")
	}
	account, err := yoomoney.New(cfg.Token).AccountInfo(ctx)
	if err != nil {
		return "", i18n.T(lang, "ym.verify_fail", escapeHTMLText(err.Error()))
	}
	if ymDigits(account) != ymDigits(cfg.Receiver) {
		return account, i18n.T(lang, "ym.verify_mismatch", escapeHTMLText(account), escapeHTMLText(cfg.Receiver))
	}
	return account, ""
}

func (a *App) ymWalletMismatch(ctx context.Context, lang string, cfg model.YooMoneyConfig) string {
	_, msg := a.ymWalletCheck(ctx, lang, cfg)
	return msg
}

// ymVerifyToken — живая проверка токена: номер кошелька по API и его
// совпадение с получателем.
func (a *App) ymVerifyToken(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.ymConfig()
	account, msg := a.ymWalletCheck(ctx, lang, cfg)
	if msg == "" {
		msg = i18n.T(lang, "ym.verify_ok", escapeHTMLText(account), escapeHTMLText(cfg.Receiver))
	}
	a.sendKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, "menu:yoomoney")})
}

func (a *App) setYooMoneyField(ctx context.Context, chatID int64, field, text string) {
	lang := a.lang(chatID)
	text = strings.TrimSpace(text)
	switch field {
	case "ym_receiver":
		if !validYMReceiver(text) {
			a.sendHome(ctx, chatID, i18n.T(lang, "ym.bad_receiver"))
			return
		}
	case "ym_return":
		if text != "" && !validGatewayReturnURL(text) {
			a.sendHome(ctx, chatID, i18n.T(lang, "pay.return_url_bad"))
			return
		}
	case "ym_secret", "ym_client", "ym_csecret", "ym_token":
		if text == "" || hasInvisible(text) || strings.ContainsAny(text, " \t\n") || len(text) > 512 {
			a.sendHome(ctx, chatID, i18n.T(lang, "ym.bad_value"))
			return
		}
	}
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "ym_receiver":
			a.botCfg.YooMoney.Receiver = text
		case "ym_secret":
			a.botCfg.YooMoney.NotifySecret = text
		case "ym_client":
			a.botCfg.YooMoney.ClientID = text
		case "ym_csecret":
			a.botCfg.YooMoney.ClientSecret = text
		case "ym_token":
			a.botCfg.YooMoney.Token = text
		case "ym_return":
			a.botCfg.YooMoney.ReturnURL = text
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	// Сменили кошелёк или токен — сразу сверяем их между собой: расхождение
	// иначе всплывёт только неподтверждёнными оплатами.
	if field == "ym_receiver" || field == "ym_token" {
		if cfg := a.ymConfig(); cfg.Token != "" {
			if msg := a.ymWalletMismatch(ctx, lang, cfg); msg != "" {
				a.sendKB(ctx, chatID, msg, [][]models.InlineKeyboardButton{navBack(lang, "menu:yoomoney")})
				return
			}
		}
	}
	a.showYooMoneyAdmin(ctx, chatID)
}
