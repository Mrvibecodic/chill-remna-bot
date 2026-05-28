package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func (a *App) starsConfig() model.StarsConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.StarsConfig{}
	}
	return a.botCfg.Stars
}

// --- пользователь: оплата Telegram Stars (XTR) ---

func (a *App) startStars(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	amount := a.pricing().StarPrice(months)
	if !a.starsConfig().Enabled || amount <= 0 {
		a.send(ctx, chatID, i18n.T(lang, "stars.no_price"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	a.msg.SendInvoice(ctx, chatID, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
}

// handlePreCheckout подтверждает предоплатную проверку (для Stars — всегда ok).
func (a *App) handlePreCheckout(ctx context.Context, q *models.PreCheckoutQuery) {
	a.msg.AnswerPreCheckout(ctx, q.ID, true, "")
}

// handleSuccessfulPayment финализирует покупку за Stars: провижн + лог + ссылка.
func (a *App) handleSuccessfulPayment(ctx context.Context, m *models.Message) {
	sp := m.SuccessfulPayment
	chatID := m.Chat.ID
	months := 0
	if _, after, ok := strings.Cut(sp.InvoicePayload, ":"); ok {
		months, _ = strconv.Atoi(after)
	}
	if months == 0 {
		months = model.PlanMonths[0]
	}
	amount := strconv.Itoa(sp.TotalAmount) + " ⭐"
	link, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodStars, amount, sp.TelegramPaymentChargeID)
	if err != nil {
		a.notify(ctx, chatID, i18n.T(a.lang(chatID), "stars.fail", err.Error()))
		return
	}
	a.notify(ctx, chatID, i18n.T(a.lang(chatID), "stars.paid_ok", link))
}

// --- админ: настройки Stars ---

func (a *App) showStarsAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	status := i18n.T(lang, "admin.off")
	if a.starsConfig().Enabled {
		status = i18n.T(lang, "admin.on")
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "admin.stars_title", status, a.formatStarPrices()), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "admin.btn_toggle"), "star:toggle"), btn(i18n.T(lang, "admin.btn_prices"), "star:prices")},
		homeRow(lang),
	})
}

func (a *App) onStars(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Stars.Enabled = !a.botCfg.Stars.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showStarsAdmin(ctx, chatID)
	case "prices":
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "star:price:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price_month"), [][]models.InlineKeyboardButton{row})
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "starprice"
		ui.priceMonths = mo
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "admin.stars_ask_price", mo), "menu:stars")
	}
}

func (a *App) formatStarPrices() string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := pr.StarPrice(mo); v > 0 {
			parts = append(parts, strconv.Itoa(mo)+"м="+strconv.Itoa(v)+"⭐")
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}
