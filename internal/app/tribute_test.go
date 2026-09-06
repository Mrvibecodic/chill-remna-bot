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
	"time"

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

// tributeSaleApp — бот с настроенной сеткой, панелью-стабом и хранилищем:
// на нём проверяется ВЫДАЧА по вебхуку Tribute, а не только разбор события.
func tributeSaleApp(t *testing.T, patched *map[string]any) (*App, *fakeStore) {
	t.Helper()
	srv := snapPanel(t, patched)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Tribute = model.TributeConfig{Enabled: true, APIKey: "key"}
	a.msg = &fakeMsg{}
	return a, fs
}

func tributeEvent(t *testing.T, a *App, p map[string]any) (bool, error) {
	t.Helper()
	return tributeSigned(t, a, map[string]any{"name": "new_subscription", "payload": p})
}

// Купил неделю — получает неделю, а не календарный месяц.
func TestTributeWeekly_GivesSevenDays(t *testing.T) {
	var patched map[string]any
	a, fs := tributeSaleApp(t, &patched)
	const uid int64 = 555

	if handled, err := tributeEvent(t, a, map[string]any{
		"subscription_id": 7, "period": "weekly", "type": "regular",
		"price": 15000, "currency": "rub", "telegram_user_id": uid,
		"expires_at": "2030-01-08T00:00:00Z",
	}); err != nil || !handled {
		t.Fatalf("вебхук не обработан: handled=%v err=%v", handled, err)
	}

	got, _ := patched["expireAt"].(string)
	exp, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("панель не получила срок: %q", got)
	}
	// Панель в стабе держит конец 2030-01-01, неделя сверху = 2030-01-08.
	want := time.Date(2030, 1, 8, 0, 0, 0, 0, time.UTC)
	if !exp.Equal(want) {
		t.Fatalf("выдано до %s, ожидалось %s (неделя, а не месяц)", exp, want)
	}
	// В снимке сделки — фактические 7 дней, а не 30.
	u, _ := fs.GetUser(context.Background(), uid)
	if u == nil || u.Snapshot == nil {
		t.Fatal("снимок не сохранён")
	}
	if u.Snapshot.Days != 7 || u.Snapshot.BoughtDays != 7 {
		t.Fatalf("окно сделки: Days=%d BoughtDays=%d, ожидалось 7 и 7", u.Snapshot.Days, u.Snapshot.BoughtDays)
	}
}

// Незнакомый период не продаётся вовсе: бот не может продать срок, которого не
// понимает. Раньше он молча становился месяцем.
func TestTributeUnknownPeriod_NotSold(t *testing.T) {
	for _, period := range []string{"biweekly", "onetime", "", "5months"} {
		var patched map[string]any
		a, fs := tributeSaleApp(t, &patched)
		const uid int64 = 555
		if _, err := tributeEvent(t, a, map[string]any{
			"subscription_id": 7, "period": period, "type": "regular",
			"price": 15000, "currency": "rub", "telegram_user_id": uid,
			"expires_at": "2030-01-08T00:00:00Z",
		}); err != nil {
			t.Fatalf("period=%q: %v", period, err)
		}
		if patched != nil {
			t.Fatalf("period=%q: подписка выдана, хотя период неизвестен", period)
		}
		if u, _ := fs.GetUser(context.Background(), uid); u != nil && u.SubExpireAt != "" {
			t.Fatalf("period=%q: срок проставлен: %q", period, u.SubExpireAt)
		}
	}
}

// Триал и подарок Tribute — не оплата: подписку за них не выдаём. Триал у бота
// свой, бесплатный, и к платёжке отношения не имеет.
func TestTributeTrialAndGift_NotSold(t *testing.T) {
	for _, c := range []struct {
		kind  string
		price int64
	}{{"trial", 0}, {"gift", 0}, {"trial", 15000}, {"regular", 0}} {
		var patched map[string]any
		a, _ := tributeSaleApp(t, &patched)
		if _, err := tributeEvent(t, a, map[string]any{
			"subscription_id": 7, "period": "monthly", "type": c.kind,
			"price": c.price, "currency": "rub", "telegram_user_id": int64(555),
			"expires_at": "2030-02-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("тип=%s цена=%d: %v", c.kind, c.price, err)
		}
		if patched != nil {
			t.Fatalf("тип=%s цена=%d: выдана подписка за бесплатное событие", c.kind, c.price)
		}
	}
}

// Возврат помечает платёж возвращённым: он перестаёт быть выручкой и перестаёт
// закрывать тарифы «только новым». Доступ при этом не отзывается.
func TestTributeRefund_MarksPayment(t *testing.T) {
	var patched map[string]any
	a, fs := tributeSaleApp(t, &patched)
	ctx := context.Background()
	const uid int64 = 555
	_ = fs.AddPayment(ctx, &model.Payment{
		TelegramID: uid, Method: model.PayMethodTribute, Months: 1,
		Amount: "150 ₽", Status: model.PaymentPaid, ExtID: "purchase-1",
	})
	if paid, _ := fs.HasPaidPayment(ctx, uid); !paid {
		t.Fatal("платёж не записан")
	}

	if handled, err := tributeSigned(t, a, map[string]any{
		"name": "digital_product_refunded",
		"payload": map[string]any{
			"purchase_id": "purchase-1", "amount": 15000, "currency": "rub",
			"telegram_user_id": uid, "refund_reason": "telegram_refund",
		},
	}); err != nil || !handled {
		t.Fatalf("возврат не обработан: handled=%v err=%v", handled, err)
	}
	if paid, _ := fs.HasPaidPayment(ctx, uid); paid {
		t.Fatal("после возврата платёж всё ещё считается оплаченным")
	}
}
