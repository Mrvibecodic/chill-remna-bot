package app

import (
	"context"
	"slices"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Доступность способов оплаты по местам: Telegram (чат и мини-апп) и сайт
// (веб-кабинет). Галочки решают только, показать ли кнопку и выставить ли
// новый счёт. Всё, что идёт после счёта (вебхуки, сверки, проверка Stars
// перед списанием, чек перевода, автопродление), их не читает: счёт,
// выставленный до снятия галочки, обязан дойти до подписки.

const (
	payPlaceTG  = "tg"
	payPlaceWeb = "web"
)

// payAccessLocked — способ не снят с места. Stars на сайте не бывает: в
// браузере звёздами не заплатить. Вызывать под a.mu.
func payAccessLocked(c *model.BotConfig, method string, web bool) bool {
	if web && method == model.PayMethodStars {
		return false
	}
	if c == nil {
		return true
	}
	off := c.PayAccess.TelegramOff
	if web {
		off = c.PayAccess.WebOff
	}
	return !slices.Contains(off, method)
}

// payShown — то же, с замком.
func (a *App) payShown(method string, web bool) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return payAccessLocked(a.botCfg, method, web)
}

// payPlaceMethods — способы, которые можно снять с места: включённые, в
// порядке экрана «Способы оплаты».
func (a *App) payPlaceMethods(web bool) []string {
	var out []string
	for _, m := range payMethodOrder {
		if web && m == model.PayMethodStars {
			continue
		}
		if a.methodEnabled(m) {
			out = append(out, m)
		}
	}
	return out
}

func (a *App) onPayAccess(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "home":
		a.showPayAccess(ctx, chatID)
	case "p":
		a.showPayPlace(ctx, chatID, arg)
	case "t":
		place, method, _ := strings.Cut(arg, ":")
		a.togglePayAccess(ctx, chatID, place, method)
	}
}

// showPayAccess — «Способы оплаты → Доступность»: выбор места.
func (a *App) showPayAccess(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, i18n.T(lang, "pacc.title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "pacc.btn_tg"), "pacc:p:"+payPlaceTG), btn(i18n.T(lang, "pacc.btn_web"), "pacc:p:"+payPlaceWeb)},
		navBack(lang, "menu:paymethods"),
	})
}

// showPayPlace — способы одного места с галочками.
func (a *App) showPayPlace(ctx context.Context, chatID int64, place string) {
	if place != payPlaceTG && place != payPlaceWeb {
		a.showPayAccess(ctx, chatID)
		return
	}
	lang := a.lang(chatID)
	web := place == payPlaceWeb
	var rows [][]models.InlineKeyboardButton
	shown := 0
	for _, m := range a.payPlaceMethods(web) {
		mark := "❌ "
		if a.payShown(m, web) {
			mark = "✅ "
			shown++
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+i18n.T(lang, "paym."+m), "pacc:t:"+place+":"+m)})
	}
	rows = append(rows, navBack(lang, "pacc:home"))
	key := "pacc.place_tg"
	if web {
		key = "pacc.place_web"
	}
	text := i18n.T(lang, key)
	if shown == 0 {
		text += "\n\n" + i18n.T(lang, "pacc.none")
	}
	a.sendKBSection(ctx, chatID, assets.SectionBuySubscription, text, rows)
}

func (a *App) togglePayAccess(ctx context.Context, chatID int64, place, method string) {
	web := place == payPlaceWeb
	if (place != payPlaceTG && !web) || !slices.Contains(payMethodOrder, method) || (web && method == model.PayMethodStars) {
		a.showPayAccess(ctx, chatID)
		return
	}
	a.mu.Lock()
	if a.botCfg != nil {
		off := &a.botCfg.PayAccess.TelegramOff
		if web {
			off = &a.botCfg.PayAccess.WebOff
		}
		if i := slices.Index(*off, method); i >= 0 {
			*off = slices.Delete(slices.Clone(*off), i, i+1)
		} else {
			*off = append(slices.Clone(*off), method)
		}
	}
	a.mu.Unlock()
	if err := a.saveBotConfig(ctx); err != nil {
		a.log.Warn("доступность способов оплаты не сохранена", "err", err)
	}
	a.showPayPlace(ctx, chatID, place)
}
