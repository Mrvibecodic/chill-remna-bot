package app

import (
	"context"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/web"
)

// То, что чат умел, а мини-апп и кабинет — нет: вход по ссылкам (приглашение,
// реферал, тариф по ссылке), продление своего тарифа, прогноз кошелька и
// ручная перепроверка оплаты. Логика везде та же, что в чате, — здесь только
// переходники к ней.

const (
	// linkPlanTTL — сколько открытый по ссылке тариф остаётся доступным к
	// покупке из мини-аппа и кабинета.
	linkPlanTTL = 24 * time.Hour
	// payCheckEvery — не чаще одной ручной перепроверки на человека.
	payCheckEvery = 5 * time.Second
	// payCheckMax — сколько последних счетов проверить за раз.
	payCheckMax = 5
)

// MiniStart — параметр ссылки входа в мини-апп или кабинет: то же, что /start
// в чате. Приглашение гасится до проверки режима публичности (иначе оно не
// сработало бы никогда), реферал привязывается только новичку, а аккаунт
// панели, заведённый раньше бота, находится и привязывается. Аккаунтам по
// почте (отрицательный id) ничего из этого не положено.
//
// cabinet — вход через кабинет: если там новичков одобряет админ, до
// одобрения ни реферал, ни поиск в панели не делаются.
func (a *App) MiniStart(ctx context.Context, tgID int64, param string, cabinet bool) string {
	if a.store == nil || tgID <= 0 {
		return ""
	}
	param = strings.TrimSpace(param)
	notice := ""
	if code, ok := strings.CutPrefix(param, "inv_"); ok && code != "" {
		if msg, _ := a.redeemInvite(ctx, tgID, code); msg != "" {
			notice = stripHTMLTags(msg)
		}
	}
	if a.userBlocked(ctx, tgID) || a.MiniAccessDenied(ctx, tgID) {
		return notice
	}
	if cabinet && a.cabinetNeedsApproval(false) {
		if u, _ := a.store.GetUser(ctx, tgID); u == nil || !u.WebApproved {
			return notice
		}
	}
	a.bindReferrer(ctx, tgID, param)
	// Синхронно, как /start в чате: иначе человек успел бы взять триал до
	// того, как найдётся его аккаунт в панели.
	a.syncPanelAccount(ctx, tgID)
	return notice
}

// ownPlanView — тариф последней покупки для «Продлить»: сценарии те же, что у
// showRenew в чате.
func (a *App) ownPlanView(ctx context.Context, tgID int64, lang string) (code, note string, gone bool) {
	snap := a.userSnapshot(ctx, tgID)
	code = planCodeOf(snap)
	if snap == nil || code == "" {
		return "", "", false
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		return "", "", false
	}
	if p == nil && code == model.PlanCodeBase {
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	if p == nil || !p.Enabled || !planSellsAnything(p) || !a.planAccessibleFor(ctx, p, tgID) {
		return code, "", true
	}
	if a.renewTermsChanged(p, snap) {
		note = stripHTMLTags(i18n.T(lang, "renew.terms_changed"))
	}
	return code, note, false
}

// ownLinkPlan — свой тариф (code — из снимка последней сделки), спрятанный
// режимом «по ссылке». Витрина его не показывает, но продление своего не
// отрезается (как в чате).
func (a *App) ownLinkPlan(ctx context.Context, code string) *model.Plan {
	if code == "" {
		return nil
	}
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil || !p.Enabled || !planSellsAnything(p) {
		return nil
	}
	if model.NormalizeAvailability(p.Availability) != model.PlanAvailLink {
		return nil
	}
	return p
}

// linkPlanOpen отмечает, что человек открыл тариф по ссылке.
func (a *App) linkPlanOpen(tgID int64, code string) {
	now := time.Now()
	a.webMu.Lock()
	defer a.webMu.Unlock()
	if a.linkPlans == nil {
		a.linkPlans = map[int64]map[string]time.Time{}
	}
	// Подметаем устаревшие отметки здесь же: карта растёт только отсюда.
	for id, m := range a.linkPlans {
		for c, t := range m {
			if now.Sub(t) > linkPlanTTL {
				delete(m, c)
			}
		}
		if len(m) == 0 {
			delete(a.linkPlans, id)
		}
	}
	m := a.linkPlans[tgID]
	if m == nil {
		m = map[string]time.Time{}
		a.linkPlans[tgID] = m
	}
	m[code] = now
}

// linkPlanOpened — открывал ли человек этот тариф по ссылке недавно.
func (a *App) linkPlanOpened(tgID int64, code string) bool {
	a.webMu.Lock()
	defer a.webMu.Unlock()
	t, ok := a.linkPlans[tgID][code]
	return ok && time.Since(t) <= linkPlanTTL
}

// linkSaleAllowed — можно ли продать тариф режима «по ссылке»: это свой тариф
// человека или он открыл его ссылку.
func (a *App) linkSaleAllowed(ctx context.Context, tgID int64, code string) bool {
	return a.userPlanCode(ctx, tgID) == code || a.linkPlanOpened(tgID, code)
}

// baseLinkSale — «Базовый» в режиме «по ссылке»: продаётся тому, кто открыл
// его ссылку или продлевает свой.
func (a *App) baseLinkSale(ctx context.Context, tgID int64) bool {
	p := a.basePlanRow(ctx)
	if p == nil || !p.Enabled || model.NormalizeAvailability(p.Availability) != model.PlanAvailLink {
		return false
	}
	return a.linkSaleAllowed(ctx, tgID, model.PlanCodeBase)
}

// MiniPlanLink открывает тариф по ссылке — тем же путём, что openPlanLink в
// чате: общий лимит перебора и один ответ «недоступен» на все отказы.
func (a *App) MiniPlanLink(ctx context.Context, tgID int64, code string) web.MiniPlansDTO {
	lang := a.lang(tgID)
	unknown := web.MiniPlansDTO{Notice: stripHTMLTags(i18n.T(lang, "plans.link_unknown"))}
	if a.planLinkThrottled(tgID) || a.planLinkGlobalBlocked(ctx, tgID) {
		return unknown
	}
	if !model.ValidPlanCode(code) {
		a.planLinkFail(tgID)
		return unknown
	}
	if expireAt, locked := a.trialBuyLock(ctx, tgID); locked {
		return web.MiniPlansDTO{Notice: i18n.T(lang, "buy.trial_locked_plain", formatExpire(expireAt, lang))}
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		return web.MiniPlansDTO{Notice: stripHTMLTags(i18n.T(lang, "err.storage"))}
	}
	if p == nil && code == model.PlanCodeBase {
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	if p == nil || !p.Enabled || !planSellsAnything(p) || !a.planAccessibleFor(ctx, p, tgID) {
		a.planLinkFail(tgID)
		return unknown
	}
	pd, ok := a.miniPlanDTO(ctx, tgID, p, a.miniPlanCtx(ctx, tgID))
	if !ok {
		a.planLinkFail(tgID)
		return unknown
	}
	a.linkPlanOpen(tgID, p.Code)
	return web.MiniPlansDTO{Plans: []web.MiniPlanDTO{pd}}
}

// MiniWallet — кошелёк: баланс и прогноз «на сколько хватит», как в чате.
func (a *App) MiniWallet(ctx context.Context, tgID int64) web.MiniWalletDTO {
	dto := web.MiniWalletDTO{TopUpOn: a.topUpEnabled(), Currency: curRUB}
	dto.BalanceK = a.userBalance(ctx, tgID)
	list, plan := a.forecastEntries(ctx, tgID)
	if plan != nil {
		dto.PlanName = strings.TrimSpace(plan.Icon + " " + plan.Name)
	}
	for _, e := range list {
		k, ok := rubToKopecks(e.price)
		if !ok || k <= 0 {
			continue
		}
		count := int(dto.BalanceK / k)
		row := web.MiniForecastRowDTO{Months: e.months, Price: e.price, Count: count, Total: count * e.months}
		if row.Total > dto.MaxMonths {
			dto.MaxMonths = row.Total
		}
		dto.Rows = append(dto.Rows, row)
	}
	return dto
}

// MiniPayCheck — «Проверить оплату» из мини-аппа и кабинета: незакрытые счета
// человека перепроверяются у платёжек тем же ядром, что и фоновая сверка, и
// оплаченные добиваются. Не чаще раза в несколько секунд на человека.
func (a *App) MiniPayCheck(ctx context.Context, tgID int64) web.MiniPayCheckDTO {
	st := a.store
	if st == nil {
		return web.MiniPayCheckDTO{Error: "хранилище недоступно"}
	}
	now := time.Now()
	a.webMu.Lock()
	if a.payChecks == nil {
		a.payChecks = map[int64]time.Time{}
	}
	last, seen := a.payChecks[tgID]
	busy := seen && now.Sub(last) < payCheckEvery
	if !busy {
		for id, t := range a.payChecks {
			if now.Sub(t) > time.Minute {
				delete(a.payChecks, id)
			}
		}
		a.payChecks[tgID] = now
	}
	a.webMu.Unlock()
	// Старше суток сверка счета уже не проверяет.
	since := now.UTC().Add(-reconcileGiveUp).Format(time.RFC3339)
	list, err := st.ListUserPending(ctx, tgID, since, payCheckMax)
	if err != nil {
		return web.MiniPayCheckDTO{Error: stripHTMLTags(a.clientErr(ctx, tgID, "мини-апп", err))}
	}
	dto := web.MiniPayCheckDTO{OK: true}
	// Выдача не должна обрываться, если человек закрыл мини-апп посреди
	// проверки: подписка в панели уже продлена, а платёж ещё не записан — и
	// следующая сверка выдала бы дни второй раз.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	for i := range list {
		pi := &list[i]
		if !busy {
			// Сверка сама закрывает уже оплаченный счёт и добивает недошедший.
			a.reconcileInvoice(rctx, st, pi)
		}
		if done, _ := st.PaymentByExtID(rctx, pi.ExtID); done {
			dto.Paid = true
		}
	}
	if left, err := st.ListUserPending(rctx, tgID, since, payCheckMax); err == nil {
		dto.Pending = len(left)
	}
	return dto
}
