package app

import (
	"context"
	"strings"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
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
			dto.Name = displayName(u.FirstName, u.Username)
		}
		if tgID < 0 {
			if wu, _ := a.store.GetWebUserByTgID(ctx, tgID); wu != nil {
				dto.Email = wu.Email
			}
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
		if c.Heleket.Enabled {
			dto.PayMethods = append(dto.PayMethods, model.PayMethodHeleket)
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
	// Same add-on state the chat screen shows, so the mini-app and the cabinet
	// don't hide a доп-сервер that ran out of traffic.
	if add, aok := a.addSubStatus(ctx, tgID); aok {
		dto.AddSubOK = true
		dto.AddSubUsed = add.Used
		dto.AddSubLimit = add.Limit
		dto.AddSubExhausted = add.Exhausted
		dto.AddSubOff = strings.EqualFold(add.Status, remnawave.StatusDisabled)
		// Название опции — тарифа пользователя (или общее).
		dto.AddSubName = a.userAddSubName(ctx, tgID)
	}
	return dto
}

// MiniPlans mirrors the chat storefront (showPlans): тарифы, доступные этому
// покупателю, каждый со своими сроками и условиями.
func (a *App) MiniPlans(ctx context.Context, tgID int64) web.MiniPlansDTO {
	var dto web.MiniPlansDTO
	// Первая точка гейта доступности — сама витрина: тарифы фильтруются по
	// покупателю, скрытые «по ссылке» не показываются.
	plans, _, err := a.storefrontPlans(ctx, tgID)
	if err != nil {
		a.log.Warn("мини-апп: тарифы не прочитаны", "err", err, "user", tgID)
		return dto
	}
	lang := a.lang(tgID)
	fallbackCur := a.pricing().Currency
	for i := range plans {
		p := &plans[i]
		pd := web.MiniPlanDTO{
			Code:        p.Code,
			Name:        p.Name,
			Description: p.Description,
			Icon:        p.Icon,
			Strategy:    p.Strategy,
		}
		if a.planAddSubOn(p) {
			pd.AddSubName, pd.AddSubDesc = a.addSubTexts(lang, p)
		}
		cur := planCurrencyOr(p, fallbackCur)
		// Лучшая цена за месяц — подсветка «выгодного» (раньше фронт жёстко
		// подсвечивал третью из четырёх позиций).
		bestIdx, bestRate := -1, int64(0)
		for j := range p.Durations {
			d := &p.Durations[j]
			if d.Months <= 0 || d.Base == "" {
				continue
			}
			// squadCountries ходит в панель (кэш) и сам берёт a.mu.
			cs, configs := a.squadCountries(ctx, p.IntSquadsFor(d))
			var countries []web.MiniCountryDTO
			for _, c := range cs {
				countries = append(countries, web.MiniCountryDTO{Flag: c.Flag, Code: c.Code, Name: c.Name})
			}
			pd.Durations = append(pd.Durations, web.MiniDurationDTO{
				Months:    d.Months,
				Price:     d.Base,
				Currency:  cur,
				TrafficGB: p.TrafficGBFor(d),
				Devices:   p.DeviceLimitFor(d),
				Countries: countries,
				Configs:   configs,
			})
			if k, ok := rubToKopecks(d.Base); ok && k > 0 {
				rate := k / int64(d.Months)
				if bestIdx < 0 || rate < bestRate {
					bestIdx, bestRate = len(pd.Durations)-1, rate
				}
			}
		}
		if bestIdx >= 0 && len(pd.Durations) > 1 {
			pd.Durations[bestIdx].Best = true
		}
		if len(pd.Durations) == 0 {
			continue
		}
		dto.Plans = append(dto.Plans, pd)
	}
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

// miniSale разрешает пару «тариф + срок» из запроса мини-аппа/кабинета в
// продажу. nil — продавать нечего: неизвестный код, срок снят с продажи,
// тариф выключен или закрыт этому покупателю. Правила зеркалят чат: витрина
// такого не предлагает, значит и счёт по прямому запросу не создаётся.
func (a *App) miniSale(ctx context.Context, tgID int64, code string, months int) *sale {
	if code == "" || code == model.PlanCodeBase {
		// Пустой код — старый фронт из кэша: он знает только «Базовый».
		// Тот же признак, что у витрины: срок без базовой цены снят с продажи.
		if !a.periodOnSale(months) {
			return nil
		}
		// Гейт здесь — baseSaleAllowed, а не planAccessibleFor: у мини-аппа нет
		// экрана тарифа по ссылке, поэтому «Базовый» в режиме «по ссылке» из
		// него не продаётся вовсе (покупка по ссылке живёт в чате).
		if !a.baseSaleAllowed(ctx, tgID) {
			return nil
		}
		return baseSale(months)
	}
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil || !p.Enabled {
		return nil
	}
	// «По ссылке» продаётся только со своего экрана в чате: мини-апп такие
	// тарифы не показывает, и API не должен становиться обходом скрытности.
	if model.NormalizeAvailability(p.Availability) == model.PlanAvailLink {
		return nil
	}
	if !a.planAccessibleFor(ctx, p, tgID) {
		return nil
	}
	d := p.Duration(months)
	if d == nil || d.Base == "" {
		return nil
	}
	return &sale{Plan: p, D: d, Months: months}
}

// MiniCheckout buys/renews a plan duration. Only the "balance" method
// completes in-app (reuses finalizePurchase, the same provisioning core as
// the chat flow); other methods return a payment URL or Redirect=true.
func (a *App) MiniCheckout(ctx context.Context, tgID int64, plan string, months int, method string, web_ bool) web.MiniActionDTO {
	// Вторая точка гейта доступности: создание счёта. Без неё авторизованный
	// пользователь покупал бы тариф, недоступный ему по режиму, прямым
	// запросом мимо витрины.
	s := a.miniSale(ctx, tgID, plan, months)
	if s == nil {
		return web.MiniActionDTO{Error: "тариф недоступен"}
	}
	if method == model.PayMethodP2P {
		if web_ {
			return a.MiniP2PWeb(ctx, tgID, s)
		}
		return a.MiniP2P(ctx, tgID, s)
	}
	if method != model.PayMethodBalance {
		payURL, invoice, err := a.miniPayURL(ctx, tgID, s, method, web_)
		if err != nil {
			return web.MiniActionDTO{Error: err.Error()}
		}
		return web.MiniActionDTO{OK: true, PayURL: payURL, Invoice: invoice}
	}

	priceStr := a.saleBase(s)
	kopecks, ok := rubToKopecks(priceStr)
	// Баланс живёт в рублях: тариф в другой валюте с баланса не продаётся —
	// иначе «5 $» молча списались бы как «5 ₽».
	if priceStr == "" || !ok || kopecks <= 0 || !a.saleGridCurrency(s) {
		return web.MiniActionDTO{Error: "тариф недоступен"}
	}
	if a.store == nil {
		return web.MiniActionDTO{Error: "хранилище недоступно"}
	}
	// Снимок — до списания (как в чате): после DeductBalance любой отказ
	// означает возврат денег, и лишних причин отказа быть не должно.
	snap := a.saleSnapshot(s)
	deducted, err := a.store.DeductBalance(ctx, tgID, kopecks)
	if err != nil {
		return web.MiniActionDTO{Error: err.Error()}
	}
	if !deducted {
		return web.MiniActionDTO{Error: "недостаточно средств на балансе"}
	}
	link, expireAt, err := a.finalizePurchase(ctx, tgID, months, "balance", priceStr+curSuffix(curRUB), "", snap)
	if err != nil {
		_ = a.store.AddBalance(ctx, tgID, kopecks) // refund on provisioning failure
		return web.MiniActionDTO{Error: err.Error()}
	}
	return web.MiniActionDTO{OK: true, SubURL: link, ExpireAt: formatExpire(expireAt, a.lang(tgID))}
}
