// Package yookassa — минимальный клиент API ЮKassa (api.yookassa.ru/v3).
// Подтверждение оплаты в боте делается опросом статуса (GetPayment), без
// входящих вебхуков, поэтому достаточно двух вызовов: создать и проверить.
package yookassa

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// BaseURL — адрес API ЮKassa (var, чтобы переопределять в тестах).
var BaseURL = "https://api.yookassa.ru/v3"

type Client struct {
	shopID string
	secret string
	http   *http.Client
}

func New(shopID, secret string) *Client {
	return &Client{shopID: shopID, secret: secret, http: &http.Client{Timeout: 20 * time.Second}}
}

// Payment — нужные нам поля ответа ЮKassa.
type Payment struct {
	ID     string `json:"id"`
	Status string `json:"status"` // pending | waiting_for_capture | succeeded | canceled
	Paid   bool   `json:"paid"`
	Amount struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Confirmation struct {
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation"`
	Metadata map[string]string `json:"metadata"`
}

func idempotenceKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (c *Client) do(ctx context.Context, method, path string, body any, idemKey string) (*Payment, error) {
	var rdr *bytes.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.shopID, c.secret)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotence-Key", idemKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("нет связи с ЮKassa: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Description != "" {
			return nil, fmt.Errorf("ЮKassa HTTP %d: %s", resp.StatusCode, e.Description)
		}
		return nil, fmt.Errorf("ЮKassa вернула HTTP %d", resp.StatusCode)
	}
	var p Payment
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("разбор ответа ЮKassa: %w", err)
	}
	return &p, nil
}

// CreatePayment создаёт платёж и возвращает его id и URL для оплаты.
func (c *Client) CreatePayment(ctx context.Context, value, currency, description, returnURL string, telegramID int64, months int) (*Payment, error) {
	if currency == "" {
		currency = "RUB"
	}
	body := map[string]any{
		"amount":       map[string]string{"value": value, "currency": currency},
		"capture":      true,
		"confirmation": map[string]string{"type": "redirect", "return_url": returnURL},
		"description":  description,
		"metadata": map[string]string{
			"telegram_id": strconv.FormatInt(telegramID, 10),
			"months":      strconv.Itoa(months),
		},
	}
	return c.do(ctx, http.MethodPost, "/payments", body, idempotenceKey())
}

// GetPayment возвращает текущее состояние платежа по id.
func (c *Client) GetPayment(ctx context.Context, id string) (*Payment, error) {
	return c.do(ctx, http.MethodGet, "/payments/"+id, nil, "")
}
