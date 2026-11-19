package app

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/heleket"
	"remnabot/internal/model"
	"remnabot/internal/platega"
	"remnabot/internal/yookassa"
)

// Каждый способ оплаты: счёт трафика из своей очереди проводится выдачей
// трафика. Без строки в очереди трафика (так его видит предыдущая версия бота
// после отката) метаданные платёжки ведут в «срок неизвестен» — ни выдачи
// месяцев, ни зачисления на баланс.

func trafficPack(t *testing.T, a *App, plan *model.Plan) *model.PlanSnapshot {
	t.Helper()
	buyTrafficWithBalance(t, a, plan.Code, "")
	return a.trafficSnapshotOf(plan, plan.Pack("pk5000"))
}

func balanceOf(t *testing.T, fs *fakeStore) int64 {
	t.Helper()
	u, _ := fs.GetUser(context.Background(), trafficUID)
	return u.Balance
}

func TestTrafficPay_YooKassa(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := trafficPack(t, a, plan)
	pay := &yookassa.Payment{ID: "yk-1", Status: "succeeded", Paid: true,
		Metadata: map[string]string{"telegram_id": "555", "months": "0", "plan": plan.Code}}
	pay.Amount.Value, pay.Amount.Currency = "160.00", "RUB"

	// Строки трафика нет — как у предыдущей версии: выдачи нет.
	if res, _ := a.settleYooKassa(ctx, pay, pay.ID); res != ykNoPeriod {
		t.Fatalf("платёж без строки трафика: %v", res)
	}
	if _, limit, _, _ := panel.state(); limit != 150*gib {
		t.Fatal("без строки счёта трафик выдан")
	}

	a.addTrafficInvoice(ctx, model.PayMethodYooKassa, "yk-2", trafficUID, 0, snap)
	pay.ID = "yk-2"
	if res, err := a.settleYooKassa(ctx, pay, pay.ID); res != ykIssued || err != nil {
		t.Fatalf("оплата пакета ЮKassa: %v %v", res, err)
	}
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет не добавлен: %d", limit)
	}
	if inv, _ := fs.TrafficInvoiceByExtID(ctx, "yk-2"); inv == nil || !inv.Resolved {
		t.Fatal("счёт трафика не закрыт")
	}
	if res, _ := a.settleYooKassa(ctx, pay, pay.ID); res != ykAlready {
		t.Fatalf("повтор вебхука: %v", res)
	}
}

func TestTrafficPay_CryptoBot(t *testing.T) {
	a, _, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := trafficPack(t, a, plan)
	payload := cbTrafficPayload(trafficUID)
	// Разбор payload прежней версией: не число во втором поле — не пополнение
	// и не месяцы.
	if _, _, err := parseCryptoBotPayload(payload); err == nil {
		t.Fatalf("payload трафика разобрался как срочный: %q", payload)
	}
	if res, _ := a.settleCryptoBot(ctx, "cb:1", payload, "2 USDT"); res != cbBadPayload {
		t.Fatalf("без строки трафика: %v", res)
	}
	a.addTrafficInvoice(ctx, model.PayMethodCryptoBot, "cb:2", trafficUID, 0, snap)
	if res, err := a.settleCryptoBot(ctx, "cb:2", payload, "2 USDT"); res != cbIssued || err != nil {
		t.Fatalf("оплата CryptoBot: %v %v", res, err)
	}
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет не добавлен: %d", limit)
	}
}

func TestTrafficPay_Heleket(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := trafficPack(t, a, plan)
	bal := balanceOf(t, fs)
	inv := &heleket.Invoice{UUID: "u-1", OrderID: "hlx-555-0-abcd", Status: "paid", Amount: "160.00", Currency: "RUB",
		PaymentAmount: "2", AdditionalData: "tg=555&mo=0"}
	if id, _, _ := parseHLOrderID(inv.OrderID); id != 0 {
		t.Fatal("order_id трафика опознан прежним разбором")
	}
	a.finalizeHeleket(ctx, inv)
	if _, limit, _, _ := panel.state(); limit != 150*gib || balanceOf(t, fs) != bal {
		t.Fatal("без строки трафика Heleket выдал или зачислил")
	}
	a.addTrafficInvoice(ctx, model.PayMethodHeleket, hlExtPrefix+"u-2", trafficUID, 0, snap)
	inv.UUID = "u-2"
	a.finalizeHeleket(ctx, inv)
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет Heleket не добавлен: %d", limit)
	}
}

func TestTrafficPay_Platega(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := trafficPack(t, a, plan)
	bal := balanceOf(t, fs)
	tx := &platega.Transaction{ID: "tx-1", Status: "CONFIRMED", Amount: 160, Currency: "RUB",
		Payload: "telegram_id=555&months=0&kind=pack"}
	if _, mo, top := parsePlPayloadAll(tx.Payload); mo != 0 || top != 0 {
		t.Fatal("payload трафика разобран как срок или пополнение")
	}
	a.finalizePlatega(ctx, tx.ID, tx)
	if _, limit, _, _ := panel.state(); limit != 150*gib || balanceOf(t, fs) != bal {
		t.Fatal("без строки трафика Platega выдала или зачислила")
	}
	a.addTrafficInvoice(ctx, model.PayMethodPlatega, "tx-2", trafficUID, 0, snap)
	tx.ID = "tx-2"
	a.finalizePlatega(ctx, tx.ID, tx)
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет Platega не добавлен: %d", limit)
	}
}

func TestTrafficPay_YooMoney(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := trafficPack(t, a, plan)
	if res := a.finalizeYooMoney(ctx, "ym-x", "op-0", 16000, 16000); res != ymNoInvoice {
		t.Fatalf("без строки счёта: %v", res)
	}
	_ = fs.AddTrafficInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooMoney, ExtID: "ym-1", TelegramID: trafficUID,
		Purpose: purposePack, Kopecks: 16000, Snapshot: snap})
	if res := a.finalizeYooMoney(ctx, "ym-1", "op-1", 15000, 15000); res != ymUnderpaid {
		t.Fatalf("недоплата: %v", res)
	}
	_ = fs.AddTrafficInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooMoney, ExtID: "ym-2", TelegramID: trafficUID,
		Purpose: purposePack, Kopecks: 16000, Snapshot: snap})
	if res := a.finalizeYooMoney(ctx, "ym-2", "op-2", 16000, 15840); res != ymIssued {
		t.Fatalf("оплата ЮMoney: %v", res)
	}
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет ЮMoney не добавлен: %d", limit)
	}
}

func TestTrafficPay_Stars(t *testing.T) {
	a, _, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	fm := a.msg.(*fakeMsg)
	a.botCfg.Stars.Enabled = true
	payload := trafficStarsPayload(plan.Code, "", trafficUID)
	if mo, _ := starsPayload(payload); mo != 0 {
		t.Fatal("payload трафика разобран как срок прежним разбором")
	}
	pre := func(payload string, total int) bool {
		fm.mu.Lock()
		fm.preOK = nil
		fm.mu.Unlock()
		a.handlePreCheckout(ctx, &models.PreCheckoutQuery{ID: "q", From: &models.User{ID: trafficUID}, InvoicePayload: payload, TotalAmount: total})
		fm.mu.Lock()
		defer fm.mu.Unlock()
		return len(fm.preOK) == 1 && fm.preOK[0]
	}
	if pre(payload, 299) {
		t.Fatal("предпроверка пропустила чужую цену")
	}
	if !pre(payload, 300) {
		t.Fatal("предпроверка отклонила верную цену тарифа")
	}
	packPayload := trafficStarsPayload(plan.Code, "pk5000", trafficUID)
	if pre(packPayload, 110) {
		t.Fatal("пакет за звёзды до покупки тарифа")
	}
	a.handleSuccessfulPayment(ctx, &models.Message{Chat: models.Chat{ID: trafficUID}, SuccessfulPayment: &models.SuccessfulPayment{
		InvoicePayload: payload, TotalAmount: 300, TelegramPaymentChargeID: "ch-1", Currency: "XTR"}})
	if exp, limit, _, _ := panel.state(); limit != 150*gib || !strings.HasPrefix(exp, "2099") {
		t.Fatalf("тариф за звёзды не выдан: %s %d", exp, limit)
	}
	if !pre(packPayload, 110) {
		t.Fatal("предпроверка отклонила пакет владельцу")
	}
	a.handleSuccessfulPayment(ctx, &models.Message{Chat: models.Chat{ID: trafficUID}, SuccessfulPayment: &models.SuccessfulPayment{
		InvoicePayload: packPayload, TotalAmount: 110, TelegramPaymentChargeID: "ch-2", Currency: "XTR"}})
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет за звёзды не добавлен: %d", limit)
	}
	// Повторная доставка — без второй прибавки.
	a.handleSuccessfulPayment(ctx, &models.Message{Chat: models.Chat{ID: trafficUID}, SuccessfulPayment: &models.SuccessfulPayment{
		InvoicePayload: packPayload, TotalAmount: 110, TelegramPaymentChargeID: "ch-2", Currency: "XTR"}})
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("повтор оплаты звёздами прибавил: %d", limit)
	}
}

func TestTrafficPay_P2P(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	a.botCfg.P2P = model.P2PConfig{Enabled: true, OpenForAll: true, Cards: []string{"0000 1111"}, Currency: "₽"}
	buyTrafficWithBalance(t, a, plan.Code, "")
	_ = a.setBuyIntentTraffic(ctx, trafficUID, plan.Code, "pk5000", "")
	s, _ := a.saleFor(ctx, trafficUID)
	_, price, reqID, err := a.prepareP2PCardSale(ctx, trafficUID, s)
	if err != nil || price != "160" {
		t.Fatalf("заявка на пакет: %v %s", err, price)
	}
	req, _ := fs.GetP2PRequest(ctx, reqID)
	if req.Months != 0 || req.Purpose != purposePack || req.IsTopUp() || !req.Snapshot.IsTraffic() {
		t.Fatalf("заявка на пакет: %+v", req)
	}
	// Другая покупка при открытой заявке на пакет — не подменяет её.
	_ = a.setBuyIntentTraffic(ctx, trafficUID, plan.Code, "", "")
	s2, _ := a.saleFor(ctx, trafficUID)
	if _, _, _, err := a.prepareP2PCardSale(ctx, trafficUID, s2); err == nil {
		t.Fatal("открытая заявка на пакет подставлена под покупку тарифа")
	}
	req.Status = model.P2PSubmitted
	_ = fs.UpdateP2PRequest(ctx, req)
	a.adminApprovePayment(ctx, 100, itoa64(reqID))
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет по переводу не добавлен: %d", limit)
	}
}

// Сверка проводит счета трафика из своей очереди и закрывает их там же.
func TestTrafficPay_Reconcile(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	snap := trafficPack(t, a, plan)
	a.addTrafficInvoice(ctx, model.PayMethodPlatega, "tx-r", trafficUID, 0, snap)
	inv, _ := fs.TrafficInvoiceByExtID(ctx, "tx-r")
	if !a.reconcileFinalize(ctx, trafficQueue{fs}, inv, "160.00 RUB") {
		t.Fatal("сверка не провела счёт трафика")
	}
	if _, limit, _, _ := panel.state(); limit != 200*gib {
		t.Fatalf("пакет сверкой не добавлен: %d", limit)
	}
	if inv, _ := fs.TrafficInvoiceByExtID(ctx, "tx-r"); !inv.Resolved {
		t.Fatal("сверка не закрыла счёт трафика")
	}
}
