package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func TestMoneyHelpers(t *testing.T) {
	cases := []struct {
		in string
		k  int64
		ok bool
	}{
		{"150", 15000, true}, {"150.50", 15050, true}, {"150,5", 15050, true},
		{"0", 0, true}, {"", 0, false}, {"abc", 0, false}, {"-5", 0, false},
	}
	for _, c := range cases {
		k, ok := rubToKopecks(c.in)
		if ok != c.ok || (ok && k != c.k) {
			t.Fatalf("rubToKopecks(%q)=%d,%v want %d,%v", c.in, k, ok, c.k, c.ok)
		}
	}
	if kopecksToRub(15000) != "150" || kopecksToRub(15050) != "150.50" {
		t.Fatalf("kopecksToRub mismatch: %s %s", kopecksToRub(15000), kopecksToRub(15050))
	}
}

func TestBalance_TopUpIdempotentAndAutopay(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}}`))
	}))
	defer panel.Close()

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		store: fs,
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Pricing: model.Pricing{Currency: "₽", Base: map[int]string{1: "150"}}}
	a.botCfg.NormalizePricing()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	if err := a.finalizeTopUp(ctx, u, 50000, model.PayMethodYooKassa, "500 ₽", "yk_top1"); err != nil {
		t.Fatal(err)
	}
	if err := a.finalizeTopUp(ctx, u, 50000, model.PayMethodYooKassa, "500 ₽", "yk_top1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 50000 {
		t.Fatalf("после двух одинаковых топ-апов баланс должен быть 50000, got %+v", got)
	}

	a.onBuyPlan(ctx, u, "1")
	a.payFromBalance(ctx, u)
	got, _ := fs.GetUser(ctx, u)
	if got == nil || got.Balance != 50000-15000 {
		t.Fatalf("после оплаты с баланса должно остаться 35000, got %+v", got)
	}
	if !strings.Contains(fm.joined(), "sub/x") {
		t.Fatalf("после оплаты с баланса не выдана ссылка:\n%s", fm.joined())
	}

	_, _ = fs.DeductBalance(ctx, u, 34000)
	n0 := len(fm.texts)
	a.payFromBalance(ctx, u)
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 1000 {
		t.Fatalf("при нехватке баланс не должен меняться, got %+v", got)
	}
	if len(fm.texts) <= n0 {
		t.Fatalf("ожидалось сообщение о нехватке баланса")
	}
}

// Двойной запрос «оплатить с баланса» из мини-аппа списывает ОДИН раз.
// Веб-хендлеры конкурентны, барьер по ключу сделки — единственная защита:
// пустой ключ выключал и замок, и проверку «уже выдано», и уникальный индекс.
func TestMiniCheckoutBalance_NoDoubleCharge(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	ctx := context.Background()
	const uid int64 = 555
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 99000*3)
	p := vipPlan(t, fs, model.PlanAvailAll)

	start := int64(99000 * 3)
	var wg sync.WaitGroup
	oks := make([]bool, 8)
	for i := range oks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			oks[i] = a.MiniCheckout(ctx, uid, p.Code, 1, model.PayMethodBalance, "", false).OK
		}(i)
	}
	wg.Wait()

	okCount := 0
	for _, v := range oks {
		if v {
			okCount++
		}
	}
	if okCount != 1 {
		t.Fatalf("успешных покупок: %d, ожидалась 1", okCount)
	}
	u, _ := fs.GetUser(ctx, uid)
	if u == nil || u.Balance != start-99000 {
		t.Fatalf("списано не 99000: баланс %d, ожидался %d", u.Balance, start-99000)
	}
	paid := 0
	for _, x := range fs.pays {
		if x.Method == "balance" {
			paid++
		}
	}
	if paid != 1 {
		t.Fatalf("записей платежа: %d, ожидалась 1", paid)
	}
}

// Ключ сделки не блокирует законную повторную покупку: после успешной выдачи
// конец срока сдвинулся, значит и ключ другой.
func TestBalanceExtID_DistinguishesPurchases(t *testing.T) {
	first := balanceExtID(555, "vip", 1, "exp:2030-01-01T00:00:00Z")
	same := balanceExtID(555, "vip", 1, "exp:2030-01-01T00:00:00Z")
	later := balanceExtID(555, "vip", 1, "exp:2030-06-01T00:00:00Z")
	other := balanceExtID(556, "vip", 1, "exp:2030-01-01T00:00:00Z")
	plan := balanceExtID(555, "base", 1, "exp:2030-01-01T00:00:00Z")

	if first != same {
		t.Fatal("повтор одного нажатия обязан давать тот же ключ")
	}
	for name, v := range map[string]string{"другой срок": later, "другой человек": other, "другой тариф": plan} {
		if v == first {
			t.Fatalf("%s обязан давать другой ключ", name)
		}
	}
	if balanceExtID(555, "vip", 1, "") != "" {
		t.Fatal("без различителя ключа быть не должно — иначе законная покупка заблокируется навсегда")
	}
}
