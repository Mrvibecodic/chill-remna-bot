package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/yookassa"
)

func mockYooKassa(t *testing.T, payment map[string]any) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payment)
	}))
	prev := yookassa.BaseURL
	yookassa.BaseURL = srv.URL
	return func() {
		yookassa.BaseURL = prev
		srv.Close()
	}
}

func installedYK() *App {
	return &App{
		log: slog.Default(),
		botCfg: &model.BotConfig{
			Installed: true, Language: "ru",
			YooKassa: model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec"},
		},
	}
}

func TestYooKassaWebhook_SkipUnknownEvent(t *testing.T) {
	a := &App{log: slog.Default()}
	body, _ := json.Marshal(map[string]any{
		"event":  "refund.succeeded",
		"object": map[string]any{"id": "pay_x", "paid": false},
	})
	handled, err := a.HandleYooKassaWebhook(context.Background(), body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if handled {
		t.Errorf("ожидалось handled=false для неизвестного события")
	}
}

// TestYooKassaWebhook_CanceledHandled — payment.canceled обрабатывается (снятие
// pending, запись в журнал), но ничего не выдаёт.
func TestYooKassaWebhook_CanceledHandled(t *testing.T) {
	a := &App{log: slog.Default()}
	body, _ := json.Marshal(map[string]any{
		"event":  "payment.canceled",
		"object": map[string]any{"id": "pay_x", "paid": false},
	})
	handled, err := a.HandleYooKassaWebhook(context.Background(), body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !handled {
		t.Errorf("ожидалось handled=true для canceled")
	}
}

func TestYooKassaWebhook_ForgedNotConfirmed(t *testing.T) {
	defer mockYooKassa(t, map[string]any{
		"id": "pay_forged", "status": "pending", "paid": false,
		"amount":   map[string]string{"value": "150.00", "currency": "RUB"},
		"metadata": map[string]string{"telegram_id": "777", "months": "1"},
	})()
	a := installedYK()
	body, _ := json.Marshal(map[string]any{
		"event": "payment.succeeded",
		"object": map[string]any{
			"id": "pay_forged", "paid": true, "status": "succeeded",
			"metadata": map[string]string{"telegram_id": "777", "months": "1"},
		},
	})
	handled, err := a.HandleYooKassaWebhook(context.Background(), body)
	if err != nil {
		t.Fatalf("неожиданная ошибка (дошло до finalize вместо отклонения): %v", err)
	}
	if !handled {
		t.Errorf("ожидалось handled=true (finalize не выполнен)")
	}
}

func TestYooKassaWebhook_VerifiedNoMetadata(t *testing.T) {
	defer mockYooKassa(t, map[string]any{
		"id": "pay_no_meta", "status": "succeeded", "paid": true,
		"amount":   map[string]string{"value": "150.00", "currency": "RUB"},
		"metadata": map[string]string{},
	})()
	a := installedYK()
	body, _ := json.Marshal(map[string]any{
		"event":  "payment.succeeded",
		"object": map[string]any{"id": "pay_no_meta"},
	})
	handled, err := a.HandleYooKassaWebhook(context.Background(), body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !handled {
		t.Errorf("ожидалось handled=true")
	}
}

func TestYooKassaWebhook_NotConfigured(t *testing.T) {
	a := &App{log: slog.Default(), botCfg: &model.BotConfig{Installed: true, Language: "ru"}}
	body, _ := json.Marshal(map[string]any{
		"event":  "payment.succeeded",
		"object": map[string]any{"id": "pay_x"},
	})
	handled, err := a.HandleYooKassaWebhook(context.Background(), body)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !handled {
		t.Errorf("ожидалось handled=true")
	}
}

func TestYooKassaWebhook_BadJSON(t *testing.T) {
	a := &App{log: slog.Default()}
	handled, err := a.HandleYooKassaWebhook(context.Background(), []byte("{bad"))
	if err == nil {
		t.Fatalf("ожидалась ошибка парсинга")
	}
	if handled {
		t.Errorf("при ошибке парсинга handled должен быть false")
	}
}
