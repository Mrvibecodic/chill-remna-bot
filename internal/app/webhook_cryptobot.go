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
	hintTG, hintMo, _ := parseCryptoBotPayload(up.Payload.Payload)
	_ = hintMo
	a.payLog(ctx, model.PayMethodCryptoBot, extID, hintTG, "webhook", "invoice_paid status=%s amount=%s", up.Payload.Status,
		cbAmount(up.Payload.Asset, up.Payload.Amount, up.Payload.PaidAsset, up.Payload.PaidAmount, up.Payload.Fiat))
	if a.store != nil {
		if done, _ := a.store.PaymentByExtID(ctx, extID); done {
			a.payLog(ctx, model.PayMethodCryptoBot, extID, hintTG, "duplicate", "уже финализирован, вебхук пропущен")
			return true, nil
		}
		if p, _ := a.store.PendingByExtID(ctx, extID); p != nil && p.Purpose == "topup" {
			amount := cbAmount(up.Payload.Asset, up.Payload.Amount, up.Payload.PaidAsset, up.Payload.PaidAmount, up.Payload.Fiat)
			if err := a.finalizeTopUp(ctx, p.TelegramID, p.Kopecks, model.PayMethodCryptoBot, amount, extID); err != nil {
				return false, fmt.Errorf("topup cryptobot %d: %w", up.Payload.InvoiceID, err)
			}
			_ = a.store.ResolvePending(ctx, p.ID)
			return true, nil
		}
	}

	chatID, months, err := parseCryptoBotPayload(up.Payload.Payload)
	if err != nil {
		a.payLog(ctx, model.PayMethodCryptoBot, extID, hintTG, "error", "битый payload инвойса (%q) — получатель неизвестен: %v", up.Payload.Payload, err)
		a.log.Error("cryptobot webhook: bad payload", "raw", up.Payload.Payload, "err", err)
		return true, nil
	}

	amount := a.cryptoAmount(months, cbAmount(up.Payload.Asset, up.Payload.Amount, up.Payload.PaidAsset, up.Payload.PaidAmount, up.Payload.Fiat))
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
