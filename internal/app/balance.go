package app

import (
	"context"
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
	table, best := a.balanceForecast(lang, bal)
	caption := i18n.T(lang, "balance.head", kopecksToRub(bal))
	if table != "" {
		caption += "\n\n" + i18n.T(lang, "balance.forecast_hdr") + "\n" + table
		if best > 0 {
			caption += "\n" + i18n.T(lang, "balance.max_months", best)
		}
	}
	caption += "\n\n" + i18n.T(lang, "balance.autopay_note")
	a.sendPayKB(ctx, chatID, caption, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup"), btn(i18n.T(lang, "btn.buy"), "menu:buy")},
		{btn(i18n.T(lang, "btn.promo"), "pr:enter")},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) balanceForecast(lang string, balKopecks int64) (string, int) {
	pr := a.pricing()
	var sb strings.Builder
	sb.WriteString("<pre>")
	sb.WriteString(padRight("Plan", 6) + "  " + padRight("Price", 11) + "  " + i18n.T(lang, "balance.col_lasts") + "\n")
	sb.WriteString(strings.Repeat("─", 34) + "\n")
	best := 0
	rows := 0
	for _, mo := range model.PlanMonths {
		base := pr.Base[mo]
		if base == "" {
			continue
		}
		k, ok := rubToKopecks(base)
		if !ok || k <= 0 {
			continue
		}
		rows++
		count := int(balKopecks / k)
		total := count * mo
		if total > best {
			best = total
		}
		lasts := "—"
		if count > 0 {
			lasts = fmt.Sprintf("%d× ≈ %d %s", count, total, i18n.T(lang, "balance.mo"))
		}
		sb.WriteString(padRight(strconv.Itoa(mo)+"m", 6) + "  " + padRight(base+curSuffix(curRUB), 11) + "  " + lasts + "\n")
	}
	sb.WriteString("</pre>")
	if rows == 0 {
		return "", 0
	}
	return sb.String(), best
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
	a.sendPayKB(ctx, chatID, i18n.T(lang, "topup.title", kopecksToRub(bal)), rows)
}

func (a *App) onTopUp(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	lang := a.lang(chatID)
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
		a.sendPayKB(ctx, chatID, i18n.T(lang, "topup.no_methods"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	rows = append(rows, navBack(lang, "menu:topup"))
	a.sendPayKB(ctx, chatID, i18n.T(lang, "topup.choose_method", kopecksToRub(k)), rows)
}

func (a *App) startTopUp(ctx context.Context, chatID int64, method string) {
	lang := a.lang(chatID)
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
	payURL, checkExtID, err := a.topUpCreate(ctx, chatID, k, method)
	if err != nil {
		a.sendHome(ctx, chatID, err.Error())
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
func (a *App) topUpCreate(ctx context.Context, chatID int64, k int64, method string) (payURL, checkExtID string, err error) {
	lang := a.lang(chatID)
	rub := kopecksToRub(k)
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	switch method {
	case "yk":
		client := a.ykClient()
		if client == nil {
			return "", "", errors.New(i18n.T(lang, "yk.not_configured"))
		}
		ret := a.ykConfig().ReturnURL
		if ret == "" {
			ret = "https://t.me"
		}
		pay, e := client.CreatePayment(ctx, rub, "RUB", i18n.T(lang, "topup.invoice_desc"), ret, chatID, 0)
		if e != nil {
			a.payLog(ctx, model.PayMethodYooKassa, "", chatID, "invoice_error", "topup kopecks=%d: %v", k, e)
			return "", "", errors.New(i18n.T(lang, "yk.fail", e.Error()))
		}
		a.payLog(ctx, model.PayMethodYooKassa, pay.ID, chatID, "invoice_created", "topup kopecks=%d", k)
		if a.store != nil {
			_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: pay.ID, TelegramID: chatID, Purpose: "topup", Kopecks: k})
		}
		return pay.Confirmation.ConfirmationURL, pay.ID, nil
	case "cb":
		client := a.cbClient()
		if client == nil {
			return "", "", errors.New(i18n.T(lang, "cb.not_configured"))
		}
		// Баланс ведётся в рублях, поэтому счёт на пополнение всегда в RUB.
		inv, e := client.CreateInvoice(ctx, rub, "RUB", a.cbConfig().Asset, chatID, 0)
		if e != nil {
			a.payLog(ctx, model.PayMethodCryptoBot, "", chatID, "invoice_error", "topup kopecks=%d: %v", k, e)
			return "", "", errors.New(i18n.T(lang, "cb.fail", e.Error()))
		}
		extID := "cb:" + strconv.FormatInt(inv.InvoiceID, 10)
		a.payLog(ctx, model.PayMethodCryptoBot, extID, chatID, "invoice_created", "topup kopecks=%d", k)
		if a.store != nil {
			_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: extID, TelegramID: chatID, Purpose: "topup", Kopecks: k})
		}
		payURL := inv.MiniAppInvoiceURL
		if payURL == "" {
			payURL = inv.BotInvoiceURL
		}
		return payURL, strconv.FormatInt(inv.InvoiceID, 10), nil
	case "hl":
		if a.hlClient() == nil {
			return "", "", errors.New(i18n.T(lang, "hl.not_configured"))
		}
		payURL, uuid, e := a.hlCreateInvoice(ctx, chatID, 0, rub, purposeTopUp, k)
		if e != nil {
			return "", "", errors.New(i18n.T(lang, "hl.fail", e.Error()))
		}
		return payURL, uuid, nil
	}
	return "", "", errors.New(i18n.T(lang, "topup.no_methods"))
}

func (a *App) finalizeTopUp(ctx context.Context, chatID int64, kopecks int64, method, amount, extID string) error {
	if a.store == nil {
		return nil
	}
	if err := a.store.AddPayment(ctx, &model.Payment{
		TelegramID: chatID, Method: method, Amount: amount, Status: model.PaymentPaid, ExtID: extID, Comment: "topup",
	}); err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.payLog(ctx, method, extID, chatID, "duplicate", "пополнение уже зачислено")
			return nil
		}
		a.payLog(ctx, method, extID, chatID, "error", "запись пополнения: %v", err)
		return err
	}
	if err := a.store.AddBalance(ctx, chatID, kopecks); err != nil {
		a.payLog(ctx, method, extID, chatID, "error", "зачисление баланса: %v", err)
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

func (a *App) payFromBalance(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	s := a.saleOrAsk(ctx, chatID)
	if s == nil {
		return
	}
	months := s.Months
	priceStr := a.saleBase(s)
	kopecks, ok := rubToKopecks(priceStr)
	if priceStr == "" || !ok || kopecks <= 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "buy.no_plans"))
		return
	}
	// Снимок — до списания: после DeductBalance отказ по любой причине означает
	// возврат денег, и лишних причин отказа быть не должно.
	snap := a.saleSnapshot(s)
	deducted, err := a.store.DeductBalance(ctx, chatID, kopecks)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if deducted {
		a.payLog(ctx, "balance", "", chatID, "balance_deducted", "kopecks=%d plan=%s months=%d", kopecks, s.planCode(), months)
	}
	if !deducted {
		a.sendKB(ctx, chatID, i18n.T(lang, "balance.not_enough", kopecksToRub(kopecks), kopecksToRub(a.userBalance(ctx, chatID))),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")}, homeRow(lang)})
		return
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, "balance", priceStr+curSuffix(curRUB), "", snap)
	if err != nil {
		_ = a.store.AddBalance(ctx, chatID, kopecks)
		a.payLog(ctx, "balance", "", chatID, "balance_refund", "kopecks=%d возвращены после ошибки выдачи", kopecks)
		a.sendHome(ctx, chatID, i18n.T(lang, "balance.pay_fail", err.Error()))
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}
