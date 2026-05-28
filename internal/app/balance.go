package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

// topUpPresets — пресеты пополнения (рубли).
var topUpPresets = []int{100, 300, 500, 1000}

// rubToKopecks парсит "150" / "150.50" / "150,5" → копейки.
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

// kopecksToRub форматирует копейки в "150" или "150.50".
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

// showBalance — вкладка «Баланс»: текущий баланс + планировщик «на сколько хватит»
// по каждому тарифу, плюс кнопки пополнить/купить.
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
	a.sendKB(ctx, chatID, caption, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup"), btn(i18n.T(lang, "btn.buy"), "menu:buy")},
		{btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// balanceForecast строит таблицу «на сколько хватит баланса» по тарифам и
// возвращает максимум месяцев среди вариантов, укладывающихся в баланс.
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

// --- пополнение баланса ---

func (a *App) showTopUp(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	bal := a.userBalance(ctx, chatID)
	var rows [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, r := range topUpPresets {
		row = append(row, btn(strconv.Itoa(r)+curSuffix(curRUB), "top:amt:"+strconv.Itoa(r)))
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
	a.sendKB(ctx, chatID, i18n.T(lang, "topup.title", kopecksToRub(bal)), rows)
}

func (a *App) onTopUp(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	lang := a.lang(chatID)
	switch action {
	case "amt":
		r, _ := strconv.Atoi(arg)
		a.getUI(chatID).topUpKopecks = int64(r) * 100
		a.showTopUpMethods(ctx, chatID)
	case "custom":
		a.getUI(chatID).awaitTopUp = true
		a.sendKB(ctx, chatID, i18n.T(lang, "topup.ask_amount"),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "top:cancel")}})
	case "cancel":
		a.getUI(chatID).awaitTopUp = false
		a.showTopUp(ctx, chatID)
	case "m":
		a.startTopUp(ctx, chatID, arg)
	}
}

// setTopUpCustom принимает введённую пользователем сумму пополнения.
func (a *App) setTopUpCustom(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	ui.awaitTopUp = false
	k, ok := rubToKopecks(text)
	if !ok || k <= 0 {
		a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "topup.bad_amount"),
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
	a.mu.Unlock()
	var rows [][]models.InlineKeyboardButton
	if ykOn {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.yk_btn", kopecksToRub(k)+curSuffix(curRUB)), "top:m:yk")})
	}
	if cbOn {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "method.cb_btn", kopecksToRub(k)+curSuffix(curRUB)), "top:m:cb")})
	}
	if len(rows) == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "topup.no_methods"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	rows = append(rows, navBack(lang, "menu:topup"))
	a.sendKB(ctx, chatID, i18n.T(lang, "topup.choose_method", kopecksToRub(k)), rows)
}

// startTopUp создаёт инвойс пополнения. Назначение ("topup" + копейки) пишем в
// pending_invoices — по нему вебхук/проверка/реконсилятор зачислят на баланс.
func (a *App) startTopUp(ctx context.Context, chatID int64, method string) {
	lang := a.lang(chatID)
	k := a.getUI(chatID).topUpKopecks
	if k <= 0 {
		a.showTopUp(ctx, chatID)
		return
	}
	rub := kopecksToRub(k)
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	switch method {
	case "yk":
		client := a.ykClient()
		if client == nil {
			a.send(ctx, chatID, i18n.T(lang, "yk.not_configured"))
			return
		}
		ret := a.ykConfig().ReturnURL
		if ret == "" {
			ret = "https://t.me"
		}
		pay, err := client.CreatePayment(ctx, rub, "RUB", i18n.T(lang, "topup.invoice_desc"), ret, chatID, 0)
		if err != nil {
			a.send(ctx, chatID, i18n.T(lang, "yk.fail", err.Error()))
			return
		}
		if a.store != nil {
			_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
				Method: model.PayMethodYooKassa, ExtID: pay.ID, TelegramID: chatID, Purpose: "topup", Kopecks: k,
			})
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "topup.pay_prompt", rub), [][]models.InlineKeyboardButton{
			{{Text: i18n.T(lang, "yk.btn_pay"), URL: pay.Confirmation.ConfirmationURL}},
			{btn(i18n.T(lang, "yk.btn_check"), "ykc:"+pay.ID)},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
	case "cb":
		client := a.cbClient()
		if client == nil {
			a.send(ctx, chatID, i18n.T(lang, "cb.not_configured"))
			return
		}
		inv, err := client.CreateInvoice(ctx, rub, a.cbConfig().Asset, chatID, 0)
		if err != nil {
			a.send(ctx, chatID, i18n.T(lang, "cb.fail", err.Error()))
			return
		}
		extID := "cb:" + strconv.FormatInt(inv.InvoiceID, 10)
		if a.store != nil {
			_ = a.store.AddPendingInvoice(ctx, &model.PendingInvoice{
				Method: model.PayMethodCryptoBot, ExtID: extID, TelegramID: chatID, Purpose: "topup", Kopecks: k,
			})
		}
		payURL := inv.MiniAppInvoiceURL
		if payURL == "" {
			payURL = inv.BotInvoiceURL
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "topup.pay_prompt", rub), [][]models.InlineKeyboardButton{
			{{Text: i18n.T(lang, "cb.btn_pay"), URL: payURL}},
			{btn(i18n.T(lang, "cb.btn_check"), "cbc:"+strconv.FormatInt(inv.InvoiceID, 10)+":0")},
			{btn(i18n.T(lang, "btn.home"), "menu:home")},
		})
	}
}

// finalizeTopUp идемпотентно зачисляет пополнение на баланс. ext_id резервируется
// записью платежа (UNIQUE) ДО зачисления — двойного кредита на гонке не будет.
func (a *App) finalizeTopUp(ctx context.Context, chatID int64, kopecks int64, method, amount, extID string) error {
	if a.store == nil {
		return nil
	}
	if err := a.store.AddPayment(ctx, &model.Payment{
		TelegramID: chatID, Method: method, Amount: amount, Status: model.PaymentPaid, ExtID: extID, Comment: "topup",
	}); err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			return nil // уже зачтено
		}
		return err
	}
	if err := a.store.AddBalance(ctx, chatID, kopecks); err != nil {
		return err
	}
	lang := a.lang(chatID)
	a.notifyKB(ctx, chatID, i18n.T(lang, "topup.done", kopecksToRub(kopecks), kopecksToRub(a.userBalance(ctx, chatID))),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.buy"), "menu:buy")}})
	return nil
}

// --- оплата с баланса (автосписание) ---

// payFromBalance списывает цену тарифа с баланса и сразу выдаёт подписку.
// При ошибке выдачи в панель — возвращает деньги на баланс (рефанд).
func (a *App) payFromBalance(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	priceStr := a.pricing().Base[months]
	kopecks, ok := rubToKopecks(priceStr)
	if priceStr == "" || !ok || kopecks <= 0 {
		a.send(ctx, chatID, i18n.T(lang, "buy.no_plans"))
		return
	}
	deducted, err := a.store.DeductBalance(ctx, chatID, kopecks)
	if err != nil {
		a.send(ctx, chatID, "❌ "+err.Error())
		return
	}
	if !deducted {
		a.sendKB(ctx, chatID, i18n.T(lang, "balance.not_enough", kopecksToRub(kopecks), kopecksToRub(a.userBalance(ctx, chatID))),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "balance.btn_topup"), "menu:topup")}, homeRow(lang)})
		return
	}
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, "balance", priceStr+curSuffix(curRUB), "")
	if err != nil {
		// рефанд: выдача не удалась — возвращаем средства
		_ = a.store.AddBalance(ctx, chatID, kopecks)
		a.send(ctx, chatID, i18n.T(lang, "balance.pay_fail", err.Error()))
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}
