package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/web"
)

type plNotification struct {
	ID            string `json:"id"`
	TransactionID string `json:"transactionId"`
}

// HandlePlategaWebhook обрабатывает колбэк Platega. Подлинность проверяется
// двумя рубежами: заголовки X-MerchantId/X-Secret (Platega шлёт свои же
// креденшелы мерчанта; чужим запросам — 401 без похода в API) и, главное,
// перепроверка статуса через GET /transaction/{id} — решение о выдаче
// принимается только по ответу API.
func (a *App) HandlePlategaWebhook(ctx context.Context, merchantID, secret string, body []byte) (bool, error) {
	cfg := a.plConfig()
	if cfg.MerchantID == "" || cfg.Secret == "" {
		a.payLogThrottled(ctx, "pl-webhook-off", model.PayMethodPlatega, "", 0, "error", "вебхук отброшен: Platega не настроена")
		return false, fmt.Errorf("platega webhook: %w", web.ErrUnauthorized)
	}
	if subtle.ConstantTimeCompare([]byte(merchantID), []byte(cfg.MerchantID)) != 1 ||
		subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.Secret)) != 1 {
		a.payLogThrottled(ctx, "pl-webhook-auth", model.PayMethodPlatega, "", 0, "sign_error", "заголовки X-MerchantId/X-Secret не сошлись — запрос не от Platega")
		a.log.Warn("platega webhook: bad credentials")
		return false, fmt.Errorf("platega webhook: %w", web.ErrUnauthorized)
	}
	var n plNotification
	if err := json.Unmarshal(body, &n); err != nil {
		return false, fmt.Errorf("platega webhook: bad json: %w", err)
	}
	id := n.ID
	if id == "" {
		id = n.TransactionID
	}
	if id == "" {
		a.log.Warn("platega webhook: empty id")
		return false, nil
	}
	client := a.plClient()
	if client == nil {
		a.payLog(ctx, model.PayMethodPlatega, id, 0, "error", "клиент Platega не настроен — вебхук нельзя верифицировать")
		a.log.Error("platega webhook: client not configured")
		return true, nil
	}
	tx, err := client.GetTransaction(ctx, id)
	if err != nil {
		a.payLog(ctx, model.PayMethodPlatega, id, 0, "verify_error", "%v", err)
		return false, fmt.Errorf("platega webhook: verify %s: %w", id, err)
	}
	hintTG, _ := parsePlPayload(tx.Payload)
	a.payLog(ctx, model.PayMethodPlatega, id, hintTG, "webhook", "verified via API: status=%s amount=%.2f %s", tx.Status, tx.Amount, tx.Currency)
	// Возврат средств приходит уже ПОСЛЕ выдачи подписки, когда платёж давно
	// финализирован — поэтому chargeback разбираем до дедупликации по ExtID.
	if strings.EqualFold(tx.Status, "CHARGEBACKED") {
		a.payLog(ctx, model.PayMethodPlatega, id, hintTG, "chargeback", "Platega вернула деньги по транзакции — доступ НЕ отозван, разберите вручную")
		alang := a.lang(a.cfg.AdminID)
		a.notify(ctx, a.cfg.AdminID, i18n.T(alang, "pl.admin_chargeback", id, fmt.Sprintf("%.2f %s", tx.Amount, tx.Currency), a.userLabelByID(ctx, hintTG)))
		return true, nil
	}
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, id); done {
			return true, nil
		}
	}
	if !strings.EqualFold(tx.Status, "CONFIRMED") {
		a.log.Info("platega webhook: not confirmed", "id", id, "status", tx.Status)
		return true, nil
	}
	a.finalizePlatega(ctx, id, tx)
	a.log.Info("platega webhook: finalized", "id", id)
	return true, nil
}
