package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

type cryptoBotUpdate struct {
	UpdateID    int64  `json:"update_id"`
	UpdateType  string `json:"update_type"`
	RequestDate string `json:"request_date"`
	Payload     struct {
		InvoiceID  int64  `json:"invoice_id"`
		Status     string `json:"status"`
		Asset      string `json:"asset"`
		Amount     string `json:"amount"`
		Fiat       string `json:"fiat"`
		PaidAsset  string `json:"paid_asset"`
		PaidAmount string `json:"paid_amount"`
		Payload    string `json:"payload"`
		Hash       string `json:"hash"`
	} `json:"payload"`
}

func verifyCryptoBotSignature(signatureHex, token string, body []byte) error {
	if token == "" {
		// No token configured means CryptoBot isn't set up; without a token we
		// can't verify the signature, so refuse rather than trust the body (the
		// webhook route is always registered for the other providers).
		return errors.New("cryptobot webhook: token not configured")
	}
	if signatureHex == "" {
		return errors.New("cryptobot webhook: signature header missing")
	}
	got, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("cryptobot webhook: bad signature hex: %w", err)
	}
	key := sha256.Sum256([]byte(token))
	m := hmac.New(sha256.New, key[:])
	m.Write(body)
	if !hmac.Equal(got, m.Sum(nil)) {
		return errors.New("cryptobot webhook: signature mismatch")
	}
	return nil
}

func (a *App) HandleCryptoBotWebhook(ctx context.Context, signature string, body []byte) (bool, error) {
	a.mu.Lock()
	token := ""
	if a.botCfg != nil {
		token = a.botCfg.CryptoBot.Token
	}
	a.mu.Unlock()

	if err := verifyCryptoBotSignature(signature, token, body); err != nil {
		a.payLogThrottled(ctx, "cb-webhook-sign", model.PayMethodCryptoBot, "", 0, "sign_error", "%v", err)
		// 401, а не 500: на 5xx CryptoBot ретраит доставку 3 дня и после этого
		// АВТОМАТИЧЕСКИ отключает вебхуки приложению — чужой мусор и запросы с
		// неверным ключом ретраить незачем.
		return false, fmt.Errorf("cryptobot webhook: %w: %w", web.ErrUnauthorized, err)
	}

	var up cryptoBotUpdate
	if err := json.Unmarshal(body, &up); err != nil {
		return false, fmt.Errorf("cryptobot webhook: bad json: %w", err)
	}
	if up.UpdateType != "invoice_paid" {
		a.log.Info("cryptobot webhook: skipping update_type", "type", up.UpdateType)
		return false, nil
	}
	// Защита от воспроизведения по рекомендации доки: request_date сильно из
	// прошлого не принимаем. Деньги при этом не теряются — оплату старше окна
	// добирает реконсилятор по pending-записи, а повторную выдачу и так
	// блокирует идемпотентность по ExtID.
	if t, err := time.Parse(time.RFC3339, up.RequestDate); err == nil && time.Since(t) > 24*time.Hour {
		a.payLog(ctx, model.PayMethodCryptoBot, "", 0, "error", "апдейт отброшен: request_date старше суток (%s)", up.RequestDate)
		return true, nil
	}
	if up.Payload.Status != "paid" {
		a.log.Info("cryptobot webhook: invoice not paid yet", "id", up.Payload.InvoiceID, "status", up.Payload.Status)
		return false, nil
	}

	extID := "cb:" + strconv.FormatInt(up.Payload.InvoiceID, 10)
	hintTG, hintMo, _ := parseCryptoBotPayload(up.Payload.Payload)
	_ = hintMo
	rawAmount := cbAmount(up.Payload.Asset, up.Payload.Amount, up.Payload.PaidAsset, up.Payload.PaidAmount, up.Payload.Fiat)
	a.payLog(ctx, model.PayMethodCryptoBot, extID, hintTG, "webhook", "invoice_paid status=%s amount=%s", up.Payload.Status, rawAmount)
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.payLog(ctx, model.PayMethodCryptoBot, extID, hintTG, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
	}
	res, err := a.settleCryptoBot(ctx, extID, up.Payload.Payload, rawAmount)
	switch res {
	case cbTopUpFailed:
		return false, fmt.Errorf("topup cryptobot %d: %w", up.Payload.InvoiceID, err)
	case cbFailed:
		return false, fmt.Errorf("finalize cryptobot %d: %w", up.Payload.InvoiceID, err)
	case cbBadPayload:
		a.payLog(ctx, model.PayMethodCryptoBot, extID, hintTG, "error", "битый payload инвойса (%q) — получатель неизвестен: %v", up.Payload.Payload, err)
		a.log.Error("cryptobot webhook: bad payload", "raw", up.Payload.Payload, "err", err)
	case cbAlready:
		a.log.Info("cryptobot webhook: race lost", "id", up.Payload.InvoiceID)
	case cbIssued:
		a.log.Info("cryptobot webhook: payment finalized", "id", up.Payload.InvoiceID)
	}
	return true, nil
}

// pendingSnap — снимок из уже прочитанной pending-записи (в вебхуке она
// доверенней payload и потому читается заранее).
func pendingSnap(p *model.PendingInvoice) *model.PlanSnapshot {
	if p == nil {
		return nil
	}
	return p.Snapshot
}

func parseCryptoBotPayload(raw string) (int64, int, error) {
	tgs, mos, ok := strings.Cut(raw, ":")
	if !ok {
		return 0, 0, fmt.Errorf("expected '<tg_id>:<months>', got %q", raw)
	}
	chatID, err := strconv.ParseInt(tgs, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad telegram_id: %w", err)
	}
	months, err := strconv.Atoi(mos)
	if err != nil || months < 0 {
		// months == 0 — легальное значение: так помечаются счета на пополнение
		// баланса («tg:0»), вызывающий разбирает этот случай сам.
		return 0, 0, fmt.Errorf("bad months: %s", mos)
	}
	return chatID, months, nil
}
