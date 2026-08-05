package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

func TestTributeAmount(t *testing.T) {
	cases := []struct {
		minor int64
		cur   string
		want  string
	}{
		{1000, "eur", "10.00 EUR"},
		{700, "usd", "7.00 USD"},
		{49900, "rub", "499.00 ₽"},
		{99, "rub", "0.99 ₽"},
		{0, "", "0.00 ₽"},
	}
	for _, c := range cases {
		if got := tributeAmount(c.minor, c.cur); got != c.want {
			t.Errorf("tributeAmount(%d, %q) = %q, ожидалось %q", c.minor, c.cur, got, c.want)
		}
		// Строка платежа разбирается обратно (процент рефереру, статистика, чеки).
		if want := float64(c.minor) / 100; parseAmountRub(tributeAmount(c.minor, c.cur)) != want {
			t.Errorf("parseAmountRub(%q) = %v, ожидалось %v", tributeAmount(c.minor, c.cur), parseAmountRub(tributeAmount(c.minor, c.cur)), want)
		}
	}
}

func tributeApp() *App {
	return &App{log: slog.Default(), botCfg: &model.BotConfig{
		Installed: true, Language: "ru",
		Tribute: model.TributeConfig{Enabled: true, APIKey: "key"},
	}}
}

func tributeSigned(t *testing.T, a *App, payload map[string]any) (bool, error) {
	t.Helper()
	body, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte("key"))
	mac.Write(body)
	return a.HandleTributeWebhook(context.Background(), hex.EncodeToString(mac.Sum(nil)), body)
}

// Кривая подпись должна давать 401, а не 500: на 5xx Tribute сутки повторяет
// доставку, а чужой запрос ретраить незачем.
func TestTributeWebhook_BadSignatureUnauthorized(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"name": "new_subscription"})
	handled, err := tributeApp().HandleTributeWebhook(context.Background(), "deadbeef", body)
	if handled {
		t.Errorf("ожидалось handled=false")
	}
	if !errors.Is(err, web.ErrUnauthorized) {
		t.Errorf("ожидалась web.ErrUnauthorized, получено %v", err)
	}
}

// Кнопка «Тест» в кабинете Tribute шлёт событие без имени.
func TestTributeWebhook_TestEvent(t *testing.T) {
	handled, err := tributeSigned(t, tributeApp(), map[string]any{"name": ""})
	if err != nil || !handled {
		t.Errorf("тестовый вебхук должен приниматься, получено handled=%v err=%v", handled, err)
	}
}

// Отмена в Tribute выключает автопродление, но оплаченный период дорабатывает:
// доступ не трогаем, событие только фиксируем.
func TestTributeWebhook_Cancelled(t *testing.T) {
	handled, err := tributeSigned(t, tributeApp(), map[string]any{
		"name": "cancelled_subscription",
		"payload": map[string]any{
			"telegram_user_id": 777, "price": 1000, "currency": "rub",
			"trb_user_id": "T-31326", "telegram_username": "durov", "type": "regular",
			"expires_at": "2026-09-01T00:00:00Z",
		},
	})
	if err != nil || !handled {
		t.Errorf("ожидалось handled=true без ошибки, получено handled=%v err=%v", handled, err)
	}
}

func TestTributeWebhook_IgnoresOtherEvents(t *testing.T) {
	handled, err := tributeSigned(t, tributeApp(), map[string]any{
		"name":    "new_donation",
		"payload": map[string]any{"telegram_user_id": 777, "amount": 1000, "currency": "rub"},
	})
	if err != nil || !handled {
		t.Errorf("ожидалось handled=true без ошибки, получено handled=%v err=%v", handled, err)
	}
}

func TestTributeWho(t *testing.T) {
	var wh tributeWebhook
	if got := wh.who(); got != "" {
		t.Errorf("пустая нагрузка не должна давать приписку, получено %q", got)
	}
	wh.Payload.TrbUserID = "W-15408"
	wh.Payload.Type = "trial"
	if want := " (trb=W-15408, тип=trial)"; wh.who() != want {
		t.Errorf("who() = %q, ожидалось %q", wh.who(), want)
	}
}
