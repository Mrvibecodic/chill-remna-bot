package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"testing"

	"remnabot/internal/model"
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

// Вебхук без панели дальше finalize не уходит, но до этого момента сумма уже
// посчитана — проверяем, что подпись сходится и событие принято.
func TestTributeWebhook_BadSignature(t *testing.T) {
	a := &App{log: slog.Default(), botCfg: &model.BotConfig{
		Installed: true, Language: "ru",
		Tribute: model.TributeConfig{Enabled: true, APIKey: "key"},
	}}
	body, _ := json.Marshal(map[string]any{"name": "new_subscription"})
	if handled, err := a.HandleTributeWebhook(context.Background(), "deadbeef", body); err == nil || handled {
		t.Errorf("ожидался отказ по подписи, получено handled=%v err=%v", handled, err)
	}
}

func TestTributeWebhook_IgnoresOtherEvents(t *testing.T) {
	a := &App{log: slog.Default(), botCfg: &model.BotConfig{
		Installed: true, Language: "ru",
		Tribute: model.TributeConfig{Enabled: true, APIKey: "key"},
	}}
	body, _ := json.Marshal(map[string]any{
		"name":    "cancelled_subscription",
		"payload": map[string]any{"telegram_user_id": 777, "price": 1000, "currency": "rub"},
	})
	mac := hmac.New(sha256.New, []byte("key"))
	mac.Write(body)
	handled, err := a.HandleTributeWebhook(context.Background(), hex.EncodeToString(mac.Sum(nil)), body)
	if err != nil || !handled {
		t.Errorf("ожидалось handled=true без ошибки, получено handled=%v err=%v", handled, err)
	}
}
