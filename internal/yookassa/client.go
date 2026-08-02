package yookassa

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var BaseURL = "https://api.yookassa.ru/v3"

type Client struct {
	shopID string
	secret string
	http   *http.Client
}

func New(shopID, secret string) *Client {
	return &Client{shopID: shopID, secret: secret, http: &http.Client{Timeout: 20 * time.Second}}
}

type Payment struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Paid   bool   `json:"paid"`
	Amount struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`
	Confirmation struct {
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation"`
	Metadata map[string]string `json:"metadata"`
	// PaymentMethod приходит в ответе, когда способ оплаты сохранён для
	// автоплатежей: Saved=true и ID, которым потом можно списывать без
	// участия пользователя.
	PaymentMethod struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Saved bool   `json:"saved"`
		Title string `json:"title"`
		Card  struct {
			Last4 string `json:"last4"`
		} `json:"card"`
	} `json:"payment_method"`
}

// SavedMethodTitle возвращает человекочитаемое название сохранённого способа
// оплаты («•••• 1234»), если ЮKassa его прислала.
func (p *Payment) SavedMethodTitle() string {
	if p == nil {
		return ""
	}
	if t := p.PaymentMethod.Title; t != "" {
		return t
	}
	if l4 := p.PaymentMethod.Card.Last4; l4 != "" {
		return "•••• " + l4
	}
	return p.PaymentMethod.Type
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

func (c *Client) CreatePayment(ctx context.Context, value, currency, description, returnURL string, telegramID int64, months int) (*Payment, error) {
	return c.CreatePaymentSaving(ctx, value, currency, description, returnURL, telegramID, months, false)
}

// CreatePaymentSaving создаёт платёж и, если save=true, просит ЮKassa
// сохранить способ оплаты для последующих автосписаний (save_payment_method).
// Пользователь при этом видит в форме оплаты согласие на автоплатежи; сам
// признак дублируется в metadata, чтобы вебхук знал, что метод надо запомнить.
func (c *Client) CreatePaymentSaving(ctx context.Context, value, currency, description, returnURL string, telegramID int64, months int, save bool) (*Payment, error) {
	if currency == "" {
		currency = "RUB"
	}
	meta := map[string]string{
		"telegram_id": strconv.FormatInt(telegramID, 10),
		"months":      strconv.Itoa(months),
	}
	if save {
		meta["autopay"] = "1"
	}
	body := map[string]any{
		"amount":       map[string]string{"value": value, "currency": currency},
		"capture":      true,
		"confirmation": map[string]string{"type": "redirect", "return_url": returnURL},
		"description":  description,
		"metadata":     meta,
	}
	if save {
		body["save_payment_method"] = true
	}
	return c.do(ctx, http.MethodPost, "/payments", body, idempotenceKey())
}

// ChargeSaved списывает деньги сохранённым способом оплаты — без участия
// пользователя (автоплатёж). methodID — это payment_method.id из первого
// платежа, сделанного с save_payment_method. idemKey должен быть стабильным
// для одной попытки списания, чтобы повтор запроса не списал деньги дважды.
func (c *Client) ChargeSaved(ctx context.Context, methodID, value, currency, description string, telegramID int64, months int, idemKey string) (*Payment, error) {
	if currency == "" {
		currency = "RUB"
	}
	if idemKey == "" {
		idemKey = idempotenceKey()
	}
	body := map[string]any{
		"amount":            map[string]string{"value": value, "currency": currency},
		"capture":           true,
		"payment_method_id": methodID,
		"description":       description,
		"metadata": map[string]string{
			"telegram_id": strconv.FormatInt(telegramID, 10),
			"months":      strconv.Itoa(months),
			"autocharge":  "1",
		},
	}
	return c.do(ctx, http.MethodPost, "/payments", body, idemKey)
}

func (c *Client) GetPayment(ctx context.Context, id string) (*Payment, error) {
	return c.do(ctx, http.MethodGet, "/payments/"+url.PathEscape(id), nil, "")
}
