package yookassa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCreateAndGetPayment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/payments") {
			if r.Header.Get("Idempotence-Key") == "" {
				t.Error("ожидался Idempotence-Key на создании платежа")
			}
			if u, _, _ := r.BasicAuth(); u != "shop1" {
				t.Errorf("basic auth shopID=%q", u)
			}
			_, _ = w.Write([]byte(`{"id":"pay_1","status":"pending","confirmation":{"confirmation_url":"https://yoo/pay/1"}}`))
			return
		}

		_, _ = w.Write([]byte(`{"id":"pay_1","status":"succeeded","paid":true,"amount":{"value":"150.00","currency":"RUB"},"metadata":{"months":"3","telegram_id":"555"}}`))
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	c := New("shop1", "secret1")
	ctx := context.Background()
	pay, err := c.CreatePayment(ctx, "150.00", "RUB", "desc", "https://t.me", 555, 3)
	if err != nil || pay.ID != "pay_1" || pay.Confirmation.ConfirmationURL != "https://yoo/pay/1" {
		t.Fatalf("CreatePayment: %+v err=%v", pay, err)
	}
	got, err := c.GetPayment(ctx, "pay_1")
	if err != nil || got.Status != "succeeded" || got.Metadata["months"] != "3" {
		t.Fatalf("GetPayment: %+v err=%v", got, err)
	}
}

// Платёж с автопродлением должен просить ЮKassa сохранить способ оплаты.
func TestCreatePaymentSaving(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"pay_s","status":"pending","confirmation":{"confirmation_url":"https://yoo/pay/s"}}`))
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	c := New("shop1", "secret1")
	if _, err := c.CreatePaymentSaving(context.Background(), "100.00", "RUB", "d", "https://t.me", 7, 1, true); err != nil {
		t.Fatalf("CreatePaymentSaving: %v", err)
	}
	if got["save_payment_method"] != true {
		t.Fatalf("ожидался save_payment_method=true, тело: %+v", got)
	}
	meta, _ := got["metadata"].(map[string]any)
	if meta["autopay"] != "1" || meta["telegram_id"] != "7" {
		t.Fatalf("metadata: %+v", meta)
	}

	got = nil
	if _, err := c.CreatePaymentSaving(context.Background(), "100.00", "RUB", "d", "https://t.me", 7, 1, false); err != nil {
		t.Fatalf("CreatePaymentSaving(false): %v", err)
	}
	if _, ok := got["save_payment_method"]; ok {
		t.Fatalf("без автопродления save_payment_method слать не надо: %+v", got)
	}
}

// Автосписание идёт по сохранённому payment_method_id и без confirmation.
func TestChargeSaved(t *testing.T) {
	var got map[string]any
	var idem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idem = r.Header.Get("Idempotence-Key")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"pay_a","status":"succeeded","paid":true,"amount":{"value":"100.00","currency":"RUB"},"payment_method":{"id":"pm_1","saved":true,"type":"bank_card","card":{"last4":"4242"}}}`))
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	c := New("shop1", "secret1")
	pay, err := c.ChargeSaved(context.Background(), "pm_1", "100.00", "RUB", "d", 7, 1, "ap-7-20260101-0")
	if err != nil || pay.Status != "succeeded" {
		t.Fatalf("ChargeSaved: %+v err=%v", pay, err)
	}
	if got["payment_method_id"] != "pm_1" {
		t.Fatalf("ожидался payment_method_id, тело: %+v", got)
	}
	if _, ok := got["confirmation"]; ok {
		t.Fatalf("у автосписания не должно быть confirmation: %+v", got)
	}
	if idem != "ap-7-20260101-0" {
		t.Fatalf("Idempotence-Key = %q", idem)
	}
	if pay.SavedMethodTitle() != "•••• 4242" {
		t.Fatalf("SavedMethodTitle = %q", pay.SavedMethodTitle())
	}
}

// 400/401/403/5xx/202 — проблема магазина или запроса, пользователю такие
// ошибки как «карта не прошла» показывать нельзя.
func TestAPIErrorClassification(t *testing.T) {
	cases := map[int]struct{ shop, retry bool }{
		202: {true, true},
		400: {true, false},
		401: {true, false},
		403: {true, false},
		429: {true, true}, // rate limit — временно, ретраим, карта ни при чём
		500: {true, true},
		502: {true, true},
		402: {false, false}, // payment required — как раз про деньги
		404: {false, false},
	}
	for code, want := range cases {
		e := &APIError{Status: code}
		if e.ShopSide() != want.shop || e.Retriable() != want.retry {
			t.Errorf("код %d: ShopSide=%v Retriable=%v, ожидалось %+v", code, e.ShopSide(), e.Retriable(), want)
		}
	}
	if (&APIError{Status: 400, Description: "invalid currency"}).Error() != "ЮKassa HTTP 400: invalid currency" {
		t.Error("текст ошибки с описанием")
	}
}
