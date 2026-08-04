package app

import (
	"context"
	"encoding/json"
	"fmt"

	"remnabot/internal/heleket"
	"remnabot/internal/model"
)

// hlRawEvent — минимальный разбор тела на случай, когда подпись не сошлась:
// uuid всё равно нужен, чтобы перепроверить счёт через API.
type hlRawEvent struct {
	UUID    string `json:"uuid"`
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
	Type    string `json:"type"`
}

// HandleHeleketWebhook принимает уведомление Heleket.
//
// Порядок намеренно такой же, как у Platega: подпись проверяется, но решение
// принимается ТОЛЬКО по ответу API. Подделанное уведомление ничего не выдаст,
// потому что статус берётся из /v1/payment/info, а не из тела запроса. Это же
// снимает риск расхождения байтовой сериализации подписи: даже если проверка
// подписи однажды начнёт ошибаться, приём денег не встанет — в журнал уйдёт
// предупреждение.
func (a *App) HandleHeleketWebhook(ctx context.Context, body []byte) (bool, error) {
	client := a.hlClient()
	if client == nil {
		a.log.Error("heleket webhook: client not configured")
		return true, nil
	}

	var uuid, orderID, status string
	ev, verr := client.VerifyWebhook(body)
	if verr == nil {
		uuid, orderID, status = ev.UUID, ev.OrderID, ev.Status
		if ev.Type == "payout" {
			a.log.Info("heleket webhook: payout event ignored", "uuid", uuid)
			return true, nil
		}
	} else {
		var raw hlRawEvent
		if err := json.Unmarshal(body, &raw); err != nil {
			return false, fmt.Errorf("heleket webhook: bad json: %w", err)
		}
		uuid, orderID, status = raw.UUID, raw.OrderID, raw.Status
		a.payLog(ctx, model.PayMethodHeleket, hlExtPrefix+uuid, 0, "sign_mismatch",
			"подпись вебхука не сошлась (%v) — статус перепроверяется через API", verr)
		a.log.Warn("heleket webhook: signature not verified", "uuid", uuid, "err", verr)
	}

	if uuid == "" {
		a.log.Warn("heleket webhook: empty uuid", "order_id", orderID)
		return false, nil
	}

	extID := hlExtPrefix + uuid
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.payLog(ctx, model.PayMethodHeleket, extID, 0, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
	}

	inv, err := client.Info(ctx, uuid)
	if err != nil {
		a.payLog(ctx, model.PayMethodHeleket, extID, 0, "verify_error", "%v", err)
		return false, fmt.Errorf("heleket webhook: verify %s: %w", uuid, err)
	}

	hintTG, _, _ := parseHLData(inv.AdditionalData)
	a.payLog(ctx, model.PayMethodHeleket, extID, hintTG, "webhook",
		"вебхук status=%s, по API status=%s amount=%s", status, inv.Status, a.hlAmountLabel(inv))

	if heleket.Successful(inv.Status) {
		a.finalizeHeleket(ctx, inv)
		a.log.Info("heleket webhook: finalized", "uuid", uuid)
		return true, nil
	}

	// Деньги пришли, но подписку выдавать нельзя — зовём админа.
	switch inv.Status {
	case heleket.StatusWrongAmount:
		a.hlNotifyAdmin(ctx, inv, "underpaid")
	case heleket.StatusLocked:
		a.hlNotifyAdmin(ctx, inv, "locked")
	}

	// Промежуточные статусы pending НЕ гасят: переход check → paid легален, и
	// снятие счёта здесь потеряло бы последующую оплату.
	if heleket.Final(inv.Status) && a.store != nil {
		if p, _ := a.store.PendingByExtID(ctx, extID); p != nil {
			_ = a.store.ResolvePending(ctx, p.ID)
		}
	}
	a.log.Info("heleket webhook: not paid", "uuid", uuid, "status", inv.Status)
	return true, nil
}
