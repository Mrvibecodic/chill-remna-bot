// Package cryptobot — минимальный клиент Crypto Pay API (CryptoBot / Crypto Pay).
// Документация: https://help.crypt.bot/crypto-pay-api
//
// Нам нужны два вызова:
//   - POST /createInvoice  — создать инвойс, получить mini-app URL.
//   - POST /getInvoices    — получить статус инвойса (fallback на случай
//     недоставки webhook'а).
//
// Аутентификация: заголовок Crypto-Pay-API-Token со значением API-токена,
// выпущенным в @CryptoBot → My apps → New app.
package cryptobot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// BaseURL переопределяемый в тестах. Production — pay.crypt.bot,
// testnet — testnet-pay.crypt.bot (выпускается тогда тестовый токен).
var BaseURL = "https://pay.crypt.bot/api"

type Client struct {
	token string
	http  *http.Client
}

func New(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

// Invoice — поля, которые мы читаем из CreateInvoice ответа.
type Invoice struct {
	InvoiceID         int64  `json:"invoice_id"`
	Status            string `json:"status"` // active | paid | expired
	Hash              string `json:"hash"`
	Asset             string `json:"asset"` // USDT | TON | BTC | ...
	Amount            string `json:"amount"`
	BotInvoiceURL     string `json:"bot_invoice_url"`      // t.me/CryptoBot?start=...
	MiniAppInvoiceURL string `json:"mini_app_invoice_url"` // прямой mini-app
	WebAppInvoiceURL  string `json:"web_app_invoice_url"`  // запасной web-app
	Payload           string `json:"payload"`              // что мы положили (tg_id+months)
}

type response[T any] struct {
	OK     bool `json:"ok"`
	Error  any  `json:"error,omitempty"`
	Result T    `json:"result"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Crypto-Pay-API-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("нет связи с CryptoBot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CryptoBot HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("разбор CryptoBot: %w", err)
	}
	return nil
}

// CreateInvoice — выставить инвойс на сумму amount в asset (USDT/TON/BTC/...).
// payload — наш payload, в который кладём telegram_id|months для восстановления
// контекста при доставке вебхука / при ручной проверке статуса.
func (c *Client) CreateInvoice(ctx context.Context, amountRUB, acceptedAssets string, telegramID int64, months int) (*Invoice, error) {
	if acceptedAssets == "" {
		acceptedAssets = "USDT"
	}
	// Фиатный инвойс (как RW Shop): цена фиксирована в рублях, пользователь
	// платит криптой (accepted_assets) по живому курсу CryptoPay.
	body := map[string]any{
		"currency_type":   "fiat",
		"fiat":            "RUB",
		"amount":          amountRUB,
		"accepted_assets": acceptedAssets,
		"description":     fmt.Sprintf("VPN subscription %d mo", months),
		"payload":         fmt.Sprintf("%d:%d", telegramID, months),
		"expires_in":      60 * 30, // 30 минут
	}
	var r response[Invoice]
	if err := c.do(ctx, http.MethodPost, "/createInvoice", body, &r); err != nil {
		return nil, err
	}
	if !r.OK {
		return nil, fmt.Errorf("CryptoBot createInvoice failed: %v", r.Error)
	}
	return &r.Result, nil
}

// GetInvoice — получить статус инвойса по id (fallback, если webhook не пришёл).
func (c *Client) GetInvoice(ctx context.Context, invoiceID int64) (*Invoice, error) {
	body := map[string]any{
		"invoice_ids": strconv.FormatInt(invoiceID, 10),
	}
	type result struct {
		Items []Invoice `json:"items"`
	}
	var r response[result]
	if err := c.do(ctx, http.MethodPost, "/getInvoices", body, &r); err != nil {
		return nil, err
	}
	if !r.OK || len(r.Result.Items) == 0 {
		return nil, fmt.Errorf("CryptoBot invoice not found: %d", invoiceID)
	}
	return &r.Result.Items[0], nil
}
