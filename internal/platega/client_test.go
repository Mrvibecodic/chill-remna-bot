package platega

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-MerchantId") != "m1" || r.Header.Get("X-Secret") != "s1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transaction/process":
			_ = json.NewEncoder(w).Encode(map[string]any{"transactionId": "tx1", "redirect": "https://pay/x", "status": "PENDING"})
		case r.Method == http.MethodGet && r.URL.Path == "/transaction/tx1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "tx1", "status": "CONFIRMED",
				"paymentDetails": map[string]any{"amount": 150.0, "currency": "RUB"}, "payload": "telegram_id=300&months=1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	prev := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = prev }()

	c := New("m1", "s1")
	tx, err := c.CreateTransaction(context.Background(), MethodSBP, 150, "RUB", "VPN", "https://t.me", "telegram_id=300&months=1", Buyer{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if tx.Redirect != "https://pay/x" || tx.ID != "tx1" {
		t.Fatalf("bad create: %+v", tx)
	}
	st, err := c.GetTransaction(context.Background(), "tx1")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Status != "CONFIRMED" || st.Payload != "telegram_id=300&months=1" {
		t.Fatalf("bad status: %+v", st)
	}
}

// Покупатель уходит в metadata: почта и ID, а без покупателя блока нет вовсе.
func TestCreateTransactionMetadata(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"transactionId": "tx1", "redirect": "https://pay/x", "status": "PENDING"})
	}))
	defer srv.Close()
	prev := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = prev }()

	c := New("m1", "s1")
	ctx := context.Background()
	if _, err := c.CreateTransaction(ctx, MethodSBP, 150, "RUB", "VPN", "https://t.me", "p", Buyer{Email: "1000000001@telegram.org", UserID: "1000000001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTransaction(ctx, MethodSBP, 150, "RUB", "VPN", "https://t.me", "p", Buyer{}); err != nil {
		t.Fatal(err)
	}
	md, _ := got[0]["metadata"].(map[string]any)
	if md["user_email"] != "1000000001@telegram.org" || md["userId"] != "1000000001" {
		t.Fatalf("metadata: %v", got[0])
	}
	if _, ok := got[1]["metadata"]; ok {
		t.Fatalf("пустой покупатель не должен давать metadata: %v", got[1])
	}
}
