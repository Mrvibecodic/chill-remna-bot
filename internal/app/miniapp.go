package app

import (
	"context"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

// This file implements web.MiniProvider: thin, read-mostly adapters that expose
// the bot's EXISTING data/predicates to the Mini App API. No business logic is
// duplicated here — every value mirrors what the chat bot already computes, so
// the Mini App can never offer an action the bot doesn't have.

// MiniEnabled reports the Mini App feature flag.
func (a *App) MiniEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.botCfg != nil && a.botCfg.MiniApp.Enabled
}

// MiniBotToken returns the Telegram bot token (used for init-data validation).
func (a *App) MiniBotToken() string { return a.cfg.BotToken }

// MiniMe returns the user's basic profile (balance, language).
func (a *App) MiniMe(ctx context.Context, tgID int64) web.MiniMeDTO {
	dto := web.MiniMeDTO{TgID: tgID, Lang: a.lang(tgID)}
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, tgID); u != nil {
			dto.BalanceK = u.Balance
		}
	}
	return dto
}

// MiniMenu mirrors navRow: it reports exactly which actions the chat bot would
// offer this user, plus the enabled payment methods and contact links.
func (a *App) MiniMenu(ctx context.Context, tgID int64, web_ bool) web.MiniMenuDTO {
	dto := web.MiniMenuDTO{
		HasSub:         a.userHasSub(ctx, tgID),
		TrialAvailable: a.trialAvailable(ctx, tgID),
		ReferralOn:     a.referralCfg().Enabled,
		SupportURL:     a.supportURL(),
	}
	if dto.HasSub {
		dto.CanRenew = a.renewEligible(ctx, tgID)
	}
	a.mu.Lock()
	if a.botCfg != nil {
		c := a.botCfg
		dto.GroupURL = c.Contact.GroupURL
		if c.Stars.Enabled && !web_ {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodStars)
		}
		if c.YooKassa.Enabled {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodYooKassa)
		}
		if c.CryptoBot.Enabled {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodCryptoBot)
		}
		if c.Platega.Enabled {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodPlatega)
		}
		if c.Tribute.Enabled {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodTribute)
		}
		if c.P2P.Enabled {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodP2P)
		}
	}
	a.mu.Unlock()
	return dto
}

// MiniSubscription mirrors showMySubs: link, expiry, status, and the read-only
// devices count (only the connected number is sent when no per-user limit).
func (a *App) MiniSubscription(ctx context.Context, tgID int64) web.MiniSubDTO {
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	var dto web.MiniSubDTO
	if panel == nil {
		return dto
	}
	url, expireAt, status, ok := panel.SubscriptionFull(ctx, tgID)
	if !ok {
		return dto
	}
	dto.Active = true
	dto.Status = status
	dto.SubURL = a.rewriteSub(url)
	dto.ExpireAt = formatExpire(expireAt, a.lang(tgID))
	if t, err := time.Parse(time.RFC3339, expireAt); err == nil {
		dto.ExpireTS = t.Unix()
	}
	if info, dok := panel.DevicesByTelegramID(ctx, tgID); dok {
		dto.DevicesOK = true
		dto.DevicesUsed = info.Used
		dto.DeviceLimit = info.Limit
		dto.HasLimit = info.HasLimit
	}
	return dto
}

// MiniPlans mirrors the chat storefront (showPlans): period + base price
// only — the bot does not show traffic/device details in the plan list.
func (a *App) MiniPlans(ctx context.Context, tgID int64) web.MiniPlansDTO {
	a.mu.Lock()
	var dto web.MiniPlansDTO
	if a.botCfg == nil {
		a.mu.Unlock()
		return dto
	}
	p := a.botCfg.Pricing
	type planRow struct {
		months           int
		price            string
		traffic, devices int
	}
	var rows []planRow
	for _, m := range model.PlanMonths {
		price := p.Base[m]
		if price == "" {
			continue
		}
		rows = append(rows, planRow{m, price, p.Traffic[m], p.DeviceLimitFor(m)})
	}
	currency := p.Currency
	strategy := p.ResetStrategy()
	a.mu.Unlock()

	// planCountries hits the panel (cached) and locks a.mu internally, so it
	// must run AFTER releasing the lock above.
	for _, r := range rows {
		cs, configs := a.planCountries(ctx, r.months)
		var countries []web.MiniCountryDTO
		for _, c := range cs {
			countries = append(countries, web.MiniCountryDTO{Flag: c.Flag, Code: c.Code, Name: c.Name})
		}
		dto.Plans = append(dto.Plans, web.MiniPlanDTO{
			Months:    r.months,
			Price:     r.price,
			Currency:  currency,
			TrafficGB: r.traffic,
			Devices:   r.devices,
			Countries: countries,
			Configs:   configs,
		})
	}
	dto.Strategy = strategy
	return dto
}

// MiniTrial activates the free trial (mirrors activateTrial's core). Read of
// availability uses the same predicate as the chat bot.
func (a *App) MiniTrial(ctx context.Context, tgID int64) web.MiniActionDTO {
	if !a.trialAvailable(ctx, tgID) {
		return web.MiniActionDTO{Error: "триал недоступен"}
	}
	link, expireAt, err := a.trialProvision(ctx, tgID)
	if err != nil {
		return web.MiniActionDTO{Error: err.Error()}
	}
	return web.MiniActionDTO{OK: true, SubURL: link, ExpireAt: formatExpire(expireAt, a.lang(tgID))}
}

// MiniCheckout buys/renews a period. Only the "balance" method completes
// in-app (reuses finalizePurchase, the same provisioning core as the chat
// flow); other methods return Redirect=true (handled in a later stage).
func (a *App) MiniCheckout(ctx context.Context, tgID int64, months int, method string, web_ bool) web.MiniActionDTO {
	valid := false
	for _, m := range model.PlanMonths {
		if m == months {
			valid = true
			break
		}
	}
	if !valid {
		return web.MiniActionDTO{Error: "неверный период"}
	}
	if method == model.PayMethodP2P {
		if web_ && tgID < 0 {
			return web.MiniActionDTO{Error: "оплата переводом для email-аккаунтов появится позже — используйте карту или криптовалюту"}
		}
		return a.MiniP2P(ctx, tgID, months)
	}
	if method != model.PayMethodBalance {
		payURL, invoice, err := a.miniPayURL(ctx, tgID, months, method, web_)
		if err != nil {
			return web.MiniActionDTO{Error: err.Error()}
		}
		return web.MiniActionDTO{OK: true, PayURL: payURL, Invoice: invoice}
	}

	priceStr := a.pricing().Base[months]
	kopecks, ok := rubToKopecks(priceStr)
	if priceStr == "" || !ok || kopecks <= 0 {
		return web.MiniActionDTO{Error: "тариф недоступен"}
	}
	if a.store == nil {
		return web.MiniActionDTO{Error: "хранилище недоступно"}
	}
	deducted, err := a.store.DeductBalance(ctx, tgID, kopecks)
	if err != nil {
		return web.MiniActionDTO{Error: err.Error()}
	}
	if !deducted {
		return web.MiniActionDTO{Error: "недостаточно средств на балансе"}
	}
	link, expireAt, err := a.finalizePurchase(ctx, tgID, months, "balance", priceStr+curSuffix(curRUB), "")
	if err != nil {
		_ = a.store.AddBalance(ctx, tgID, kopecks) // refund on provisioning failure
		return web.MiniActionDTO{Error: err.Error()}
	}
	return web.MiniActionDTO{OK: true, SubURL: link, ExpireAt: formatExpire(expireAt, a.lang(tgID))}
}
