package cryptobot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var BaseURL = "https://pay.crypt.bot/api"

type Client struct {
	token string
	http  *http.Client
}

func New(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

type Invoice struct {
	InvoiceID         int64  `json:"invoice_id"`
	Status            string `json:"status"`
	Hash              string `json:"hash"`
	Asset             string `json:"asset"`
	Amount            string `json:"amount"`
	Fiat              string `json:"fiat"`
	PaidAsset         string `json:"paid_asset"`
	PaidAmount        string `json:"paid_amount"`
	BotInvoiceURL     string `json:"bot_invoice_url"`
	MiniAppInvoiceURL string `json:"mini_app_invoice_url"`
	WebAppInvoiceURL  string `json:"web_app_invoice_url"`
	Payload           string `json:"payload"`
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
		// Тело содержит имя ошибки (UNAUTHORIZED, PARAM_* и т.п.) — без него
		// «HTTP 400» в журнале не даёт понять, что чинить.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if len(b) > 0 {
			return fmt.Errorf("CryptoBot HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return fmt.Errorf("CryptoBot HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("разбор CryptoBot: %w", err)
	}
	return nil
}

// CreateInvoice выставляет фиатный счёт. fiat — код валюты счёта (RUB, USD,
// EUR…); пустое значение означает RUB.
func (c *Client) CreateInvoice(ctx context.Context, amount, fiat, acceptedAssets string, telegramID int64, months int) (*Invoice, error) {
	if acceptedAssets == "" {
		acceptedAssets = "USDT"
	}
	if fiat == "" {
		fiat = "RUB"
	}

	body := map[string]any{
		"currency_type":   "fiat",
		"fiat":            fiat,
		"amount":          amount,
		"accepted_assets": acceptedAssets,
		"description":     fmt.Sprintf("VPN subscription %d mo", months),
		"payload":         fmt.Sprintf("%d:%d", telegramID, months),
		"expires_in":      60 * 30,
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
