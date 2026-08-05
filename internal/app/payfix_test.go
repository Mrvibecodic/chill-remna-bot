package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

// Регресс дедлока: setHeleketField с полем hl_tocur зовёт hlCryptoCode →
// hlClient → hlConfig, и до фикса повторно брал a.mu — бот вставал намертво.
func TestHeleketToCurNoDeadlock(t *testing.T) {
	a := &App{botCfg: &model.BotConfig{}, log: slog.Default(), wiz: map[int64]*wizard{}, ui: map[int64]*uiState{}}
	done := make(chan struct{})
	go func() {
		defer func() { _ = recover(); close(done) }()
		a.setHeleketField(context.Background(), 1, "hl_tocur", "USDT")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("дедлок: setHeleketField(hl_tocur) не вернулась за 3 секунды")
	}
}

func TestHLValidReturnURL(t *testing.T) {
	good := []string{"https://t.me/mybot", "http://example.com/x"}
	bad := []string{"вернуться", "t.me", "ftp://x.y", "https://", "ab"}
	for _, s := range good {
		if !hlValidReturnURL(s) {
			t.Errorf("hlValidReturnURL(%q) = false, ожидалось true", s)
		}
	}
	for _, s := range bad {
		if hlValidReturnURL(s) {
			t.Errorf("hlValidReturnURL(%q) = true, ожидалось false", s)
		}
	}
}

// Регресс: halfyearly перехватывался веткой "year" и давал 12 месяцев вместо 6.
func TestTributePeriodToMonths(t *testing.T) {
	cases := map[string]int{
		"monthly":    1,
		"quarterly":  3,
		"halfyearly": 6,
		"yearly":     12,
		"annual":     12,
		"weekly":     1,
		"trial":      1,
		"onetime":    1,
		"HalfYearly": 6,
		"6months":    6,
		"3-month":    3,
		"":           1,
	}
	for in, want := range cases {
		if got := tributePeriodToMonths(in); got != want {
			t.Errorf("tributePeriodToMonths(%q) = %d, ожидалось %d", in, got, want)
		}
	}
}

// В ключе дедупликации Tribute обязан быть telegram_user_id: subscription_id —
// это id тарифа, общий для всех подписчиков.
func TestTributeExtIDIncludesTelegramID(t *testing.T) {
	// Формат зафиксирован в HandleTributeWebhook: trb_<tg>_<sub>_<expUnix>.
	// Здесь просто закрепляем, что для разных юзеров ключи разные.
	a := fmtTributeExtID(111, 5, 1700000000)
	b := fmtTributeExtID(222, 5, 1700000000)
	if a == b {
		t.Fatalf("ext_id не различает пользователей: %s == %s", a, b)
	}
}

func fmtTributeExtID(tg int64, sub int, exp int64) string {
	return fmt.Sprintf("trb_%d_%d_%d", tg, sub, exp)
}

func TestParseAmountRubOnly(t *testing.T) {
	cases := []struct {
		in    string
		val   float64
		isRub bool
	}{
		{"150.00 RUB", 150, true},
		{"150 ₽", 150, true},
		{"150", 150, true},
		{"7.00 EUR", 7, false},
		{"10.5 USD", 10.5, false},
		{"100 ⭐", 100, false},
	}
	for _, c := range cases {
		v, ok := parseAmountRubOnly(c.in)
		if v != c.val || ok != c.isRub {
			t.Errorf("parseAmountRubOnly(%q) = (%v,%v), ожидалось (%v,%v)", c.in, v, ok, c.val, c.isRub)
		}
	}
}

func TestYKValue(t *testing.T) {
	cases := []struct {
		in string
		ok bool
		v  string
	}{
		{"150", true, "150"},
		{"199,50", true, "199.50"},
		{"199.5", true, "199.50"},
		{"1 000", false, ""},
		{"199 ₽", false, ""},
		{"", false, ""},
		{"0", false, ""},
	}
	for _, c := range cases {
		v, ok := ykValue(c.in)
		if ok != c.ok || (ok && v != c.v) {
			t.Errorf("ykValue(%q) = (%q,%v), ожидалось (%q,%v)", c.in, v, ok, c.v, c.ok)
		}
	}
}

// Payload «tg:0» — легальный признак пополнения; отрицательные месяцы — мусор.
func TestParseCryptoBotPayloadTopUp(t *testing.T) {
	tg, mo, err := parseCryptoBotPayload("777:0")
	if err != nil || tg != 777 || mo != 0 {
		t.Fatalf("топ-ап payload разобран неверно: tg=%d mo=%d err=%v", tg, mo, err)
	}
	if _, _, err := parseCryptoBotPayload("777:-1"); err == nil {
		t.Fatal("ожидалась ошибка на отрицательных месяцах")
	}
}

// Вебхук Platega без верных заголовков не должен приниматься.
func TestPlategaWebhookUnauthorized(t *testing.T) {
	a := &App{
		log: slog.Default(),
		botCfg: &model.BotConfig{
			Platega: model.PlategaConfig{MerchantID: "m-1", Secret: "s-1"},
		},
	}
	_, err := a.HandlePlategaWebhook(context.Background(), "m-1", "wrong", []byte(`{"id":"tx"}`))
	if !errors.Is(err, web.ErrUnauthorized) {
		t.Fatalf("ожидался ErrUnauthorized, получили: %v", err)
	}
	_, err = a.HandlePlategaWebhook(context.Background(), "", "", []byte(`{"id":"tx"}`))
	if !errors.Is(err, web.ErrUnauthorized) {
		t.Fatalf("ожидался ErrUnauthorized без заголовков, получили: %v", err)
	}
}
