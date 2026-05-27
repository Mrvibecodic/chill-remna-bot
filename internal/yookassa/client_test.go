package yookassa

import (
	"context"
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
		// GET /payments/pay_1
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
