package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"remnabot/internal/storage"
	"remnabot/internal/yookassa"
)

// ykCheckFixture — ЮKassa отвечает «оплачено» на любой платёж, панель
// считает изменения.
func ykCheckFixture(t *testing.T) (*App, *fakeMsg, *atomic.Int32) {
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
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/yk"}}`))
	}))
	t.Cleanup(panel.Close)
	yk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_, _ = w.Write([]byte(`{"id":"` + id + `","status":"succeeded","paid":true,"amount":{"value":"150.00","currency":"RUB"},"metadata":{"months":"1","telegram_id":"555"}}`))
	}))
	t.Cleanup(yk.Close)
	oldBase := yookassa.BaseURL
	yookassa.BaseURL = yk.URL
	t.Cleanup(func() { yookassa.BaseURL = oldBase })

	a, fm, st := bkTestApp(t)
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, ShopID: "shop", SecretKey: "sec", ReturnURL: "https://t.me"}
	a.botCfg.Pricing = model.Pricing{Currency: "руб", Base: map[int]string{1: "150"}}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})
	_ = st.UpsertUser(context.Background(), 555)
	return a, fm, &writes
}

// Старая кнопка «Проверить оплату» после восстановления: счёт выставлен до
// восстановления — подписка не выдаётся, оплата уходит админу один раз,
// человек узнаёт, что её проверят.
func TestOldCheckButtonHeldAfterRestore(t *testing.T) {
	a, fm, writes := ykCheckFixture(t)
	st := a.store
	ctx := context.Background()
	at := time.Now().UTC()
	a.bkState.Guard = &model.RestoreGuard{At: at.Format(time.RFC3339), Synced: true}
	// Счёт из файла и счёт, выставленный между датой файла и
	// восстановлением (в базе его нет вовсе).
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay_old", TelegramID: 555, Months: 1,
		CreatedAt: at.Add(-time.Hour).Format(time.RFC3339)})
	for _, id := range []string{"pay_old", "pay_gap", "pay_old"} {
		a.handleCallback(ctx, cb(555, "ykc:"+id))
		if paid, _ := st.PaymentByExtID(ctx, id); paid {
			t.Fatalf("%s: оплата до восстановления выдана снова", id)
		}
	}
	if writes.Load() != 0 {
		t.Fatalf("панель изменена %d раз", writes.Load())
	}
	all := fm.joined()
	if strings.Count(all, "pay_old") != 1 || strings.Count(all, "pay_gap") != 1 {
		t.Fatalf("список админу: %q", all)
	}
	if strings.Count(all, "проверит администратор") != 3 {
		t.Fatalf("человеку не сказали, что оплату проверят: %q", all)
	}
	if left, _ := st.ListUnresolvedPending(ctx, time.Now().UTC().Add(time.Hour).Format(time.RFC3339), 10); len(left) != 0 {
		t.Fatal("счёт не закрыт после отправки админу")
	}

	// Счёт, выставленный после восстановления, выдаётся как обычно.
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay_new", TelegramID: 555, Months: 1,
		CreatedAt: at.Add(time.Minute).Format(time.RFC3339)})
	a.handleCallback(ctx, cb(555, "ykc:pay_new"))
	if paid, _ := st.PaymentByExtID(ctx, "pay_new"); !paid {
		t.Fatalf("свежая оплата не выдана: %q", lastText(fm))
	}
}

// Пополнение по счёту до восстановления не зачисляется: баланс из файла
// уже не знает, что эти деньги потрачены.
func TestTopUpHeldAfterRestore(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000001)
	a.bkState.Guard = &model.RestoreGuard{At: time.Now().UTC().Format(time.RFC3339), Synced: true}
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodCryptoBot, ExtID: "cb:7", TelegramID: 1000000001,
		Purpose: "topup", Kopecks: 50000, CreatedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
	if err := a.finalizeTopUp(ctx, 1000000001, 50000, model.PayMethodCryptoBot, "5 USDT", "cb:7"); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUser(ctx, 1000000001); u.Balance != 0 {
		t.Fatalf("пополнение до восстановления зачислено: %d", u.Balance)
	}
	if all := fm.joined(); !strings.Contains(all, "cb:7") || !strings.Contains(all, "проверит администратор") {
		t.Fatalf("сообщения: %q", all)
	}
	// Другие способы (перевод по заявке) идут своим путём — их не держим.
	if err := a.finalizeTopUp(ctx, 1000000001, 100, model.PayMethodP2P, "1 RUB", "p2p:1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUser(ctx, 1000000001); u.Balance != 100 {
		t.Fatalf("пополнение переводом: %d", u.Balance)
	}
}

// Не дошедший до админа список остаётся в очереди и уходит в следующий раз;
// закрывается только доставленное.
func TestFlushReviewKeepsUndelivered(t *testing.T) {
	a, fm, _ := bkTestApp(t)
	ctx := context.Background()
	sent := 0
	a.addReview("x", "", 1, "1", 1, "e1", func(context.Context) { sent++ })
	fm.kbFail = true
	a.flushReview(ctx)
	if sent != 0 || len(a.reviewItems) != 1 {
		t.Fatalf("недоставленный список: sent=%d в очереди=%d", sent, len(a.reviewItems))
	}
	a.addReview("x", "", 1, "1", 1, "e1", func(context.Context) { sent++ })
	fm.kbFail = false
	a.flushReview(ctx)
	if sent != 1 || len(a.reviewItems) != 0 {
		t.Fatalf("повтор: sent=%d в очереди=%d", sent, len(a.reviewItems))
	}
}

// Сверка лимитов не откатывает условия тому, чей срок разошёлся с файлом, —
// до его новой покупки после восстановления.
func TestSubRepairKeepsChangedUntilNewPurchase(t *testing.T) {
	a, fs, patches := repairFixture(t, 3, repairGB)
	ctx := context.Background()
	sold := &model.PlanSnapshot{Months: 1, DeviceLimit: 5, TrafficGB: 50}
	seedRepairUser(t, fs, time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339), sold)
	at := time.Now().UTC().Add(-time.Hour)
	a.bkState.Guard = &model.RestoreGuard{At: at.Format(time.RFC3339), Synced: true, Changed: []int64{555}}
	if st := a.repairSubscriptions(ctx); st.fixed != 0 || len(*patches) != 0 {
		t.Fatalf("условия откачены к покупке из файла: %+v", st)
	}
	_ = fs.AddPayment(ctx, &model.Payment{TelegramID: 555, Method: model.PayMethodYooKassa, Months: 1, Amount: "150",
		Status: model.PaymentPaid, ExtID: "yk_new", Snapshot: sold, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	if st := a.repairSubscriptions(ctx); st.fixed != 1 {
		t.Fatalf("после новой покупки сверка не чинит: %+v", st)
	}
}

// Заявки на перевод из файла: не дошёл список — пробуем снова, дошёл — больше
// не шлём.
func TestP2PReviewRetriedUntilDelivered(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000001)
	at := time.Now().UTC()
	if err := st.CreateP2PRequest(ctx, &model.P2PRequest{TelegramID: 1000000001, Months: 1, Price: "199 ₽",
		Status: model.P2PSubmitted, CreatedAt: at.Add(-time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	a.bkState.Guard = &model.RestoreGuard{At: at.Format(time.RFC3339), Synced: true}
	fm.kbFail = true
	a.guardTick(ctx, at)
	fm.kbFail = false
	if a.restoreGuard().P2PNoted {
		t.Fatal("недоставленный список заявок отмечен")
	}
	a.guardTick(ctx, at.Add(time.Minute)) // рано для повтора
	a.guardTick(ctx, at.Add(guardSyncRetry+time.Second))
	if !a.restoreGuard().P2PNoted {
		t.Fatal("список заявок не отправлен повторно")
	}
	a.guardTick(ctx, at.Add(time.Hour))
	if n := strings.Count(fm.joined(), "Заявки на перевод из восстановленной базы"); n != 2 {
		t.Fatalf("список заявок отправлялся %d раз (ожидалось: неудача и доставка)", n)
	}
}

// Окно звёзд — от даты файла до восстановления. Раньше даты файла каждая
// выданная оплата в файле есть; через два месяца после восстановления
// список админу больше не приходит.
func TestStarsReviewWindow(t *testing.T) {
	a, fm, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000002)
	a.botCfg.Stars.Enabled = true
	now := time.Now()
	tx := func(id string, at time.Time) models.StarTransaction {
		return models.StarTransaction{ID: id, Amount: 50, Date: int(at.Unix()),
			Source: &models.TransactionPartner{Type: models.TransactionPartnerTypeUser, User: &models.TransactionPartnerUser{
				User: models.User{ID: 1000000002}, InvoicePayload: "stars:1"}}}
	}
	a.bkState.Guard = &model.RestoreGuard{At: now.UTC().Format(time.RFC3339),
		BackupAt: now.Add(-3 * time.Hour).UTC().Format(time.RFC3339), Synced: true}
	fm.starTx = []models.StarTransaction{tx("st-before", now.Add(-5*time.Hour)), tx("st-window", now.Add(-time.Hour)),
		tx("st-edge", now.Add(-3*time.Hour-30*time.Minute))}
	a.reconcileStars(ctx)
	all := fm.joined()
	if !strings.Contains(all, "st-window") || !strings.Contains(all, "st-edge") || strings.Contains(all, "st-before") {
		t.Fatalf("окно звёзд: %q", all)
	}

	b, fm2, st2 := bkTestApp(t)
	_ = st2.UpsertUser(ctx, 1000000002)
	b.botCfg.Stars.Enabled = true
	long := now.Add(-61 * 24 * time.Hour)
	b.bkState.Guard = &model.RestoreGuard{At: long.UTC().Format(time.RFC3339), Synced: true}
	fm2.starTx = []models.StarTransaction{tx("st-old", long.Add(-time.Hour))}
	b.reconcileStars(ctx)
	if strings.Contains(fm2.joined(), "st-old") {
		t.Fatal("через два месяца оплата из окна снова пришла админу")
	}
	if paid, _ := st2.PaymentByExtID(ctx, "st-old"); paid {
		t.Fatal("оплата из окна выдана автоматически")
	}
}

// Покупка посреди сверки сроков: её срок не затирается тем, что панель
// отдала до покупки.
func TestSyncKeepsPurchaseDuringSync(t *testing.T) {
	a, _, st := bkTestApp(t)
	ctx := context.Background()
	_ = st.UpsertUser(ctx, 1000000001)
	_ = st.SetSubExpiry(ctx, 1000000001, "2026-12-01T00:00:00Z", "paid")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Человек купил, пока сверка читала панель.
		_ = st.AddPayment(ctx, &model.Payment{TelegramID: 1000000001, Method: model.PayMethodYooKassa, Months: 1,
			Amount: "150", Status: model.PaymentPaid, ExtID: "yk-during"})
		_ = st.SetSubExpiry(ctx, 1000000001, "2027-02-01T00:00:00Z", "paid")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"users":[{"uuid":"u1","username":"tg_1000000001","telegramId":1000000001,"expireAt":"2027-01-01T00:00:00Z"}],"total":1}}`))
	}))
	t.Cleanup(srv.Close)
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	changed, err := a.syncExpiryFromPanel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUser(ctx, 1000000001)
	if !sameMoment(u.SubExpireAt, "2027-02-01T00:00:00Z") || len(changed) != 0 {
		t.Fatalf("срок покупки затёрт: %q, changed=%v", u.SubExpireAt, changed)
	}
}

// «Проверить оплату» из мини-аппа: счёт до восстановления уходит админу сразу,
// а не ждёт фоновой сверки (и не попадает в список дважды).
func TestMiniPayCheckSendsReview(t *testing.T) {
	a, fm, _ := ykCheckFixture(t)
	st := a.store
	ctx := context.Background()
	at := time.Now().UTC()
	a.bkState.Guard = &model.RestoreGuard{At: at.Format(time.RFC3339), Synced: true}
	_ = st.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay_mini", TelegramID: 555, Months: 1,
		CreatedAt: at.Add(-time.Hour).Format(time.RFC3339)})
	a.MiniPayCheck(ctx, 555)
	if !strings.Contains(fm.joined(), "pay_mini") {
		t.Fatal("список на проверку из мини-аппа не отправлен")
	}
	a.reconcileOnce(ctx)
	if n := strings.Count(fm.joined(), "pay_mini"); n != 1 {
		t.Fatalf("счёт в списке %d раз", n)
	}
	if paid, _ := st.PaymentByExtID(ctx, "pay_mini"); paid {
		t.Fatal("оплата до восстановления выдана")
	}
}

// Для вебхуков удержанная оплата — «уже обработано» (повтора не будет), для
// человека — понятный текст.
func TestRestoreHeldError(t *testing.T) {
	a, _, _ := bkTestApp(t)
	err := a.restoreHeldErr(555)
	if !errors.Is(err, storage.ErrDuplicateExtID) {
		t.Fatal("вебхук принял бы удержанную оплату за ошибку и повторял бы её")
	}
	if got := a.clientErr(context.Background(), 555, "тест", err); !strings.Contains(got, "проверит администратор") {
		t.Fatalf("текст человеку: %q", got)
	}
}
