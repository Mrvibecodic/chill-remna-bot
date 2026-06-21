package app

import (
	"context"
	"errors"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

// miniPayURL creates a payment for an external method and returns a URL the
// Mini App can open: a Telegram invoice link (invoice=true → openInvoice) for
// Stars, or a payment-page/redirect URL (openLink) for the others. It reuses
// the SAME invoice-creation cores as the chat flow, so pending-invoice ExtID
// formats are identical and the existing webhooks complete the payment.
func (a *App) miniPayURL(ctx context.Context, tgID int64, months int, method string, web_ bool) (string, bool, error) {
	switch method {
	case model.PayMethodStars:
		link, err := a.starsInvoiceLink(ctx, tgID, months)
		return link, true, err

	case model.PayMethodYooKassa:
		cfg := a.ykConfig()
		pr := a.pricing()
		value := pr.Fiat(model.PayMethodYooKassa, months)
		if !cfg.Enabled || value == "" {
			return "", false, errors.New("оплата картой недоступна")
		}
		returnURL := cfg.ReturnURL
		if returnURL == "" {
			returnURL = "https://t.me"
		}
		currency := pr.Currency
		if len(currency) != 3 {
			currency = "RUB"
		}
		desc := miniDesc(months)
		url, _, err := a.ykCreatePayment(ctx, tgID, months, value, currency, returnURL, desc)
		return url, false, err

	case model.PayMethodCryptoBot:
		cfg := a.cbConfig()
		price := a.pricing().Base[months]
		if !cfg.Enabled || price == "" {
			return "", false, errors.New("оплата криптовалютой недоступна")
		}
		url, _, err := a.cbCreateInvoice(ctx, tgID, months, price, web_)
		return url, false, err

	case model.PayMethodPlatega:
		cfg := a.plConfig()
		pr := a.pricing()
		value := pr.Fiat(model.PayMethodPlatega, months)
		if !cfg.Enabled || value == "" {
			return "", false, errors.New("оплата недоступна")
		}
		returnURL := cfg.ReturnURL
		if returnURL == "" {
			returnURL = "https://t.me"
		}
		url, _, err := a.plCreateTransaction(ctx, tgID, months, parseAmountRub(value), miniDesc(months), returnURL)
		return url, false, err

	case model.PayMethodTribute:
		cfg := a.tributeCfg()
		if !cfg.Enabled || cfg.PayURL == "" {
			return "", false, errors.New("оплата недоступна")
		}
		if a.store != nil {
			_ = a.store.UpsertUser(ctx, tgID)
		}
		return cfg.PayURL, false, nil
	}
	return "", false, errors.New("неизвестный способ оплаты")
}

// MiniP2P starts the P2P flow for a Mini App checkout: it delivers the payment
// card (or the approval-needed notice) into the user's bot chat — exactly like
// the chat flow — and tells the Mini App to open the bot to finish (the
// screenshot upload and admin confirmation happen in the chat).
func (a *App) MiniP2P(ctx context.Context, tgID int64, months int) web.MiniActionDTO {
	if a.store == nil {
		return web.MiniActionDTO{Error: "хранилище недоступно"}
	}
	_ = a.store.UpsertUser(ctx, tgID)
	u, err := a.store.GetUser(ctx, tgID)
	if err != nil {
		return web.MiniActionDTO{Error: err.Error()}
	}
	if u == nil || !u.P2PApproved {
		a.notifyAdminUserRequest(ctx, tgID)
		return web.MiniActionDTO{Redirect: true, Message: "Доступ к P2P ещё не подтверждён. Запрос отправлен администратору — откройте бота."}
	}
	a.issueCardMonths(ctx, tgID, months)
	return web.MiniActionDTO{Redirect: true, Message: "Реквизиты для оплаты отправлены в бот. Откройте чат и завершите оплату."}
}

// miniDesc is a neutral invoice description for Mini App payments.
func miniDesc(months int) string {
	return "VPN " + itoaMonths(months)
}

func itoaMonths(m int) string {
	switch m {
	case 1:
		return "1 мес."
	case 3:
		return "3 мес."
	case 6:
		return "6 мес."
	case 12:
		return "12 мес."
	}
	return "подписка"
}

// MiniReferral mirrors showReferral: link, referral count and bonus terms.
func (a *App) MiniReferral(ctx context.Context, tgID int64) web.MiniReferralDTO {
	cfg := a.referralCfg()
	if !cfg.Enabled {
		return web.MiniReferralDTO{Enabled: false}
	}
	count := 0
	earned := int64(0)
	if a.store != nil {
		count, _ = a.store.CountReferrals(ctx, tgID)
		if u, _ := a.store.GetUser(ctx, tgID); u != nil {
			earned = u.RefEarned
		}
	}
	return web.MiniReferralDTO{
		Enabled:       true,
		Link:          a.referralLink(ctx, tgID),
		Count:         count,
		BonusValue:    cfg.BonusValue,
		BonusKind:     cfg.BonusKind,
		OnFirstPay:    cfg.OnFirstPay,
		EarnedKopecks: earned,
		InviteeKind:   cfg.InviteeKind,
		InviteeValue:  cfg.InviteeValue,
		Percent:       cfg.Percent,
	}
}

// MiniPromo applies a promo code via the shared redeemPromo core.
func (a *App) MiniPromo(ctx context.Context, tgID int64, code string) web.MiniPromoDTO {
	msg, ok := a.redeemPromo(ctx, tgID, code)
	return web.MiniPromoDTO{OK: ok, Message: msg}
}

// MiniTopUpOptions returns the same preset amounts as the chat top-up screen,
// plus the enabled top-up methods (YooKassa/CryptoBot).
func (a *App) MiniTopUpOptions(ctx context.Context, tgID int64) web.MiniTopUpOptionsDTO {
	var dto web.MiniTopUpOptionsDTO
	amts, _ := a.topUpAmounts()
	for _, k := range amts {
		dto.Amounts = append(dto.Amounts, web.MiniAmountDTO{Kopecks: k, Label: kopecksToRub(k) + curSuffix(curRUB)})
	}
	a.mu.Lock()
	if a.botCfg != nil {
		if a.botCfg.YooKassa.Enabled {
			dto.Methods = append(dto.Methods, "yk")
		}
		if a.botCfg.CryptoBot.Enabled {
			dto.Methods = append(dto.Methods, "cb")
		}
	}
	a.mu.Unlock()
	return dto
}

// MiniTopUp creates a balance top-up payment (preset amount + yk/cb) via the
// shared topUpCreate core and returns the payment URL.
func (a *App) MiniTopUp(ctx context.Context, tgID int64, kopecks int64, method string) web.MiniActionDTO {
	amts, maxK := a.topUpAmounts()
	valid := false
	for _, k := range amts {
		if k == kopecks {
			valid = true
			break
		}
	}
	if !valid || (maxK > 0 && kopecks > maxK) {
		return web.MiniActionDTO{Error: "недопустимая сумма"}
	}
	if method != "yk" && method != "cb" {
		return web.MiniActionDTO{Error: "способ пополнения недоступен"}
	}
	payURL, _, err := a.topUpCreate(ctx, tgID, kopecks, method)
	if err != nil {
		return web.MiniActionDTO{Error: err.Error()}
	}
	return web.MiniActionDTO{OK: true, PayURL: payURL}
}
