package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"remnabot/internal/model"
	"remnabot/internal/storage"
)

type ykNotification struct {
	Type   string `json:"type"`
	Event  string `json:"event"`
	Object struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Paid   bool   `json:"paid"`
		Amount struct {
			Value    string `json:"value"`
			Currency string `json:"currency"`
		} `json:"amount"`
		Metadata map[string]string `json:"metadata"`
	} `json:"object"`
}

func (a *App) HandleYooKassaWebhook(ctx context.Context, body []byte) (bool, error) {
	var n ykNotification
	if err := json.Unmarshal(body, &n); err != nil {
		return false, fmt.Errorf("yookassa webhook: bad json: %w", err)
	}
	hintTG, _ := strconv.ParseInt(n.Object.Metadata["telegram_id"], 10, 64)
	if n.Object.ID != "" {
		a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "webhook", "event=%s status=%s", n.Event, n.Object.Status)
	}
	if n.Event == "payment.canceled" && n.Object.ID != "" {
		// Платёж отменён: выдача не нужна, но pending-запись надо снять, иначе
		// реконсилятор сутки опрашивает мёртвый счёт. Причину пишем в журнал.
		if a.store != nil {
			if p, _ := a.store.PendingByExtID(ctx, n.Object.ID); p != nil {
				_ = a.store.ResolvePending(ctx, p.ID)
			}
		}
		reason := ""
		if client := a.ykClient(); client != nil {
			if pay, err := client.GetPayment(ctx, n.Object.ID); err == nil {
				reason = pay.CancellationDetails.Reason
			}
		}
		a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "canceled", "платёж отменён (причина: %s)", reason)
		return true, nil
	}
	if n.Event != "payment.succeeded" {
		a.log.Info("yookassa webhook: skipping event", "event", n.Event, "id", n.Object.ID)
		return false, nil
	}
	if n.Object.ID == "" {
		a.log.Warn("yookassa webhook: empty payment id")
		return false, nil
	}
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, n.Object.ID); done {
			a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
	}
	client := a.ykClient()
	if client == nil {
		a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "error", "клиент ЮKassa не настроен — вебхук нельзя верифицировать")
		a.log.Error("yookassa webhook: client not configured, cannot verify", "id", n.Object.ID)
		return true, nil
	}
	pay, err := client.GetPayment(ctx, n.Object.ID)
	if err != nil {
		a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "verify_error", "%v", err)
		return false, fmt.Errorf("yookassa webhook: verify %s: %w", n.Object.ID, err)
	}
	a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "verified", "API: status=%s paid=%v amount=%s %s", pay.Status, pay.Paid, pay.Amount.Value, pay.Amount.Currency)
	if pay.Status != "succeeded" || !pay.Paid {
		a.log.Warn("yookassa webhook: payment not confirmed by API", "id", n.Object.ID, "status", pay.Status, "paid", pay.Paid)
		return true, nil
	}
	if a.store != nil {
		if p, _ := a.store.PendingByExtID(ctx, n.Object.ID); p != nil && p.Purpose == "topup" {
			amount := pay.Amount.Value + " " + pay.Amount.Currency
			if err := a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodYooKassa, amount, n.Object.ID); err != nil {
				return false, fmt.Errorf("topup yookassa %s: %w", n.Object.ID, err)
			}
			_ = a.store.ResolvePending(ctx, p.ID)
			return true, nil
		}
	}
	chatID, _ := strconv.ParseInt(pay.Metadata["telegram_id"], 10, 64)
	months, _ := strconv.Atoi(pay.Metadata["months"])
	if chatID == 0 || months == 0 {
		a.payLog(ctx, model.PayMethodYooKassa, n.Object.ID, hintTG, "error", "в metadata платежа нет telegram_id/months — получатель неизвестен")
		a.log.Error("yookassa webhook: missing metadata", "id", n.Object.ID)
		return true, nil
	}
	amount := pay.Amount.Value + " " + pay.Amount.Currency
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodYooKassa, amount, n.Object.ID)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.log.Info("yookassa webhook: race lost (other delivery won)", "id", n.Object.ID)
			return true, nil
		}
		return false, fmt.Errorf("finalize yookassa %s: %w", n.Object.ID, err)
	}
	a.saveAutoPayFromPayment(ctx, chatID, months, pay)
	a.sendSubActive(ctx, chatID, link, expireAt)
	a.log.Info("yookassa webhook: payment finalized", "id", n.Object.ID, "chat_id", chatID, "months", months)
	return true, nil
}
