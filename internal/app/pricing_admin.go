package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// askPriceMonth — выбор срока для задания цены; callback "<prefix>:price:<mo>".
func (a *App) askPriceMonth(ctx context.Context, chatID int64, prefix string) {
	var row []models.InlineKeyboardButton
	for _, mo := range model.PlanMonths {
		row = append(row, btn(strconv.Itoa(mo)+"м", prefix+":price:"+strconv.Itoa(mo)))
	}
	a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "admin.ask_price_month"), [][]models.InlineKeyboardButton{row})
}

func (a *App) formatBasePrices() string {
	pr := a.pricing()
	var parts []string
	for _, mo := range model.PlanMonths {
		if v := pr.Base[mo]; v != "" {
			parts = append(parts, strconv.Itoa(mo)+"м="+v)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " ")
}

// showPricing — единый базовый прайс (общий для всех денежных методов).
func (a *App) showPricing(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cur := a.pricing().Currency
	if cur == "" {
		cur = i18n.T(lang, "admin.none")
	}
	a.sendKB(ctx, chatID, i18n.T(lang, "pricing.title", a.formatBasePrices(), cur), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "pricing.btn_base"), "prc:base"), btn(i18n.T(lang, "pricing.btn_cur"), "prc:cur")},
		homeRow(lang),
	})
}

func (a *App) onPricing(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "base":
		a.askPriceMonth(ctx, chatID, "prc")
	case "price":
		mo, _ := strconv.Atoi(arg)
		ui := a.getUI(chatID)
		ui.adminInput = "baseprice"
		ui.priceMonths = mo
		a.send(ctx, chatID, i18n.T(lang, "admin.ask_base_price", mo))
	case "cur":
		a.getUI(chatID).adminInput = "currency"
		a.send(ctx, chatID, i18n.T(lang, "admin.ask_currency"))
	}
}
