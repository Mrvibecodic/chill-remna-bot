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
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
)

const (
	purposeTraffic = "traffic"
	purposePack    = "pack"
)

// Платёж с нулём месяцев старый образ бота не считает покупкой подписки.
const trafficPaymentMonths = 1

var (
	errTrafficSnap   = errors.New("сделка трафика без условий трафика")
	errPackNotOwner  = errors.New("тарифа пакета у покупателя нет")
	errPackUnlimited = errors.New("у подписки безлимитный трафик")
	errNoPanelUser   = errors.New("учётки в панели нет")
)

func (a *App) trafficSnapshotOf(p *model.Plan, pack *model.PlanPack) *model.PlanSnapshot {
	t := trafficTerms(p)
	sold := a.planAddSubOn(p)
	snap := &model.PlanSnapshot{
		Code:        p.Code,
		Name:        p.Name,
		Kind:        model.PlanKindTraffic,
		TrafficGB:   p.TrafficGB,
		DeviceLimit: p.DeviceLimit,
		Strategy:    "NO_RESET",
		IntSquads:   append([]string(nil), p.IntSquads...),
		ExtSquad:    p.ExtSquad,
		AddSub:      &sold,
		Currency:    p.Currency,
		Price:       t.Price,
	}
	if sold {
		snap.AddSubTrafficGB, snap.AddSubSquads = planAddSubTerms(p)
		snap.AddSubStrategy = p.AddSubStrategyOrDefault()
	}
	if snap.Currency == "" {
		snap.Currency = a.pricing().Currency
	}
	if pack != nil {
		snap.PackID, snap.PackGB, snap.Price = pack.ID, pack.GB, pack.Price
	}
	return snap
}

func (a *App) trafficOwner(ctx context.Context, tgID int64) *model.Plan {
	code := planCodeOf(a.userSnapshot(ctx, tgID))
	if code == "" || code == model.PlanCodeBase {
		return nil
	}
	p, err := a.planByCode(ctx, code)
	if err != nil || !p.IsTraffic() {
		return nil
	}
	return p
}

// trafficDeal — что продаётся человеку по коду тарифа и пакета: тариф —
// включённый и доступный ему, пакет — только владельцу тарифа, даже
// выключенного. nil — не продаётся.
func (a *App) trafficDeal(ctx context.Context, uid int64, code, packID string) (*model.Plan, *model.PlanPack, error) {
	p, err := a.planByCode(ctx, code)
	if err != nil || !p.IsTraffic() {
		return nil, nil, err
	}
	if packID != "" {
		pk := p.Pack(packID)
		if owner := a.trafficOwner(ctx, uid); !packSells(pk) || owner == nil || owner.Code != p.Code {
			return nil, nil, nil
		}
		cp := *pk
		return p, &cp, nil
	}
	if !p.Enabled || !trafficPlanSells(p) || !a.planAccessibleFor(ctx, p, uid) {
		return nil, nil, nil
	}
	return p, nil, nil
}

func (a *App) onTrafficPlan(ctx context.Context, tgID int64) bool {
	snap := a.userSnapshot(ctx, tgID)
	if snap.IsTraffic() {
		return true
	}
	return a.trafficOwner(ctx, tgID) != nil
}

func (a *App) setBuyIntentTraffic(ctx context.Context, chatID int64, planCode, packID, shownPrice string) error {
	st := a.storeRef()
	if st == nil {
		return nil
	}
	return st.SetPurchaseIntent(ctx, &model.PurchaseIntent{
		TelegramID: chatID,
		PlanCode:   planCode,
		PackID:     packID,
		ShownPrice: shownPrice,
	})
}

func (a *App) forgetTrafficIntent(ctx context.Context, chatID int64, code, packID string) {
	st := a.storeRef()
	if st == nil || chatID == 0 {
		return
	}
	in, err := st.PurchaseIntent(ctx, chatID)
	if err != nil || in == nil || in.Months != 0 || in.PlanCode != code || in.PackID != packID {
		return
	}
	_ = st.DeletePurchaseIntentFor(ctx, chatID, 0, in.CreatedAt)
}

func (a *App) invoiceAny(ctx context.Context, st storage.Storage, extID string) (inv *model.PendingInvoice, traffic bool) {
	if p, _ := st.PendingByExtID(ctx, extID); p != nil {
		return p, false
	}
	if p, _ := st.TrafficInvoiceByExtID(ctx, extID); p != nil {
		return p, true
	}
	return nil, false
}

func (a *App) resolveExt(ctx context.Context, st storage.Storage, extID string) {
	inv, traffic := a.invoiceAny(ctx, st, extID)
	resolveAny(ctx, st, inv, traffic)
}

func resolveAny(ctx context.Context, st storage.Storage, inv *model.PendingInvoice, traffic bool) {
	if inv == nil {
		return
	}
	if traffic {
		_ = st.ResolveTraffic(ctx, inv.ID)
		return
	}
	_ = st.ResolvePending(ctx, inv.ID)
}

func (a *App) closeTrafficDeal(ctx context.Context, telegramID int64, method, amount, extID string, snap *model.PlanSnapshot, grant bool) (dealDone, error) {
	var d dealDone
	if !snap.IsTraffic() || snap.Code == "" {
		return d, errTrafficSnap
	}
	if a.orphanInvoice(ctx, method, extID, telegramID, amount) {
		return d, errOrphanPayment
	}
	if snap.PackID != "" {
		return a.finalizePack(ctx, telegramID, method, amount, extID, snap, grant)
	}
	breach := ""
	if !grant {
		breach = a.planGateBreach(ctx, telegramID, snap.Code)
	}
	link, expireAt, applied, err := a.finalizeTrafficCore(ctx, telegramID, method, amount, extID, snap, grant)
	d.link, d.expireAt, d.applied = link, expireAt, applied
	if err != nil {
		return d, err
	}
	a.syncAddSubSnap(ctx, telegramID, true, applied)
	a.autoPayOffForTraffic(ctx, telegramID, method, applied)
	if !grant {
		a.notifyPlanGateBreach(ctx, telegramID, applied, breach)
		a.trafficRefBonus(ctx, telegramID, method, extID, applied)
	}
	return d, nil
}

func (a *App) autoPayOffForTraffic(ctx context.Context, tgID int64, method string, applied *model.PlanSnapshot) {
	ap := a.getAutoPay(ctx, tgID)
	st := a.storeRef()
	if ap == nil || st == nil {
		return
	}
	if applied != nil && planCodeOf(ap.Snapshot) != applied.Code {
		if err := st.UpdateAutoPaySnapshot(ctx, tgID, applied); err != nil {
			a.log.Warn("autopay: снимок не обновлён после трафик-тарифа", "tg_id", tgID, "err", err)
		}
	}
	if !ap.Enabled {
		return
	}
	if err := st.SetAutoPayEnabled(ctx, tgID, false); err != nil {
		a.log.Warn("autopay: не выключено после трафик-тарифа", "tg_id", tgID, "err", err)
		return
	}
	a.payLog(ctx, ap.Method, "", tgID, "autopay_off", "выключено: трафик-тариф не продлевается (%s)", methodLabel(method))
	a.notify(ctx, tgID, i18n.T(a.lang(tgID), "ap.off_traffic"))
}

func (a *App) finalizeTrafficCore(ctx context.Context, telegramID int64, method, amount, extID string, snap *model.PlanSnapshot, grant bool) (string, string, *model.PlanSnapshot, error) {
	if a.holdRestoredPayment(ctx, method, purposeTraffic, 0, amount, telegramID, extID) {
		return "", "", nil, a.restoreHeldErr(telegramID)
	}
	unlock, dup := a.lockDeal(ctx, telegramID, method, extID)
	defer unlock()
	if dup {
		return "", "", nil, storage.ErrDuplicateExtID
	}
	cp := *snap
	snap = &cp
	snap.Months, snap.Days, snap.BoughtDays, snap.WindowPaidK, snap.SwitchDays = 0, 0, 0, 0, 0
	snap.PackID, snap.PackGB = "", 0
	snap.Strategy = "NO_RESET"
	if paid := paidRub(amount); paid != "" {
		snap.Paid = paid
	}
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	a.payLog(ctx, method, extID, telegramID, "finalize", "трафик-тариф %s: %d GB, amount=%s", snap.Code, snap.TrafficGB, amount)
	if panel == nil {
		a.payLog(ctx, method, extID, telegramID, "error", "панель не подключена")
		return "", "", nil, fmt.Errorf("панель не подключена")
	}
	r, err := panel.GrantTraffic(ctx, telegramID, remnawave.UserLimits{
		InternalSquads: snap.IntSquads,
		ExternalSquad:  snap.ExtSquad,
		TrafficBytes:   snap.TrafficBytes(),
		TrafficSet:     true,
		DeviceLimit:    snap.DeviceLimit,
		Strategy:       "NO_RESET",
	})
	if err != nil {
		a.payLog(ctx, method, extID, telegramID, "panel_error", "%v", err)
		return "", "", nil, err
	}
	a.payLog(ctx, method, extID, telegramID, "panel_ok", "expire=%s", r.ExpireAt)
	if r.ResetErr != nil {
		a.payLog(ctx, method, extID, telegramID, "error", "трафик не сброшен после выдачи трафик-тарифа: %v", r.ResetErr)
		a.notify(ctx, a.cfg.AdminID, i18n.T(a.lang(a.cfg.AdminID), "admin.traffic_reset_failed", a.userLabelByID(ctx, telegramID)))
	}
	link := a.rewriteSub(r.Link)
	a.invalidateSubCache(telegramID)
	if err := a.recordDeal(ctx, &model.Payment{
		TelegramID: telegramID, Method: method, Months: trafficPaymentMonths, Amount: amount,
		Status: model.PaymentPaid, ExtID: extID, Snapshot: snap,
	}, "трафик выдан"); err != nil {
		return "", "", nil, err
	}
	if a.store != nil {
		_ = a.store.SetSubExpiry(ctx, telegramID, r.ExpireAt, "paid")
		_ = a.store.SetUserSnapshot(ctx, telegramID, snap)
		_ = a.store.SetTrafficBonus(ctx, telegramID, nil)
	}
	a.payLog(ctx, method, extID, telegramID, "done", "трафик-тариф выдан")
	if grant {
		return link, r.ExpireAt, snap, nil
	}
	a.forgetTrafficIntent(ctx, telegramID, snap.Code, "")
	if method == model.PayMethodYooKassa {
		a.fiscalize(parseAmountRub(amount), fmt.Sprintf("Трафик-тариф «%s»", snapTitle(snap)))
	}
	return link, r.ExpireAt, snap, nil
}

func snapTitle(s *model.PlanSnapshot) string {
	if s == nil {
		return ""
	}
	if strings.TrimSpace(s.Name) != "" {
		return s.Name
	}
	return s.Code
}

const packTargetStage = "pack_target"

func (a *App) packTargetDone(ctx context.Context, extID string, limit int64) bool {
	if extID == "" || a.store == nil {
		return false
	}
	logs, err := a.store.PayLogs(ctx, extID, 0, 0)
	if err != nil {
		return false
	}
	want := "limit=" + strconv.FormatInt(limit, 10)
	for i := range logs {
		// Суффикс, а не подстрока: «limit=1» входит в «limit=10».
		if logs[i].ExtID == extID && logs[i].Stage == packTargetStage && strings.HasSuffix(strings.TrimSpace(logs[i].Detail), want) {
			return true
		}
	}
	return false
}

func (a *App) finalizePack(ctx context.Context, telegramID int64, method, amount, extID string, snap *model.PlanSnapshot, grant bool) (dealDone, error) {
	var d dealDone
	if a.holdRestoredPayment(ctx, method, purposePack, 0, amount, telegramID, extID) {
		return d, a.restoreHeldErr(telegramID)
	}
	unlock, dup := a.lockDeal(ctx, telegramID, method, extID)
	if dup {
		unlock()
		return d, storage.ErrDuplicateExtID
	}
	refund, err := a.applyPackLocked(ctx, telegramID, method, amount, extID, snap)
	unlock()
	switch {
	case refund && grant:
		return d, errPackNotOwner
	case refund:
		d.refunded = true
		return d, a.refundPackToBalance(ctx, telegramID, method, amount, extID, snap)
	case err != nil:
		return d, err
	}
	d.applied = snap
	if !grant {
		a.forgetTrafficIntent(ctx, telegramID, snap.Code, snap.PackID)
		if method == model.PayMethodYooKassa {
			a.fiscalize(parseAmountRub(amount), fmt.Sprintf("Пакет трафика %d ГБ", snap.PackGB))
		}
	}
	return d, nil
}

// trafficGrowth — лимит владельца трафик-тарифа после прибавки gb ГБ. code —
// его тариф; пустой — любой трафик-тариф.
func (a *App) trafficGrowth(ctx context.Context, tgID int64, code string, gb int) (*remnawave.Client, *remnawave.PanelUser, int64, error) {
	if owner := a.trafficOwner(ctx, tgID); owner == nil || (code != "" && owner.Code != code) {
		return nil, nil, 0, errPackNotOwner
	}
	panel := a.panelClient()
	if panel == nil {
		return nil, nil, 0, errors.New("панель не подключена")
	}
	pu, err := panel.FindByTelegramID(ctx, tgID)
	switch {
	case err != nil:
		return nil, nil, 0, err
	case pu == nil:
		return nil, nil, 0, errNoPanelUser
	case pu.TrafficLimit <= 0:
		return nil, nil, 0, errPackUnlimited
	}
	want := pu.TrafficLimit + int64(gb)<<30
	if want < pu.TrafficLimit {
		return nil, nil, 0, errors.New("переполнение лимита трафика")
	}
	return panel, pu, want, nil
}

func (a *App) applyPackLocked(ctx context.Context, telegramID int64, method, amount, extID string, snap *model.PlanSnapshot) (refund bool, err error) {
	if snap.PackGB <= 0 || snap.PackGB > model.PlanTrafficMaxGB {
		return false, errTrafficSnap
	}
	panel, pu, want, err := a.trafficGrowth(ctx, telegramID, snap.Code, snap.PackGB)
	switch {
	case errors.Is(err, errPackNotOwner):
		a.payLog(ctx, method, extID, telegramID, "pack_refused", "пакет %s оплачен, а тарифа %s у покупателя нет", snap.PackID, snap.Code)
		return true, err
	case errors.Is(err, errNoPanelUser), errors.Is(err, errPackUnlimited):
		a.payLog(ctx, method, extID, telegramID, "pack_refused", "%v — прибавлять не к чему", err)
		if errors.Is(err, errNoPanelUser) {
			err = errPackNotOwner
		}
		return true, err
	case err != nil:
		a.payLog(ctx, method, extID, telegramID, "panel_error", "%v", err)
		return false, err
	}
	if !a.packTargetDone(ctx, extID, pu.TrafficLimit) {
		a.payLog(ctx, method, extID, telegramID, packTargetStage, "+%d GB: limit=%d", snap.PackGB, want)
		if err := panel.SetTrafficLimit(ctx, pu.Ref, want); err != nil {
			if !a.bonusTrafficApplied(telegramID, want) {
				a.payLog(ctx, method, extID, telegramID, "panel_error", "%v", err)
				return false, err
			}
			a.payLog(ctx, method, extID, telegramID, "panel_ok", "ответ панели потерян, лимит применён: %v", err)
		}
	} else {
		want = pu.TrafficLimit
		a.payLog(ctx, method, extID, telegramID, "panel_ok", "прибавка уже применена прошлой доставкой")
	}
	a.invalidateSubCache(telegramID)
	a.payLog(ctx, method, extID, telegramID, "panel_ok", "пакет +%d GB, лимит %d", snap.PackGB, want)
	if err := a.recordDeal(ctx, &model.Payment{
		TelegramID: telegramID, Method: method, Amount: amount, Status: model.PaymentPaid, ExtID: extID,
		Comment: purposePack, Snapshot: snap,
	}, "пакет выдан"); err != nil {
		return false, err
	}
	a.payLog(ctx, method, extID, telegramID, "done", "пакет трафика выдан")
	return false, nil
}

func (a *App) refundPackToBalance(ctx context.Context, telegramID int64, method, amount, extID string, snap *model.PlanSnapshot) error {
	alang := a.lang(a.cfg.AdminID)
	kopecks, ok := rubToKopecks(snap.Price)
	if !ok || kopecks <= 0 || !rubCurrency(snap.Currency) || a.store == nil {
		a.payLog(ctx, method, extID, telegramID, "error", "пакет не выдан и на баланс не зачислен: %s", amount)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.pack_refund_manual",
			a.userLabelByID(ctx, telegramID), escapeName(amount), escapeName(methodLabel(method)), escapeName(extID)))
		return nil
	}
	err := a.store.AddPaymentAndBalance(ctx, &model.Payment{
		TelegramID: telegramID, Method: method, Amount: amount, Status: model.PaymentPaid, ExtID: extID,
		Comment: "pack_refund", Snapshot: snap,
	}, kopecks)
	if errors.Is(err, storage.ErrDuplicateExtID) {
		return nil
	}
	if err != nil {
		a.payLog(ctx, method, extID, telegramID, "error", "возврат стоимости пакета на баланс: %v", err)
		return err
	}
	a.payLog(ctx, method, extID, telegramID, "pack_refund", "пакет не выдан: %d коп. на баланс", kopecks)
	if method == model.PayMethodYooKassa {
		a.fiscalize(float64(kopecks)/100, "Пополнение баланса")
	}
	a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "admin.pack_refunded",
		a.userLabelByID(ctx, telegramID), escapeName(amount), escapeName(snapTitle(snap))))
	if hasChat(telegramID) {
		a.notify(ctx, telegramID, i18n.T(a.lang(telegramID), "pack.refunded", kopecksToRub(kopecks)))
	}
	return nil
}

func (a *App) showPackDone(ctx context.Context, chatID int64, gb int) {
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "pack.done", gb, a.trafficLeftText(ctx, chatID, lang, "pack.left")), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.mysubs"), "menu:mysubs"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}
