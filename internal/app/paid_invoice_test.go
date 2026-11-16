package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"remnabot/internal/cryptobot"
	"remnabot/internal/model"
	"remnabot/internal/platega"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
	"remnabot/internal/yookassa"
)

const paidUID int64 = 1000000001

// paidFixture — бот с настоящей базой, панелью-заглушкой и заглушками ЮKassa
// и CryptoBot, которые любой счёт называют оплаченным. onGateway зовётся до
// ответа платёжки: так воспроизводится событие, случившееся, пока шёл запрос.
// cbPayload — что CryptoBot вернёт в payload счёта.
func paidFixture(t *testing.T, cbPayload string, onGateway func(id string)) (*App, *fakeMsg, storage.Storage, *atomic.Int32) {
	t.Helper()
	var writes atomic.Int32
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub.example.com/x"}}`))
	}))
	t.Cleanup(panel.Close)

	yk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if onGateway != nil {
			onGateway(id)
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","status":"succeeded","paid":true,"amount":{"value":"150.00","currency":"RUB"},"metadata":{"months":"1","telegram_id":"` + strconv.FormatInt(paidUID, 10) + `"}}`))
	}))
	t.Cleanup(yk.Close)
	oldYK := yookassa.BaseURL
	yookassa.BaseURL = yk.URL
	t.Cleanup(func() { yookassa.BaseURL = oldYK })

	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"items":[{"invoice_id":77,"status":"paid","asset":"USDT","amount":"2","payload":"` + cbPayload + `"}]}}`))
	}))
	t.Cleanup(cbSrv.Close)
	oldCB := cryptobot.BaseURL
	cryptobot.BaseURL = cbSrv.URL
	t.Cleanup(func() { cryptobot.BaseURL = oldCB })

	a, fm, st := bkTestApp(t)
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec", ReturnURL: "https://t.me"}
	a.botCfg.CryptoBot = model.CryptoBotConfig{Enabled: true, Token: "token"}
	a.botCfg.Pricing = model.Pricing{Currency: "руб", Base: map[int]string{1: "150", 3: "400"}}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	_ = st.UpsertUser(context.Background(), paidUID)
	return a, fm, st, &writes
}

func ykPaidBody(id string) []byte {
	return []byte(`{"event":"payment.succeeded","object":{"id":"` + id + `"}}`)
}

// Счёт оплачен, а пользователя к этому моменту удалили. Вебхук — обычный путь
// оплаты — обязан ответить так же, как сверка: не выдавать, закрыть счёт и
// один раз позвать администратора.
func TestPaidInvoiceOfDeletedUserGoesToAdmin(t *testing.T) {
	a, fm, st, writes := paidFixture(t, "", nil)
	ctx := context.Background()

	// Контроль: живому пользователю тот же вебхук подписку выдаёт.
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay_live", TelegramID: paidUID, Months: 1})
	if handled, err := a.HandleYooKassaWebhook(ctx, ykPaidBody("pay_live")); err != nil || !handled {
		t.Fatalf("живой пользователь: handled=%v err=%v", handled, err)
	}
	if paid, _ := st.PaymentByExtID(ctx, "pay_live"); !paid {
		t.Fatal("живому пользователю оплата не выдана — проверка ниже ничего бы не доказала")
	}
	before := writes.Load()

	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay_gone", TelegramID: paidUID, Months: 1})
	if err := st.DeleteUser(ctx, paidUID); err != nil {
		t.Fatal(err)
	}
	// Платёжка повторяет доставку — второй раз администратора не зовём.
	for i := 0; i < 2; i++ {
		if handled, err := a.HandleYooKassaWebhook(ctx, ykPaidBody("pay_gone")); err != nil || !handled {
			t.Fatalf("доставка %d: handled=%v err=%v", i+1, handled, err)
		}
	}
	if paid, _ := st.PaymentByExtID(ctx, "pay_gone"); paid {
		t.Fatal("оплата удалённого пользователя выдана")
	}
	if writes.Load() != before {
		t.Fatalf("панель изменена %d раз", writes.Load()-before)
	}
	if u, _ := st.GetUser(ctx, paidUID); u != nil {
		t.Fatal("удалённый пользователь заведён заново")
	}
	if n := strings.Count(fm.joined(), "удалённого пользователя"); n != 1 {
		t.Fatalf("сообщений администратору: %d, нужно 1:\n%s", n, fm.joined())
	}
	left, _ := st.ListUnresolvedPending(ctx, "9999-01-01T00:00:00Z", 10)
	for _, p := range left {
		if p.ExtID == "pay_gone" {
			t.Fatalf("счёт не закрыт: %+v", p)
		}
	}
}

// Вебхук выдал подписку, пока кнопка «Проверить оплату» ждала ответ ЮKassa.
// Оплата прошла — человек должен увидеть свою подписку, а не ошибку.
func TestYooKassaCheckAfterWebhookShowsSubscription(t *testing.T) {
	var st storage.Storage
	a, fm, st, _ := paidFixture(t, "", func(id string) {
		if id == "pay_race" {
			_ = st.AddPayment(context.Background(), &model.Payment{
				TelegramID: paidUID, Method: model.PayMethodYooKassa, Months: 1,
				Amount: "150.00 RUB", Status: model.PaymentPaid, ExtID: id,
			})
		}
	})
	ctx := context.Background()
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay_race", TelegramID: paidUID, Months: 1})

	a.handleCallback(ctx, cb(paidUID, "ykc:pay_race"))

	all := fm.joined()
	if strings.Contains(all, "Что-то пошло не так") || strings.Contains(all, "временно недоступен") {
		t.Fatalf("после успешной оплаты показана ошибка:\n%s", all)
	}
	if !strings.Contains(all, "подписок") {
		t.Fatalf("экран подписки не показан:\n%s", all)
	}
}

// Кнопка «Проверить оплату» CryptoBot: получатель и срок берутся из своей
// строки счёта, payload платёжки — только подсказка. Так же решает вебхук.
func TestCryptoBotCheckTrustsInvoiceRow(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"чужой получатель и срок", "1000000002:1"},
		{"payload не разобрался", "мусор"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, st, _ := paidFixture(t, tc.payload, nil)
			ctx := context.Background()
			_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: "cb:77", TelegramID: paidUID, Months: 3})

			a.handleCallback(ctx, cb(paidUID, "cbc:77:1"))

			pays, _, err := st.ListPayments(ctx, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(pays) != 1 {
				t.Fatalf("платежей %d, нужен один:\n%s", len(pays), fm.joined())
			}
			if p := pays[0]; p.TelegramID != paidUID || p.Months != 3 || p.ExtID != "cb:77" {
				t.Fatalf("выдано не по строке счёта: %+v", p)
			}
		})
	}
}

// Уведомление Platega и кнопка «Проверить оплату» приходят разом: выдаёт
// первый, второй упирается в уже записанный платёж. Это штатный исход, а не
// сбой — в журнале не должно появляться ошибки, которую кто-то пойдёт разбирать.
func TestPlategaDuplicateFinalizeIsQuiet(t *testing.T) {
	a, _, st, _ := paidFixture(t, "", nil)
	ctx := context.Background()
	var buf bytes.Buffer
	a.log = slog.New(slog.NewTextHandler(&buf, nil))
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodPlatega, ExtID: "tx-1", TelegramID: paidUID, Months: 1})
	tx := &platega.Transaction{ID: "tx-1", Status: "CONFIRMED", Amount: 150, Currency: "RUB", Payload: plPayload(paidUID, 1)}

	a.finalizePlatega(ctx, "tx-1", tx)
	a.finalizePlatega(ctx, "tx-1", tx)

	if _, n, _ := st.ListPayments(ctx, 10, 0); n != 1 {
		t.Fatalf("платежей %d, ожидался один", n)
	}
	if strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("повторный разбор оплаченного счёта записан как ошибка:\n%s", buf.String())
	}
}
