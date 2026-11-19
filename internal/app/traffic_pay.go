package app

import (
	"context"
	"errors"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

func trafficPurpose(snap *model.PlanSnapshot) string {
	if snap != nil && snap.PackID != "" {
		return purposePack
	}
	return purposeTraffic
}

func (a *App) addTrafficInvoice(ctx context.Context, method, extID string, chatID int64, kopecks int64, snap *model.PlanSnapshot) {
	st := a.storeRef()
	if st == nil || extID == "" {
		return
	}
	if err := st.AddTrafficInvoice(ctx, &model.PendingInvoice{
		Method: method, ExtID: extID, TelegramID: chatID, Purpose: trafficPurpose(snap), Kopecks: kopecks, Snapshot: snap,
	}); err != nil {
		a.log.Warn("счёт трафика не поставлен в очередь", "err", err, "method", method, "ext_id", extID)
	}
}

func (a *App) trafficInvoice(ctx context.Context, extID string) *model.PendingInvoice {
	st := a.storeRef()
	if st == nil || extID == "" {
		return nil
	}
	inv, _ := st.TrafficInvoiceByExtID(ctx, extID)
	if inv == nil || !inv.Snapshot.IsTraffic() {
		return nil
	}
	return inv
}

type trafficSettled int

const (
	trafficIssued  trafficSettled = iota // выдано и отправлено человеку
	trafficAlready                       // уже выдано другой доставкой
	trafficHeld                          // счёт до восстановления базы, решает администратор
	trafficOrphan                        // получателя удалили, администратор извещён
	trafficFailed                        // выдача не удалась, счёт оставлен сверке
)

func (a *App) settleTraffic(ctx context.Context, inv *model.PendingInvoice, amount string) (trafficSettled, error) {
	st := a.storeRef()
	d, err := a.closeTrafficDeal(ctx, inv.TelegramID, inv.Method, amount, inv.ExtID, inv.Snapshot, false)
	var held restoreHeld
	switch {
	case errors.As(err, &held):
		return trafficHeld, err
	case errors.Is(err, storage.ErrDuplicateExtID):
		if st != nil {
			_ = st.ResolveTraffic(ctx, inv.ID)
		}
		return trafficAlready, nil
	case errors.Is(err, errOrphanPayment):
		return trafficOrphan, err
	case err != nil:
		a.log.Warn("оплата трафика не проведена", "method", inv.Method, "ext_id", inv.ExtID, "err", err)
		return trafficFailed, err
	}
	if st != nil {
		_ = st.ResolveTraffic(ctx, inv.ID)
	}
	if !d.refunded {
		a.notifyTrafficDone(ctx, inv.TelegramID, inv.Snapshot, d)
	}
	return trafficIssued, nil
}

func (a *App) notifyTrafficDone(ctx context.Context, chatID int64, snap *model.PlanSnapshot, d dealDone) {
	if !hasChat(chatID) {
		return
	}
	if snap != nil && snap.PackID != "" {
		a.showPackDone(ctx, chatID, snap.PackGB)
		return
	}
	a.sendSubActive(ctx, chatID, d.link, d.expireAt)
}

func trafficInvoiceDesc(lang string, snap *model.PlanSnapshot) string {
	if snap != nil && snap.PackID != "" {
		return i18n.T(lang, "pay.desc_pack", snap.PackGB)
	}
	return i18n.T(lang, "pay.desc_traffic", snapTitle(snap))
}

type trafficQueue struct{ storage.Storage }

func (q trafficQueue) ResolvePending(ctx context.Context, id int64) error {
	return q.ResolveTraffic(ctx, id)
}

func (q trafficQueue) PendingByExtID(ctx context.Context, extID string) (*model.PendingInvoice, error) {
	return q.TrafficInvoiceByExtID(ctx, extID)
}
