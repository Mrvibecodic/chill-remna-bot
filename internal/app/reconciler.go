package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/storage"
)

const (
	reconcileInterval = 2 * time.Minute
	reconcileGrace    = 2 * time.Minute
	reconcileGiveUp   = 24 * time.Hour
	reconcileBatch    = 50
)

func (a *App) RunReconciler(ctx context.Context) {
	t := time.NewTicker(reconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.reconcileOnce(ctx)
		}
	}
}

func (a *App) reconcileOnce(ctx context.Context) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	if time.Since(a.payLogPurgedAt) > 24*time.Hour {
		a.payLogPurgedAt = time.Now()
		_ = st.PurgePayLogs(ctx, time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339))
	}
	cutoff := time.Now().UTC().Add(-reconcileGrace).Format(time.RFC3339)
	list, err := st.ListUnresolvedPending(ctx, cutoff, reconcileBatch)
	if err != nil {
		a.log.Warn("reconciler: list pending", "err", err)
		return
	}
	for i := range list {
		a.reconcileInvoice(ctx, st, &list[i])
	}
}

func (a *App) reconcileInvoice(ctx context.Context, st storage.Storage, pi *model.PendingInvoice) {

	if t, err := time.Parse(time.RFC3339, pi.CreatedAt); err == nil && time.Since(t) > reconcileGiveUp {
		a.payLog(ctx, pi.Method, pi.ExtID, pi.TelegramID, "reconcile_giveup", "счёт старше 24ч, снят с проверки")
		_ = st.ResolvePending(ctx, pi.ID)
		return
	}

	if done, _ := st.PaymentByExtID(ctx, pi.ExtID); done {
		_ = st.ResolvePending(ctx, pi.ID)
		return
	}
	switch pi.Method {
	case model.PayMethodYooKassa:
		a.reconcileYooKassa(ctx, st, pi)
	case model.PayMethodCryptoBot:
		a.reconcileCryptoBot(ctx, st, pi)
	case model.PayMethodPlatega:
		a.reconcilePlatega(ctx, st, pi)
	default:
		_ = st.ResolvePending(ctx, pi.ID)
	}
}

func (a *App) reconcileYooKassa(ctx context.Context, st storage.Storage, pi *model.PendingInvoice) {
	client := a.ykClient()
	if client == nil {
		return
	}
	pay, err := client.GetPayment(ctx, pi.ExtID)
	if err != nil {
		return
	}
	a.payLog(ctx, pi.Method, pi.ExtID, pi.TelegramID, "reconcile", "status=%s paid=%v", pay.Status, pay.Paid)
	switch {
	case pay.Status == "succeeded" && pay.Paid:
		// Платёж мог быть сделан с сохранением карты: если вебхук не дошёл и
		// оплату добил реконсилятор, предложение про автопродление всё равно
		// должно уйти — но ровно один раз, только когда подписка реально выдана.
		if a.reconcileFinalize(ctx, st, pi, pay.Amount.Value+" "+pay.Amount.Currency) {
			a.saveAutoPayFromPayment(ctx, pi.TelegramID, pi.Months, pay)
		}
	case pay.Status == "canceled":
		_ = st.ResolvePending(ctx, pi.ID)
	}
}

func (a *App) reconcileCryptoBot(ctx context.Context, st storage.Storage, pi *model.PendingInvoice) {
	client := a.cbClient()
	if client == nil {
		return
	}
	idStr := strings.TrimPrefix(pi.ExtID, "cb:")
	invoiceID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = st.ResolvePending(ctx, pi.ID)
		return
	}
	inv, err := client.GetInvoice(ctx, invoiceID)
	if err != nil {
		return
	}
	a.payLog(ctx, pi.Method, pi.ExtID, pi.TelegramID, "reconcile", "status=%s", inv.Status)
	switch inv.Status {
	case "paid":
		a.reconcileFinalize(ctx, st, pi, a.cryptoAmount(pi.Months, inv.Amount+" "+inv.Asset))
	case "expired":
		_ = st.ResolvePending(ctx, pi.ID)
	}
}

func (a *App) reconcilePlatega(ctx context.Context, st storage.Storage, pi *model.PendingInvoice) {
	client := a.plClient()
	if client == nil {
		return
	}
	tx, err := client.GetTransaction(ctx, pi.ExtID)
	if err != nil {
		return
	}
	a.payLog(ctx, pi.Method, pi.ExtID, pi.TelegramID, "reconcile", "status=%s", tx.Status)
	switch {
	case strings.EqualFold(tx.Status, "CONFIRMED"):
		a.reconcileFinalize(ctx, st, pi, fmt.Sprintf("%.2f %s", tx.Amount, tx.Currency))
	case strings.EqualFold(tx.Status, "CANCELED") || strings.EqualFold(tx.Status, "CHARGEBACKED"):
		_ = st.ResolvePending(ctx, pi.ID)
	}
}

// reconcileFinalize добивает недоставленную оплату. Возвращает true, только
// если подписка была выдана именно этим вызовом (дубли и ошибки — false), чтобы
// вызывающий не повторял разовые действия вроде предложения автопродления.
func (a *App) reconcileFinalize(ctx context.Context, st storage.Storage, pi *model.PendingInvoice, amount string) bool {
	if pi.Purpose == "topup" {
		if err := a.finalizeTopUp(ctx, pi.TelegramID, pi.Kopecks, pi.Method, amount, pi.ExtID); err != nil &&
			!errors.Is(err, storage.ErrDuplicateExtID) {
			a.log.Warn("reconciler: topup", "ext_id", pi.ExtID, "err", err)
			return false
		}
		_ = st.ResolvePending(ctx, pi.ID)
		return false
	}
	link, expireAt, err := a.finalizePurchase(ctx, pi.TelegramID, pi.Months, pi.Method, amount, pi.ExtID)
	if err != nil {

		if errors.Is(err, storage.ErrDuplicateExtID) {
			_ = st.ResolvePending(ctx, pi.ID)
			return false
		}
		a.log.Warn("reconciler: finalize", "method", pi.Method, "ext_id", pi.ExtID, "err", err)
		return false
	}
	_ = st.ResolvePending(ctx, pi.ID)
	a.sendSubActive(ctx, pi.TelegramID, link, expireAt)
	a.log.Info("reconciler: finalized late payment", "method", pi.Method, "ext_id", pi.ExtID, "chat_id", pi.TelegramID)
	return true
}
