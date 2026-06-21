package app

import (
	"context"
	"errors"
	"math"
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

func (a *App) startStars(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	months := a.getUI(chatID).buyMonths
	if months == 0 {
		months = model.PlanMonths[0]
	}
	amount := a.pricing().StarPrice(months)
	if !a.starsConfig().Enabled || amount <= 0 {
		a.sendHome(ctx, chatID, i18n.T(lang, "stars.no_price"))
		return
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	a.msg.SendInvoice(ctx, chatID, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_sent", "purchase months=%d stars=%d", months, amount)
}

func (a *App) handlePreCheckout(ctx context.Context, q *models.PreCheckoutQuery) {
	months := 0
	if _, after, ok := strings.Cut(q.InvoicePayload, ":"); ok {
		months, _ = strconv.Atoi(after)
	}
	if !a.starsConfig().Enabled || months <= 0 || a.pricing().StarPrice(months) != q.TotalAmount {
		var fromID int64
		if q.From != nil {
			fromID = q.From.ID
		}
		a.payLog(ctx, model.PayMethodStars, "", fromID, "precheckout_rejected", "payload=%s total=%d enabled=%v", q.InvoicePayload, q.TotalAmount, a.starsConfig().Enabled)
		a.msg.AnswerPreCheckout(ctx, q.ID, false, i18n.T(a.lang(fromID), "stars.no_price"))
		return
	}
	a.msg.AnswerPreCheckout(ctx, q.ID, true, "")
}

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
	a.payLog(ctx, model.PayMethodStars, sp.TelegramPaymentChargeID, chatID, "payment_received", "total=%d payload=%s", sp.TotalAmount, sp.InvoicePayload)
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodStars, amount, sp.TelegramPaymentChargeID)
	if err != nil {
		a.notify(ctx, chatID, i18n.T(a.lang(chatID), "stars.fail", err.Error()))
		return
	}
	a.sendSubActive(ctx, chatID, link, expireAt)
}

func (a *App) showStarsAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	status := i18n.T(lang, "admin.off")
	if a.starsConfig().Enabled {
		status = i18n.T(lang, "admin.on")
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "admin.stars_title", status, a.formatStarPrices()), [][]models.InlineKeyboardButton{
		{toggleBtn(lang, a.starsConfig().Enabled, "star:toggle"), btn(i18n.T(lang, "admin.btn_prices"), "star:prices")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
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
		lang := a.lang(chatID)
		var row []models.InlineKeyboardButton
		for _, mo := range model.PlanMonths {
			row = append(row, btn(strconv.Itoa(mo)+"м", "star:price:"+strconv.Itoa(mo)))
		}
		a.sendKB(ctx, chatID, i18n.T(lang, "admin.ask_price_month"), [][]models.InlineKeyboardButton{row, navBack(lang, "menu:stars")})
	case "price":
		mo, _ := strconv.Atoi(arg)
		lang := a.lang(chatID)
		ui := a.getUI(chatID)
		ui.adminInput = "starprice"
		ui.priceMonths = mo
		prompt := i18n.T(lang, "admin.stars_ask_price", mo)
		if s := a.starsSuggestion(lang, mo); s != "" {
			prompt += "\n\n" + s
		}
		a.askInput(ctx, chatID, prompt, "menu:stars")
	}
}

const approxRubPerStar = 1.5

func (a *App) starsSuggestion(lang string, months int) string {
	base := a.pricing().Base[months]
	k, ok := rubToKopecks(base)
	if !ok || k <= 0 {
		return ""
	}
	rub := float64(k) / 100.0
	stars := int(math.Ceil(rub / approxRubPerStar * 1.05))
	return i18n.T(lang, "stars.suggest", base, stars)
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

// starsInvoiceLink builds a Telegram Stars invoice LINK (for Mini App
// openInvoice). Uses the same payload as the chat invoice so the
// pre-checkout/successful-payment handlers treat them identically.
var errStarsUnavailable = errors.New("оплата звёздами недоступна")

func (a *App) starsInvoiceLink(ctx context.Context, chatID int64, months int) (string, error) {
	amount := a.pricing().StarPrice(months)
	if !a.starsConfig().Enabled || amount <= 0 {
		return "", errStarsUnavailable
	}
	if a.store != nil {
		_ = a.store.UpsertUser(ctx, chatID)
	}
	lang := a.lang(chatID)
	title := i18n.T(lang, "stars.invoice_title", months)
	desc := i18n.T(lang, "stars.invoice_desc", months)
	link, err := a.msg.CreateInvoiceLink(ctx, title, desc, "stars:"+strconv.Itoa(months), "XTR", amount)
	if err != nil {
		return "", err
	}
	a.payLog(ctx, model.PayMethodStars, "", chatID, "invoice_link", "purchase months=%d stars=%d", months, amount)
	return link, nil
}
