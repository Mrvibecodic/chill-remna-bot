package heleket

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/heleket/go-sdk"
)

const (
	testMerchant = "8b03432e-385b-4670-8d06-064591096795"
	testKey      = "test-payment-key"
)

func withServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	c, err := New(testMerchant, testKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestCreateInvoice(t *testing.T) {
	var gotPath, gotMerchant, gotSign string
	var gotBody []byte
	c := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMerchant = r.Header.Get("merchant")
		gotSign = r.Header.Get("sign")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"state":0,"result":{"uuid":"u-1","order_id":"hl-7-1-abcd","amount":"199.00","currency":"RUB","payment_status":"check","url":"https://pay.example/u-1","is_final":false,"additional_data":"tg=7&mo=1"}}`))
	})

	inv, err := c.CreateInvoice(context.Background(), InvoiceRequest{
		Amount: "199.00", Currency: "RUB", OrderID: "hl-7-1-abcd",
		Subtract: 100, Lifetime: 3600, AdditionalData: "tg=7&mo=1",
		CallbackURL: "https://bot.example/webhook/heleket",
	})
	if err != nil {
		t.Fatalf("CreateInvoice: %v", err)
	}
	if gotPath != "/v1/payment" {
		t.Fatalf("путь: %q", gotPath)
	}
	if gotMerchant != testMerchant {
		t.Fatalf("заголовок merchant: %q", gotMerchant)
	}
	// Подпись должна считаться ровно по тем байтам, что ушли на провод.
	if want := sdk.Sign(gotBody, testKey); gotSign != want {
		t.Fatalf("подпись %q, ожидалась %q", gotSign, want)
	}
	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("тело не JSON: %v", err)
	}
	if sent["additional_data"] != "tg=7&mo=1" {
		t.Fatalf("additional_data не ушёл: %v", sent["additional_data"])
	}
	if sent["url_callback"] != "https://bot.example/webhook/heleket" {
		t.Fatalf("url_callback не ушёл: %v", sent["url_callback"])
	}
	if inv.UUID != "u-1" || inv.URL != "https://pay.example/u-1" {
		t.Fatalf("разбор ответа: %+v", inv)
	}
	if inv.Status != "check" {
		t.Fatalf("статус берётся из payment_status: %q", inv.Status)
	}
	if inv.AdditionalData != "tg=7&mo=1" {
		t.Fatalf("additional_data не разобран: %q", inv.AdditionalData)
	}
}

func TestInfoAndStatusPredicates(t *testing.T) {
	c := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payment/info" {
			t.Errorf("путь: %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"state":0,"result":{"uuid":"u-2","status":"paid_over","amount":"199.00","currency":"RUB","payer_amount":"2.35","payer_currency":"USDT","is_final":true}}`))
	})
	inv, err := c.Info(context.Background(), "u-2")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !Successful(inv.Status) || !Final(inv.Status) {
		t.Fatalf("paid_over должен быть успешным и финальным: %q", inv.Status)
	}
	if inv.PayerAmount != "2.35" || inv.PayerCurrency != "USDT" {
		t.Fatalf("сумма клиента: %+v", inv)
	}
}

func TestStatusClassification(t *testing.T) {
	cases := []struct {
		status         string
		success, final bool
	}{
		{"paid", true, true},
		{"paid_over", true, true},
		{"wrong_amount", false, true},
		{"locked", false, true},
		{"cancel", false, true},
		{"fail", false, true},
		{"check", false, false},
		{"confirm_check", false, false},
		{"process", false, false},
		{"wrong_amount_waiting", false, false},
	}
	for _, c := range cases {
		if Successful(c.status) != c.success || Final(c.status) != c.final {
			t.Fatalf("статус %q: success=%v final=%v, ожидалось %v/%v",
				c.status, Successful(c.status), Final(c.status), c.success, c.final)
		}
	}
}

func TestValidationErrorIsHumanized(t *testing.T) {
	c := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"state":1,"errors":{"amount":["validation.min"],"currency":["validation.in"]}}`))
	})
	_, err := c.CreateInvoice(context.Background(), InvoiceRequest{Amount: "0.01", Currency: "XXX", OrderID: "hl-1-1-a"})
	if err == nil {
		t.Fatal("ожидалась ошибка валидации")
	}
	msg := err.Error()
	if !strings.Contains(msg, "amount") || !strings.Contains(msg, "currency") {
		t.Fatalf("в тексте нет полей: %q", msg)
	}
	if strings.Contains(msg, "{") {
		t.Fatalf("в текст протёк сырой JSON: %q", msg)
	}
}

func TestAPIErrorIsHumanized(t *testing.T) {
	c := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":1,"message":"merchant not found"}`))
	})
	_, err := c.Info(context.Background(), "u-3")
	if err == nil || !strings.Contains(err.Error(), "merchant not found") {
		t.Fatalf("ошибка API не проброшена: %v", err)
	}
}

func TestServices(t *testing.T) {
	c := withServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":0,"result":[{"network":"tron","currency":"USDT","is_available":true,"limit":{"min_amount":"1","max_amount":"10000"}},{"network":"eth","currency":"ETH","is_available":false}]}`))
	})
	list, err := c.Services(context.Background())
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(list) != 2 || list[0].Currency != "USDT" || list[0].MinAmount != "1" || list[1].IsAvailable {
		t.Fatalf("разбор списка услуг: %+v", list)
	}
}

// signedBody собирает тело вебхука так же, как это делает Heleket: поле sign
// идёт последним, подпись считается по телу БЕЗ него.
func signedBody(payload string) []byte {
	sign := sdk.Sign([]byte(payload), testKey)
	return []byte(payload[:len(payload)-1] + `,"sign":"` + sign + `"}`)
}

func TestVerifyWebhook(t *testing.T) {
	c, err := New(testMerchant, testKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := signedBody(`{"type":"payment","uuid":"u-9","order_id":"hl-7-1-abcd","status":"paid","amount":"199.00"}`)
	ev, err := c.VerifyWebhook(body)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.UUID != "u-9" || ev.Status != "paid" || ev.OrderID != "hl-7-1-abcd" || ev.Type != "payment" {
		t.Fatalf("разбор вебхука: %+v", ev)
	}
}

func TestVerifyWebhookRejectsBadSign(t *testing.T) {
	c, err := New(testMerchant, testKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.VerifyWebhook([]byte(`{"uuid":"u-9","status":"paid","sign":"00000000000000000000000000000000"}`)); err == nil {
		t.Fatal("подделанная подпись принята")
	}
	// Чужой ключ тоже не должен проходить: в Heleket ключи платежей и выплат разные.
	other, _ := New(testMerchant, "payout-key")
	body := signedBody(`{"uuid":"u-9","status":"paid"}`)
	if _, err := other.VerifyWebhook(body); err == nil {
		t.Fatal("подпись принята с чужим ключом")
	}
}

// Слеши в URL — та самая PHP-специфика (json_encode экранирует «/» как «\/»),
// на которой ломаются самописные проверки подписи.
func TestVerifyWebhookWithEscapedSlashes(t *testing.T) {
	c, _ := New(testMerchant, testKey)
	body := signedBody(`{"uuid":"u-9","status":"paid","url":"https:\/\/pay.example\/u-9"}`)
	if _, err := c.VerifyWebhook(body); err != nil {
		t.Fatalf("подпись с экранированными слешами не сошлась: %v", err)
	}
}
