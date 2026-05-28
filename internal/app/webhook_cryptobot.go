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

	"remnabot/internal/model"
	"remnabot/internal/storage"
)

// cryptoBotUpdate — формат уведомления CryptoBot.
// https://help.crypt.bot/crypto-pay-api#webhooks
//
// Подпись: HMAC-SHA256(body, SHA256(token)) в hex, в заголовке
// Crypto-Pay-API-Signature. Ключом для HMAC берётся не сам токен, а его
// SHA256-дайджест — таково правило API.
type cryptoBotUpdate struct {
	UpdateID    int64  `json:"update_id"`
	UpdateType  string `json:"update_type"` // invoice_paid
	RequestDate string `json:"request_date"`
	Payload     struct {
		InvoiceID int64  `json:"invoice_id"`
		Status    string `json:"status"`
		Asset     string `json:"asset"`
		Amount    string `json:"amount"`
		Payload   string `json:"payload"` // наш "telegram_id:months"
		Hash      string `json:"hash"`
	} `json:"payload"`
}

// verifyCryptoBotSignature — HMAC-SHA256(body, SHA256(token)).
// При пустом токене проверка пропускается (только для локальной отладки).
func verifyCryptoBotSignature(signatureHex, token string, body []byte) error {
	if token == "" {
		return nil
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

// HandleCryptoBotWebhook — обработчик POST /webhook/cryptobot (Phase 4).
//
// Финализация подписки строго идемпотентна: ранний PaymentByExtID + UNIQUE
// INDEX в БД. ExtID для CryptoBot — строковое представление invoice_id.
func (a *App) HandleCryptoBotWebhook(ctx context.Context, signature string, body []byte) (bool, error) {
	a.mu.Lock()
	token := ""
	if a.botCfg != nil {
		token = a.botCfg.CryptoBot.Token
	}
	a.mu.Unlock()

	if err := verifyCryptoBotSignature(signature, token, body); err != nil {
		return false, err
	}

	var up cryptoBotUpdate
	if err := json.Unmarshal(body, &up); err != nil {
		return false, fmt.Errorf("cryptobot webhook: bad json: %w", err)
	}
	if up.UpdateType != "invoice_paid" {
		a.log.Info("cryptobot webhook: skipping update_type", "type", up.UpdateType)
		return false, nil
	}
	if up.Payload.Status != "paid" {
		a.log.Info("cryptobot webhook: invoice not paid yet", "id", up.Payload.InvoiceID, "status", up.Payload.Status)
		return false, nil
	}

	extID := "cb:" + strconv.FormatInt(up.Payload.InvoiceID, 10)
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.log.Info("cryptobot webhook: duplicate", "id", up.Payload.InvoiceID)
			return true, nil
		}
	}

	chatID, months, err := parseCryptoBotPayload(up.Payload.Payload)
	if err != nil {
		a.log.Error("cryptobot webhook: bad payload", "raw", up.Payload.Payload, "err", err)
		return true, nil // 200 OK — ретраить нет смысла
	}

	amount := a.cryptoAmount(months, up.Payload.Amount+" "+up.Payload.Asset)
	link, expireAt, err := a.finalizePurchase(ctx, chatID, months, model.PayMethodCryptoBot, amount, extID)
	if err != nil {
		if errors.Is(err, storage.ErrDuplicateExtID) {
			a.log.Info("cryptobot webhook: race lost", "id", up.Payload.InvoiceID)
			return true, nil
		}
		return false, fmt.Errorf("finalize cryptobot %d: %w", up.Payload.InvoiceID, err)
	}

	a.sendSubActive(ctx, chatID, link, expireAt)
	a.log.Info("cryptobot webhook: payment finalized", "id", up.Payload.InvoiceID, "chat_id", chatID, "months", months)
	return true, nil
}

// parseCryptoBotPayload разбирает наш "<telegram_id>:<months>".
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
	if err != nil || months <= 0 {
		return 0, 0, fmt.Errorf("bad months: %s", mos)
	}
	return chatID, months, nil
}
