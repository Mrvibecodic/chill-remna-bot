package platega

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var BaseURL = "https://app.platega.io"

const (
	MethodSBP = 2
	// MethodCards — карточный эквайринг. В актуальном enum Platega это 11;
	// прежний код 10 (CardsRUB) объявлен устаревшим и в enum отсутствует.
	MethodCards = 11
	// MethodCardsLegacy — старое значение из конфигов, сохранённых до перехода
	// на 11. При чтении конфига трактуется как MethodCards.
	MethodCardsLegacy = 10
)

type Client struct {
	http     *http.Client
	merchant string
	secret   string
}

func New(merchant, secret string) *Client {
	return &Client{
		http:     &http.Client{Timeout: 30 * time.Second},
		merchant: merchant,
		secret:   secret,
	}
}

type paymentDetails struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type createRequest struct {
	PaymentMethod  int            `json:"paymentMethod"`
	PaymentDetails paymentDetails `json:"paymentDetails"`
	Description    string         `json:"description"`
	Return         string         `json:"return"`
	FailedURL      string         `json:"failedUrl"`
	Payload        string         `json:"payload,omitempty"`
}

type createResponse struct {
	TransactionID string `json:"transactionId"`
	Redirect      string `json:"redirect"`
	Status        string `json:"status"`
}

type statusResponse struct {
	ID             string         `json:"id"`
	Status         string         `json:"status"`
	PaymentDetails paymentDetails `json:"paymentDetails"`
	Payload        string         `json:"payload"`
}

type Transaction struct {
	ID       string
	Redirect string
	Status   string
	Amount   float64
	Currency string
	Payload  string
}

func (c *Client) headers(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-MerchantId", c.merchant)
	req.Header.Set("X-Secret", c.secret)
}

func (c *Client) CreateTransaction(ctx context.Context, method int, amount float64, currency, desc, returnURL, payload string) (*Transaction, error) {
	body, _ := json.Marshal(createRequest{
		PaymentMethod:  method,
		PaymentDetails: paymentDetails{Amount: amount, Currency: currency},
		Description:    desc,
		Return:         returnURL,
		FailedURL:      returnURL,
		Payload:        payload,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/transaction/process", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.headers(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("platega create: status %d: %s", resp.StatusCode, b)
	}
	var cr createResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	if cr.Redirect == "" || cr.TransactionID == "" {
		return nil, fmt.Errorf("platega create: empty redirect/transactionId")
	}
	return &Transaction{ID: cr.TransactionID, Redirect: cr.Redirect, Status: cr.Status}, nil
}

func (c *Client) GetTransaction(ctx context.Context, id string) (*Transaction, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/transaction/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	c.headers(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("platega status: %d: %s", resp.StatusCode, b)
	}
	var sr statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return &Transaction{
		ID:       id,
		Status:   sr.Status,
		Amount:   sr.PaymentDetails.Amount,
		Currency: sr.PaymentDetails.Currency,
		Payload:  sr.Payload,
	}, nil
}
