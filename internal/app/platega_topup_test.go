package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/platega"
)

// plTopUpStub поднимает заглушку API Platega: создание транзакции запоминает
// тело запроса, статус отдаёт CONFIRMED с тем payload, что пришёл при создании.
func plTopUpStub(t *testing.T, enabled bool) (*App, *fakeMsg, *fakeStore, *map[string]any) {
	t.Helper()
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transaction/process":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &created)
			_, _ = w.Write([]byte(`{"transactionId":"tx-t1","redirect":"https://pay.example.test/x","status":"PENDING"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/transaction/tx-t1":
			payload, _ := created["payload"].(string)
			resp, _ := json.Marshal(map[string]any{
				"id": "tx-t1", "status": "CONFIRMED", "payload": payload,
				"paymentDetails": map[string]any{"amount": 123.45, "currency": "RUB"},
			})
			_, _ = w.Write(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := platega.BaseURL
	platega.BaseURL = srv.URL
	t.Cleanup(func() { platega.BaseURL = old })

	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		store: fs,
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Pricing: model.Pricing{Currency: "₽", Base: map[int]string{1: "150"}},
		Wallet:  model.WalletConfig{TopUp: true, Init: true},
		Platega: model.PlategaConfig{Enabled: enabled, MerchantID: "m-1", Secret: "s-1", Method: platega.MethodSBP},
	}
	a.botCfg.NormalizePricing()
	return a, fm, fs, &created
}

func TestPlategaTopUp_InvoiceAndWebhookCredit(t *testing.T) {
	a, _, fs, created := plTopUpStub(t, true)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	payURL, txID, err := a.topUpCreate(ctx, u, 12345, "pl", false)
	if err != nil || payURL != "https://pay.example.test/x" || txID != "tx-t1" {
		t.Fatalf("счёт не создан: url=%q tx=%q err=%v", payURL, txID, err)
	}
	c := *created
	det, _ := c["paymentDetails"].(map[string]any)
	if det["amount"] != 123.45 || det["currency"] != "RUB" {
		t.Fatalf("сумма счёта: %v", det)
	}
	if c["payload"] != "telegram_id=555&topup=12345" || c["paymentMethod"] != float64(platega.MethodSBP) {
		t.Fatalf("тело запроса: %v", c)
	}
	if c["return"] == "" || c["failedUrl"] == "" || c["description"] != "Пополнение баланса" {
		t.Fatalf("обязательные поля: %v", c)
	}
	p, _ := fs.PendingByExtID(ctx, "tx-t1")
	if p == nil || p.Purpose != purposeTopUp || p.Kopecks != 12345 || p.Months != 0 || p.TelegramID != u {
		t.Fatalf("строка счёта: %+v", p)
	}

	body := []byte(`{"id":"tx-t1","amount":123.45,"currency":"RUB","status":"CONFIRMED","paymentMethod":2}`)
	for i := 0; i < 2; i++ {
		if ok, err := a.HandlePlategaWebhook(ctx, "m-1", "s-1", body); err != nil || !ok {
			t.Fatalf("вебхук: ok=%v err=%v", ok, err)
		}
	}
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 12345 {
		t.Fatalf("баланс после двух доставок: %+v", got)
	}
}

// Строка счёта потерялась — пополнение восстанавливается из payload и не
// превращается в «срок не определён».
func TestPlategaTopUp_LostPendingUsesPayload(t *testing.T) {
	a, fm, fs, _ := plTopUpStub(t, true)
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	a.finalizePlatega(ctx, "tx-9", &platega.Transaction{
		ID: "tx-9", Status: "CONFIRMED", Amount: 50, Currency: "RUB",
		Payload: "telegram_id=555&topup=5000",
	})
	if got, _ := fs.GetUser(ctx, u); got == nil || got.Balance != 5000 {
		t.Fatalf("баланс не зачислен: %+v", got)
	}
	if strings.Contains(fm.joined(), "срок подписки определить не удалось") {
		t.Fatalf("пополнение ушло в «срок не определён»:\n%s", fm.joined())
	}
}

// Способ виден и принимает пополнение только включённым — в чате, мини-аппе
// и в самом ядре.
func TestPlategaTopUp_MethodGate(t *testing.T) {
	ctx := context.Background()
	const u int64 = 555

	a, _, fs, _ := plTopUpStub(t, false)
	_ = fs.UpsertUser(ctx, u)
	if _, _, err := a.topUpCreate(ctx, u, 10000, "pl", false); err == nil {
		t.Fatal("выключенная Platega приняла пополнение")
	}
	if r := a.MiniTopUp(ctx, u, 10000, "pl", false); r.Error == "" {
		t.Fatalf("мини-апп принял пополнение выключенной Platega: %+v", r)
	}
	if got := a.MiniTopUpOptions(ctx, u).Methods; len(got) != 0 {
		t.Fatalf("мини-апп предлагает выключенный способ: %v", got)
	}

	a, fm, fs, _ := plTopUpStub(t, true)
	_ = fs.UpsertUser(ctx, u)
	if got := a.MiniTopUpOptions(ctx, u).Methods; len(got) != 1 || got[0] != "pl" {
		t.Fatalf("мини-апп не предлагает Platega: %v", got)
	}
	a.getUI(u).topUpKopecks = 10000
	a.showTopUpMethods(ctx, u)
	if !hasCB(fm.cbData, "top:m:pl") {
		t.Fatalf("в чате нет кнопки Platega: %v", fm.cbData)
	}
	a.handleCallback(ctx, cb(u, "top:m:pl"))
	if !hasCB(fm.cbData, "plc:tx-t1") {
		t.Fatalf("нет кнопки проверки оплаты: %v\n%s", fm.cbData, fm.joined())
	}
}

func TestParsePlPayload_TopUp(t *testing.T) {
	tg, months, k := parsePlPayloadAll("telegram_id=555&topup=12345")
	if tg != 555 || months != 0 || k != 12345 {
		t.Fatalf("tg=%d months=%d topup=%d", tg, months, k)
	}
	if tg, months, k := parsePlPayloadAll("telegram_id=-100&amp;topup=500"); tg != -100 || months != 0 || k != 500 {
		t.Fatalf("кабинетный аккаунт: tg=%d months=%d topup=%d", tg, months, k)
	}
}
