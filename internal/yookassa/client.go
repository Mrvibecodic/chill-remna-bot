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

// APIError — ответ ЮKassa с кодом, отличным от 200. Код важен вызывающей
// стороне: 202 значит «запрос с этим Idempotence-Key ещё обрабатывается»
// (надо повторить тем же ключом), 401/403/5xx — проблема магазина или самой
// ЮKassa, а не карты пользователя.
type APIError struct {
	Status      int
	Description string
}

func (e *APIError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("ЮKassa HTTP %d: %s", e.Status, e.Description)
	}
	return fmt.Sprintf("ЮKassa вернула HTTP %d", e.Status)
}

// Retriable сообщает, что запрос имеет смысл повторить тем же ключом
// идемпотентности (ЮKassa ещё обрабатывает предыдущий или у неё сбой).
func (e *APIError) Retriable() bool {
	return e.Status == http.StatusAccepted || e.Status >= 500
}

// ShopSide сообщает, что дело в настройках магазина, в запросе бота или в самой
// ЮKassa, а не в карте пользователя: писать пользователю «не хватило средств»
// тут нельзя. 400 (invalid_request) — тоже сюда: так ЮKassa отвечает на битые
// параметры запроса (валюта, формат суммы, payment_method_id), и разбираться с
// этим должен админ, а не пользователь.
func (e *APIError) ShopSide() bool {
	return e.Retriable() || e.Status == http.StatusBadRequest ||
		e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

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
		return nil, &APIError{Status: resp.StatusCode, Description: e.Description}
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
