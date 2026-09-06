package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

func rubToKopecks(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
	if s == "" {
		return 0, false
	}
	whole, frac := s, "0"
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" {
		whole = "0"
	}
	for len(frac) < 2 {
		frac += "0"
	}
	frac = frac[:2]
	w, e1 := strconv.ParseInt(whole, 10, 64)
	f, e2 := strconv.ParseInt(frac, 10, 64)
	if e1 != nil || e2 != nil || w < 0 || f < 0 {
		return 0, false
	}
	return w*100 + f, true
}

func kopecksToRub(k int64) string {
	if k%100 == 0 {
		return strconv.FormatInt(k/100, 10)
	}
	return fmt.Sprintf("%d.%02d", k/100, k%100)
}

func (a *App) userBalance(ctx context.Context, chatID int64) int64 {
	if a.store == nil {
		return 0
	}
	u, _ := a.store.GetUser(ctx, chatID)
	if u == nil {
		return 0
	}
	return u.Balance
}

func (a *App) showBalance(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	bal := a.userBalance(ctx, chatID)
	table, best, planLine := a.balanceForecast(ctx, chatID, lang, bal)
	caption := i18n.T(lang, "balance.head", kopecksToRub(bal))
	if table != "" {
		hdr := i18n.T(lang, "balance.forecast_hdr")
		if planLine != "" {
			hdr = i18n.T(lang, "balance.forecast_plan", planLine)
		}
		caption += "\n\n" + hdr + "\n" + table
		if best > 0 {
			caption += "\n" + i18n.T(lang, "balance.max_months", best)
		}
	}
	caption += "\n\n" + i18n.T(lang, "balance.autopay_note")
	// Про вывод говорим прямо и всегда: баланс тратится только внутри бота.
	caption += "\n" + i18n.T(lang, "balance.no_withdraw")
	topRow := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.buy"), "menu:buy")}
	if a.topUpEnabled() {
		topRow = append([]models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")}, topRow...)
	}
	a.sendPayKB(ctx, chatID, caption, [][]models.InlineKeyboardButton{
		topRow,
		{btn(i18n.T(lang, "btn.promo"), "pr:enter")},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// balanceForecast — таблица «на сколько хватит баланса». Считает по тарифу
// покупателя (снимок последней сделки): прогноз по чужой сетке обещал бы
// человеку не его цены. Без своего тарифа — а также для «Базового»,
// выключенного, удалённого или тарифа в чужой валюте — по сетке, как раньше.
// Третье значение — подпись тарифа для заголовка ("" — прогноз по сетке).
func (a *App) balanceForecast(ctx context.Context, chatID int64, lang string, balKopecks int64) (string, int, string) {
	type entry struct {
		months int
		price  string
	}
	var list []entry
	title := ""
	if code := a.userPlanCode(ctx, chatID); code != "" && code != model.PlanCodeBase {
		if p, err := a.planByCode(ctx, code); err == nil && p != nil && p.Enabled && a.saleGridCurrency(&sale{Plan: p}) {
			for i := range p.Durations {
				d := &p.Durations[i]
				if d.Months > 0 && d.Base != "" {
					list = append(list, entry{d.Months, d.Base})
				}
			}
			if len(list) > 0 {
				title = planTitleHTML(lang, p)
			}
		}
	}
	if len(list) == 0 {
		title = ""
		pr := a.pricing()
		for _, mo := range model.PlanMonths {
			if base := pr.Base[mo]; base != "" {
				list = append(list, entry{mo, base})
			}
		}
	}
	var sb strings.Builder
	sb.WriteString("<pre>")
	sb.WriteString(padRight("Plan", 6) + "  " + padRight("Price", 11) + "  " + i18n.T(lang, "balance.col_lasts") + "\n")
	sb.WriteString(strings.Repeat("─", 34) + "\n")
	best := 0
	rows := 0
	for _, e := range list {
		k, ok := rubToKopecks(e.price)
		if !ok || k <= 0 {
			continue
		}
		rows++
		count := int(balKopecks / k)
		total := count * e.months
		if total > best {
			best = total
		}
		lasts := "—"
		if count > 0 {
			lasts = fmt.Sprintf("%d× ≈ %d %s", count, total, i18n.T(lang, "balance.mo"))
		}
		sb.WriteString(padRight(strconv.Itoa(e.months)+"m", 6) + "  " + padRight(e.price+curSuffix(curRUB), 11) + "  " + lasts + "\n")
	}
	sb.WriteString("</pre>")
	if rows == 0 {
		return "", 0, ""
	}
	return sb.String(), best, title
}

// topUpAmounts — пресеты пополнения и потолок произвольной суммы.
//
// Раньше и то и другое считалось только по сетке «Базового», и цену тарифа по
// ссылке дороже её максимума нельзя было положить на баланс одним платежом.
// Теперь потолок учитывает ВСЕ включённые тарифы, а пресеты — только тарифы
// публичных режимов: кнопка с ценой скрытого тарифа выдавала бы её всем.
func (a *App) topUpAmounts(ctx context.Context) ([]int64, int64) {
	pr := a.pricing()
	seen := map[int64]bool{}
	var amts []int64
	var maxK int64
	for _, mo := range model.PlanMonths {
		k, ok := rubToKopecks(pr.Base[mo])
		if !ok || k <= 0 || seen[k] {
			continue
		}
		seen[k] = true
		amts = append(amts, k)
		if k > maxK {
			maxK = k
		}
	}
	if plans, err := a.planList(ctx); err == nil {
		for i := range plans {
			p := &plans[i]
			if !p.Enabled {
				continue
			}
			// Цены тарифа в чужой валюте — не рубли: ни в пресеты, ни в
			// потолок (с баланса такой тариф всё равно не продаётся).
			if p.Currency != "" && p.Currency != pr.Currency {
				continue
			}
			mode := model.NormalizeAvailability(p.Availability)
			hidden := mode == model.PlanAvailList || mode == model.PlanAvailLink
			for j := range p.Durations {
				d := &p.Durations[j]
				if d.Months <= 0 {
					continue
				}
				k, ok := rubToKopecks(d.Base)
				if !ok || k <= 0 {
					continue
				}
				if k > maxK {
					maxK = k
				}
				if hidden || seen[k] {
					continue
				}
				seen[k] = true
				amts = append(amts, k)
			}
		}
	}
	sort.Slice(amts, func(i, j int) bool { return amts[i] < amts[j] })
	// Кнопок — не больше восьми: длинный прайс превращал бы экран пополнения
	// в простыню. Потолок при этом считается по всем ценам.
	if len(amts) > 8 {
		amts = amts[:8]
	}
	return amts, maxK
}

func (a *App) showTopUp(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	if !a.topUpEnabled() {
		a.showBalance(ctx, chatID)
		return
	}
	bal := a.userBalance(ctx, chatID)
	amts, _ := a.topUpAmounts(ctx)
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, k := range amts {
		row = append(row, btn(kopecksToRub(k)+curSuffix(curRUB), "top:amt:"+strconv.FormatInt(k, 10)))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "topup.btn_custom"), "top:custom")})
	rows = append(rows, navBack(lang, "menu:buy"))
	a.sendPayKB(ctx, chatID, i18n.T(lang, "topup.title", kopecksToRub(bal))+"\n\n"+i18n.T(lang, "balance.no_withdraw"), rows)
}

func (a *App) onTopUp(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	lang := a.lang(chatID)
	if !a.topUpEnabled() {
		// Кнопка могла остаться в старом сообщении: гейтим действие, а не вид.
		a.showBalance(ctx, chatID)
		return
	}
	switch action {
	case "amt":
		k, _ := strconv.ParseInt(arg, 10, 64)
		amts, _ := a.topUpAmounts(ctx)
		ok := false
		for _, v := range amts {
			if v == k {
				ok = true
				break
			}
		}
		if !ok {
			a.showTopUp(ctx, chatID)
			return
		}
		a.getUI(chatID).topUpKopecks = k
		a.showTopUpMethods(ctx, chatID)
	case "custom":
		a.getUI(chatID).awaitTopUp = true
		ask := i18n.T(lang, "topup.ask_amount")
		if _, maxK := a.topUpAmounts(ctx); maxK > 0 {
			ask = i18n.T(lang, "topup.ask_amount_max", kopecksToRub(maxK))
		}
		a.sendKB(ctx, chatID, ask,
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "top:cancel")}})
	case "cancel":
		a.getUI(chatID).awaitTopUp = false
		a.showTopUp(ctx, chatID)
	case "m":
		a.startTopUp(ctx, chatID, arg)
	}
}

func (a *App) setTopUpCustom(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	ui.awaitTopUp = false
	if !a.topUpEnabled() {
		a.showBalance(ctx, chatID)
		return
	}
	k, ok := rubToKopecks(text)
	if !ok || k <= 0 {
		a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "topup.bad_amount"),
			[][]models.InlineKeyboardButton{navBack(a.lang(chatID), "menu:topup")})
		return
	}
	if _, maxK := a.topUpAmounts(ctx); maxK > 0 && k > maxK {
		a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "topup.too_much", kopecksToRub(maxK)),
			[][]models.InlineKeyboardButton{navBack(a.lang(chatID), "menu:topup")})
		return
	}
	ui.topUpKopecks = k
	a.showTopUpMethods(ctx, chatID)
}

func (a *App) showTopUpMethods(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	k := a.getUI(chatID).topUpKopecks
	if k <= 0 {
		a.showTopUp(ctx, chatID)
		return
	}
	a.mu.Lock()
	ykOn := a.botCfg != nil && a.botCfg.YooKassa.Enabled
	cbOn := a.botCfg != nil && a.botCfg.CryptoBot.Enabled
	hlOn := a.botCfg != nil && a.botCfg.Heleket.Enabled
	a.mu.Unlock()
	var rows [][]models.InlineKeyboardButton
	if ykOn {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.yk_btn", kopecksToRub(k)+curSuffix(curRUB)), "top:m:yk")})
	}
	if cbOn {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.cb_btn", kopecksToRub(k)+curSuffix(curRUB)), "top:m:cb")})
	}
	if hlOn {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.hl_btn", kopecksToRub(k)+curSuffix(curRUB)), "top:m:hl")})
	}
	if len(rows) == 0 {
		// Админу — что включить, покупателю — куда писать. Раньше все видели
		// админскую инструкцию «Включите ЮKassa или CryptoBot», хотя такой
		// кнопки у них нет.
		key := "topup.unavailable"
		if chatID == a.cfg.AdminID {
			key = "topup.no_methods"
		}
		a.sendPayKB(ctx, chatID, i18n.T(lang, key), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	rows = append(rows, navBack(lang, "menu:topup"))
	a.sendPayKB(ctx, chatID, i18n.T(lang, "topup.choose_method", kopecksToRub(k)), rows)
}

func (a *App) startTopUp(ctx context.Context, chatID int64, method string) {
	lang := a.lang(chatID)
	if !a.topUpEnabled() {
		a.showBalance(ctx, chatID)
		return
	}
	k := a.getUI(chatID).topUpKopecks
	if k <= 0 {
		a.showTopUp(ctx, chatID)
		return
	}
	if _, maxK := a.topUpAmounts(ctx); maxK > 0 && k > maxK {
		a.getUI(chatID).topUpKopecks = 0
		a.sendKB(ctx, chatID, i18n.T(lang, "topup.too_much", kopecksToRub(maxK)),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:topup")})
		return
	}
	rub := kopecksToRub(k)
	payURL, checkExtID, err := a.topUpCreate(ctx, chatID, k, method, false)
	if err != nil {
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "счёт на пополнение", err))
		return
	}
	checkCB := "ykc:" + checkExtID
	payBtn := i18n.T(lang, "yk.btn_pay")
	checkBtn := i18n.T(lang, "yk.btn_check")
	switch method {
	case "cb":
		checkCB = "cbc:" + checkExtID + ":0"
		payBtn = i18n.T(lang, "cb.btn_pay")
		checkBtn = i18n.T(lang, "cb.btn_check")
	case "hl":
		checkCB = "hlc:" + checkExtID
		payBtn = i18n.T(lang, "hl.btn_pay")
		checkBtn = i18n.T(lang, "hl.btn_check")
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "topup.pay_prompt", rub), [][]models.InlineKeyboardButton{
		{{Text: payBtn, URL: payURL}},
		{btn(checkBtn, checkCB)},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// topUpCreate creates a balance top-up invoice (Purpose "topup") via YooKassa
// ("yk") or CryptoBot ("cb") and returns the pay URL plus the check ExtID.
// For "cb" the returned ExtID is the bare invoice id (caller adds the "cb:"
// prefix where needed). Shared by the chat flow and the Mini App so the pending
// record format stays identical for the webhooks.
// web — запрос пришёл из браузерного кабинета, а не из мини-аппа Telegram.
// Признак нужен CryptoBot: ссылка мини-аппа вне Telegram не открывается.
func (a *App) topUpCreate(ctx context.Context, chatID int64, k int64, method string, web bool) (payURL, checkExtID string, err error) {
	lang := a.lang(chatID)
	// Последний рубеж: сюда приходят и чат, и мини-апп, и веб-кабинет.
	if !a.topUpEnabled() {
		return "", "", errUserText(i18n.T(lang, "topup.disabled"))
	}
	rub := kopecksToRub(k)
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	switch method {
	case "yk":
		client := a.ykClient()
		if client == nil {
			return "", "", errUserText(i18n.T(lang, "yk.not_configured"))
		}
		ret := a.ykConfig().ReturnURL
		if ret == "" {
			ret = "https://t.me"
		}
		pay, e := client.CreatePayment(ctx, rub, "RUB", i18n.T(lang, "topup.invoice_desc"), ret, chatID, 0)
		if e != nil {
			a.payLog(ctx, model.PayMethodYooKassa, "", chatID, "invoice_error", "topup kopecks=%d: %v", k, e)
			return "", "", fmt.Errorf("шлюз ЮKassa: счёт на пополнение: %w", e)
		}
		a.payLog(ctx, model.PayMethodYooKassa, pay.ID, chatID, "invoice_created", "topup kopecks=%d", k)
		if a.store != nil {
			_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: pay.ID, TelegramID: chatID, Purpose: "topup", Kopecks: k})
		}
		return pay.Confirmation.ConfirmationURL, pay.ID, nil
	case "cb":
		client := a.cbClient()
		if client == nil {
			return "", "", errUserText(i18n.T(lang, "cb.not_configured"))
		}
		// Баланс ведётся в рублях, поэтому счёт на пополнение всегда в RUB.
		// Описание задаём явно: по умолчанию клиент пишет «VPN subscription
		// N mo», и при N=0 плательщик видел «подписка на 0 мес.».
		inv, e := client.CreateInvoice(ctx, rub, "RUB", a.cbConfig().Asset, i18n.T(lang, "topup.invoice_desc"), chatID, 0)
		if e != nil {
			a.payLog(ctx, model.PayMethodCryptoBot, "", chatID, "invoice_error", "topup kopecks=%d: %v", k, e)
			return "", "", fmt.Errorf("шлюз CryptoBot: счёт на пополнение: %w", e)
		}
		extID := "cb:" + strconv.FormatInt(inv.InvoiceID, 10)
		a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "invoice_created", "topup kopecks=%d", k)
		if a.store != nil {
			_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: extID, TelegramID: chatID, Purpose: "topup", Kopecks: k})
		}
		// Из браузерного кабинета — веб-ссылка: ссылку мини-аппа вне Telegram
		// открыть нечем. У покупки этот выбор давно есть, у пополнения не было.
		payURL := inv.MiniAppInvoiceURL
		if web && inv.WebAppInvoiceURL != "" {
			payURL = inv.WebAppInvoiceURL
		}
		if payURL == "" {
			payURL = inv.BotInvoiceURL
		}
		return payURL, strconv.FormatInt(inv.InvoiceID, 10), nil
	case "hl":
		if a.hlClient() == nil {
			return "", "", errUserText(i18n.T(lang, "hl.not_configured"))
		}
		payURL, uuid, e := a.hlCreateInvoice(ctx, chatID, 0, rub, purposeTopUp, k)
		if e != nil {
			return "", "", fmt.Errorf("шлюз Heleket: счёт на пополнение: %w", e)
		}
		return payURL, uuid, nil
	}
	return "", "", errUserText(i18n.T(lang, "topup.unavailable"))
}

func (a *App) finalizeTopUp(ctx context.Context, chatID int64, kopecks int64, method, amount, extID string) error {
	if a.store == nil {
		return nil
	}
	// Запись платежа и зачисление — одной транзакцией. Порознь сбой между
	// ними оставлял барьер повтора стоять при нулевом балансе, и пополнение
	// пропадало навсегда: повторную доставку гасил тот же барьер, а сверка
	// закрывала счёт.
	if err := a.store.AddPaymentAndBalance(ctx, &model.Payment{
		TelegramID: chatID, Method: method, Amount: amount, Status: model.PaymentPaid, ExtID: extID, Comment: "topup",
	}, kopecks); err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.payLog(ctx, method, extID, chatID, "duplicate", "пополнение уже зачислено")
			return nil
		}
		a.payLog(ctx, method, extID, chatID, "error", "зачисление пополнения: %v", err)
		return err
	}
	a.payLog(ctx, method, extID, chatID, "topup_credited", "kopecks=%d amount=%s", kopecks, amount)
	// Чек «Мой налог» — только по платежам ЮKassa.
	if method == model.PayMethodYooKassa {
		a.fiscalize(float64(kopecks)/100, "Пополнение баланса")
	}
	lang := a.lang(chatID)
	a.notifyKB(ctx, chatID, i18n.T(lang, "topup.done", kopecksToRub(kopecks), kopecksToRub(a.userBalance(ctx, chatID))),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.buy"), "menu:buy")}})
	return nil
}

// refundBalance возвращает списанные деньги после неудачной выдачи.
//
// Контекст берётся фоновый, а не тот, в котором провалилась выдача: запрос из
// мини-аппа или кабинета мог уже отмениться по таймауту, и возврат тем же
// контекстом молча не выполнился бы — деньги остались бы списанными. Неудача
// возврата не проглатывается: она идёт в журнал ошибкой и уходит админу.
func (a *App) refundBalance(chatID int64, kopecks int64, cause error) {
	if a.store == nil || kopecks <= 0 {
		return
	}
	ctx := a.bgContext()
	// Ровно одна попытка, без повторов. Начисление баланса не идемпотентно
	// (balance = balance + N) и не прикрыто барьером по ключу сделки, а самый
	// частый способ получить ошибку — потерянный ОТВЕТ на уже применённую
	// запись. Повтор в этом случае вернул бы деньги дважды, то есть менял бы
	// потерю на кражу. Неудачу зовём разбирать руками — это и надёжнее, и
	// честнее.
	err := a.store.AddBalance(ctx, chatID, kopecks)
	alang := a.lang(a.cfg.AdminID)
	if err != nil {
		a.payLog(ctx, "balance", "", chatID, "error", "возврат не прошёл: %d коп. списаны и не возвращены: %v", kopecks, err)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.refund_failed", a.userLabelByID(ctx, chatID), kopecksToRub(kopecks)))
		return
	}
	a.payLog(ctx, "balance", "", chatID, "balance_refund", "kopecks=%d возвращены после ошибки выдачи", kopecks)
	// Отказ мог прийти уже ПОСЛЕ того, как панель применила продление — обрыв
	// или таймаут на чтении ответа выглядят так же, как честный отказ. Деньги
	// вернули, а подписка могла остаться: отличить это по ответу панели нельзя,
	// поэтому зовём админа сверить вручную.
	if panelStateUnknown(cause) {
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.refund_unclear", a.userLabelByID(ctx, chatID), kopecksToRub(kopecks)))
	}
}

// panelStateUnknown — отказ, после которого нельзя утверждать, что панель
// ничего не изменила: обрыв связи или истёкший срок ожидания ответа.
func panelStateUnknown(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := err.Error()
	// Обрыв на ЧТЕНИИ ответа — тот самый случай, ради которого всё это: PATCH
	// панель применила, а ответ не доехал. Такая ошибка приходит от декодера
	// и слова «нет связи с панелью» не содержит.
	for _, s := range []string{
		"нет связи с панелью",
		"unexpected EOF",
		"connection reset",
		"broken pipe",
		"EOF",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	// Шлюзовые коды: запрос до панели мог дойти и примениться, а ответ
	// подменил прокси. 5xx самой панели сюда не входит — она отвечает сама,
	// значит про своё состояние знает.
	for _, s := range []string{"HTTP 502", "HTTP 503", "HTTP 504"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// balanceExtID — ключ сделки для оплаты с баланса.
//
// У внешних платёжек ключ приходит от провайдера, и по нему ядро выдачи
// отсекает повторы. У баланса ключа не было (передавалась пустая строка), а
// пустой ключ выключает барьер целиком: и шардовый замок, и проверку
// «уже выдано», и частичный уникальный индекс (он покрывает только ext_id <> ”).
// Поэтому два одновременных нажатия списывали дважды и дважды продлевали.
//
// Ключ обязан быть стабильным для ПОВТОРА ОДНОГО нажатия и разным для разных
// покупок, иначе законная «ещё месяц через минуту» упёрлась бы в барьер
// навсегда. Различителем служит отпечаток момента выбора: время создания
// намерения покупки (оно перезаписывается при каждом новом выборе срока и
// удаляется после успешной сделки), а где намерения нет (мини-апп, кабинет) —
// конец срока ДО покупки: он сдвигается каждой выдачей.
//
// Пустой различитель означает «различить нечем» — тогда ключ не выдаётся и
// поведение остаётся прежним, без барьера, но и без риска заблокировать
// законную покупку.
func balanceExtID(tgID int64, planCode string, months int, discriminator string) string {
	if discriminator == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(discriminator))
	return fmt.Sprintf("bal:%d:%s:%d:%x", tgID, planCode, months, sum[:6])
}

func (a *App) payFromBalance(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	months := s.Months
	priceStr := a.saleBase(s)
	kopecks, ok := rubToKopecks(priceStr)
	// Баланс живёт в рублях: тариф в другой валюте с баланса не продаётся —
	// иначе «5 $» молча списались бы как «5 ₽».
	if !a.saleGridCurrency(s) {
		// Отдельная ветка: раньше сюда попадало «Тарифы пока не настроены» —
		// человеку, стоящему на карточке настроенного тарифа.
		a.sendHome(ctx, chatID, i18n.T(lang, "buy.currency_mismatch",
			curSymbol(a.saleCurrency(s)), curSymbol(a.pricing().Currency)))
		return
	}
	if priceStr == "" || !ok || kopecks <= 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "buy.no_plans"))
		return
	}
	// Ключ сделки — до списания: по нему отсекается двойное нажатие. Момент
	// выбора берётся из намерения покупки; ошибку чтения игнорируем — тогда
	// ключа не будет и поведение останется прежним.
	discr := ""
	if in, ierr := a.buyIntent(ctx, chatID); ierr == nil && in != nil {
		discr = in.CreatedAt
	}
	extID := balanceExtID(chatID, s.planCode(), months, discr)
	if extID != "" {
		if done, derr := a.store.PaymentByExtID(ctx, extID); derr == nil && done {
			// Первое нажатие уже выдало подписку. Денег не трогаем вовсе —
			// иначе списали бы и тут же вернули, оставив в журнале пугающий
			// откат вместо честного «уже куплено».
			a.payLog(ctx, "balance", extID, chatID, "duplicate", "повторное нажатие: покупка уже выполнена")
			a.showMySubs(ctx, chatID)
			return
		}
	}
	// Снимок — до списания: после DeductBalance отказ по любой причине означает
	// возврат денег, и лишних причин отказа быть не должно.
	snap := a.saleSnapshot(s)
	deducted, err := a.store.DeductBalance(ctx, chatID, kopecks)
	if err != nil {
		a.sendHome(ctx, chatID, a.clientErr(ctx, chatID, "списание с баланса", err))
		return
	}
	if deducted {
		a.payLog(ctx, "balance", extID, chatID, "balance_deducted", "kopecks=%d plan=%s months=%d", kopecks, s.planCode(), months)
	}
	if !deducted {
		rows := [][]models.InlineKeyboardButton{}
		if a.topUpEnabled() {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")})
		}
		rows = append(rows, homeRow(lang))
		a.sendKB(ctx, chatID, i18n.T(lang, "balance.not_enough", kopecksToRub(kopecks), kopecksToRub(a.userBalance(ctx, chatID))), rows)
		return
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, "balance", priceStr+curSuffix(curRUB), extID, snap)
	if err != nil {
		// Дубль поймало ядро выдачи (второе нажатие прошло проверку выше, пока
		// первое ещё не записало платёж): деньги вернуть, но это не сбой —
		// подписка выдана первым нажатием.
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.refundBalance(chatID, kopecks, nil)
			a.showMySubs(ctx, chatID)
			return
		}
		a.refundBalance(chatID, kopecks, err)
		a.sendHome(ctx, chatID, i18n.T(lang, "balance.pay_fail", a.clientErr(ctx, chatID, "оплата с баланса", err)))
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}

// topUpEnabled — можно ли класть деньги на баланс. Оплата с баланса от этой
// настройки не зависит: реферальные начисления и промокоды приходят на баланс
// и при выключенном пополнении, тратить их надо чем-то.
func (a *App) topUpEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return true
	}
	if !a.botCfg.Wallet.Init {
		return true
	}
	return a.botCfg.Wallet.TopUp
}
