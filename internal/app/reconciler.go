package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/storage"
)

// Параметры фонового добивания оплат.
const (
	reconcileInterval = 2 * time.Minute // как часто проверяем неподтверждённые инвойсы
	reconcileGrace    = 2 * time.Minute // «свежие» инвойсы не трогаем — вебхук ещё может прийти
	reconcileGiveUp   = 24 * time.Hour  // через сутки инвойс считаем протухшим и снимаем с учёта
	reconcileBatch    = 50              // максимум инвойсов за один проход
)

// RunReconciler — фоновый «добивающий» проход по неподтверждённым инвойсам
// (YooKassa / CryptoBot). Закрывает случай, когда вебхук провайдера не дошёл
// (бот лежал / был за недоступным reverse-proxy), а пользователь не нажал
// «Проверить»: периодически перепрашиваем статус у провайдера и, если оплачено,
// выдаём подписку — идемпотентно, через тот же finalizePurchase, что и вебхук.
// Работает до отмены ctx.
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
	// Протухло — снимаем с учёта (деньги, если и были, вернёт провайдер; наша
	// задача — не висеть в очереди бесконечно).
	if t, err := time.Parse(time.RFC3339, pi.CreatedAt); err == nil && time.Since(t) > reconcileGiveUp {
		_ = st.ResolvePending(ctx, pi.ID)
		return
	}
	// Уже выдано (вебхук или кнопка «Проверить» успели) — закрываем тихо.
	if done, _ := st.PaymentByExtID(ctx, pi.ExtID); done {
		_ = st.ResolvePending(ctx, pi.ID)
		return
	}
	switch pi.Method {
	case model.PayMethodYooKassa:
		a.reconcileYooKassa(ctx, st, pi)
	case model.PayMethodCryptoBot:
		a.reconcileCryptoBot(ctx, st, pi)
	default:
		_ = st.ResolvePending(ctx, pi.ID)
	}
}

func (a *App) reconcileYooKassa(ctx context.Context, st storage.Storage, pi *model.PendingInvoice) {
	client := a.ykClient()
	if client == nil {
		return // платёжка выключена/не настроена — попробуем в следующий проход
	}
	pay, err := client.GetPayment(ctx, pi.ExtID)
	if err != nil {
		return // транзиентная ошибка — повторим позже
	}
	switch {
	case pay.Status == "succeeded" && pay.Paid:
		a.reconcileFinalize(ctx, st, pi, pay.Amount.Value+" "+pay.Amount.Currency)
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
		_ = st.ResolvePending(ctx, pi.ID) // битый ext_id — не зацикливаемся
		return
	}
	inv, err := client.GetInvoice(ctx, invoiceID)
	if err != nil {
		return
	}
	switch inv.Status {
	case "paid":
		a.reconcileFinalize(ctx, st, pi, inv.Amount+" "+inv.Asset)
	case "expired":
		_ = st.ResolvePending(ctx, pi.ID)
	}
}

func (a *App) reconcileFinalize(ctx context.Context, st storage.Storage, pi *model.PendingInvoice, amount string) {
	link, err := a.finalizePurchase(ctx, pi.TelegramID, pi.Months, pi.Method, amount, pi.ExtID)
	if err != nil {
		// Уже зачтено параллельно — закрываем; иначе оставляем на следующий проход.
		if errors.Is(err, storage.ErrDuplicateExtID) {
			_ = st.ResolvePending(ctx, pi.ID)
			return
		}
		a.log.Warn("reconciler: finalize", "method", pi.Method, "ext_id", pi.ExtID, "err", err)
		return
	}
	_ = st.ResolvePending(ctx, pi.ID)
	lang := a.lang(pi.TelegramID)
	a.notifyKB(ctx, pi.TelegramID, i18n.T(lang, "reconcile.paid_ok", link), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.mysubs"), "menu:mysubs")},
	})
	a.log.Info("reconciler: finalized late payment", "method", pi.Method, "ext_id", pi.ExtID, "chat_id", pi.TelegramID)
}
