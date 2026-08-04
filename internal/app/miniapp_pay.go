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
	url, invoice, err := a.miniPayURLCore(ctx, tgID, months, method, web_)
	if err != nil {
		// Ядра создания счёта пишут свои invoice_error, но отказы «метод
		// недоступен/неизвестен» терялись совсем, а источник платежа (чат,
		// мини-апп, кабинет) не был виден нигде — без него разбор жалобы
		// «не смог оплатить» упирается в догадки.
		a.payLog(ctx, method, "", tgID, "checkout_error", "источник=%s months=%d: %v", miniSource(web_), months, err)
	}
	return url, invoice, err
}

// miniSource — откуда пришла попытка оплаты, для журнала.
func miniSource(web_ bool) string {
	if web_ {
		return "веб-кабинет"
	}
	return "мини-апп"
}

func (a *App) miniPayURLCore(ctx context.Context, tgID int64, months int, method string, web_ bool) (string, bool, error) {
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
		// В прайсе валюта задаётся символом («₽» — тоже три байта), поэтому
		// длины мало: нужен настоящий трёхбуквенный код, иначе ЮKassa вернёт 400.
		currency := pr.Currency
		if !currencyCode(currency) {
			currency = "RUB"
		}
		desc := miniDesc(months)
		url, _, err := a.ykCreatePayment(ctx, tgID, months, value, currency, returnURL, desc, a.autoPayAvailable())
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
		valueK, okV := rubToKopecks(value)
		if !okV || valueK <= 0 {
			return "", false, errors.New("оплата недоступна")
		}
		url, _, err := a.plCreateTransaction(ctx, tgID, months, float64(valueK)/100, miniDesc(months), returnURL)
		return url, false, err

	case model.PayMethodHeleket:
		price := a.pricing().Base[months]
		if !a.hlConfig().Enabled || price == "" {
			return "", false, errors.New("оплата криптовалютой недоступна")
		}
		url, _, err := a.hlCreateInvoice(ctx, tgID, months, price, "", 0)
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
	if !a.p2pAllowed(u) {
		a.notifyAdminUserRequest(ctx, tgID)
		return web.MiniActionDTO{Redirect: true, Message: "Доступ к P2P ещё не подтверждён. Запрос отправлен администратору — откройте бота."}
	}
	a.issueCardMonths(ctx, tgID, months)
	return web.MiniActionDTO{Redirect: true, Message: "Реквизиты для оплаты отправлены в бот. Откройте чат и завершите оплату."}
}

// MiniP2PWeb runs the P2P flow for the web cabinet: it returns the card + amount
// to show in the browser (the screenshot is uploaded back via the cabinet).
func (a *App) MiniP2PWeb(ctx context.Context, tgID int64, months int) web.MiniActionDTO {
	if a.store == nil {
		return web.MiniActionDTO{Error: "хранилище недоступно"}
	}
	_ = a.store.UpsertUser(ctx, tgID)
	u, _ := a.store.GetUser(ctx, tgID)
	// «Перевод всем без одобрения» распространяется на Telegram-аккаунты.
	// E-mail-аккаунт кабинета (отрицательный синтетический id) заводится без
	// подтверждения почты, поэтому реквизиты по нему выдаём только после
	// ручного одобрения — иначе карты вытягиваются регистрацией на любой ящик.
	allowed := a.p2pAllowed(u)
	if tgID < 0 {
		allowed = u != nil && u.P2PApproved
	}
	if !allowed {
		a.notifyAdminUserRequest(ctx, tgID)
		return web.MiniActionDTO{Redirect: true, Message: "Доступ к оплате переводом ещё не подтверждён администратором — запрос отправлен. Попробуйте позже."}
	}
	card, price, reqID, err := a.prepareP2PCard(ctx, tgID, months)
	if err != nil {
		return web.MiniActionDTO{Error: "оплата переводом недоступна"}
	}
	return web.MiniActionDTO{OK: true, P2PCard: card, P2PAmount: price + curSuffix(curRUB), P2PReqID: reqID}
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
// plus the enabled top-up methods (YooKassa/CryptoBot/Heleket).
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
		if a.botCfg.Heleket.Enabled {
			dto.Methods = append(dto.Methods, "hl")
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
		a.payLog(ctx, method, "", tgID, "topup_error", "недопустимая сумма kopecks=%d (максимум %d)", kopecks, maxK)
		return web.MiniActionDTO{Error: "недопустимая сумма"}
	}
	if method != "yk" && method != "cb" && method != "hl" {
		a.payLog(ctx, method, "", tgID, "topup_error", "способ пополнения недоступен (kopecks=%d)", kopecks)
		return web.MiniActionDTO{Error: "способ пополнения недоступен"}
	}
	payURL, _, err := a.topUpCreate(ctx, tgID, kopecks, method)
	if err != nil {
		a.payLog(ctx, method, "", tgID, "topup_error", "kopecks=%d: %v", kopecks, err)
		return web.MiniActionDTO{Error: err.Error()}
	}
	return web.MiniActionDTO{OK: true, PayURL: payURL}
}

// MiniAutoPay отдаёт состояние автопродления для мини-аппа и веб-кабинета.
func (a *App) MiniAutoPay(ctx context.Context, tgID int64) web.MiniAutoPayDTO {
	available, on, months, title := a.AutoPayState(ctx, tgID)
	dto := web.MiniAutoPayDTO{
		Available: available,
		On:        on,
		Months:    months,
		Title:     title,
		Days:      a.autoPayCfg().AutoPayDays,
	}
	if ap := a.getAutoPay(ctx, tgID); ap != nil && ap.MethodID != "" {
		dto.CanEnable = available
	}
	return dto
}

// MiniSetAutoPay включает/выключает автопродление из мини-аппа или кабинета.
func (a *App) MiniSetAutoPay(ctx context.Context, tgID int64, on bool) web.MiniActionDTO {
	if err := a.SetAutoPayEnabled(ctx, tgID, on); err != nil {
		return web.MiniActionDTO{Error: err.Error()}
	}
	return web.MiniActionDTO{OK: true}
}
