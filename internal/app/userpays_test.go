package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

func seedUserPays(t *testing.T, fs *fakeStore, uid int64) {
	t.Helper()
	// Записи 2026 года: год в подписи не пишется, только пока он текущий.
	payNow = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { payNow = time.Now })
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, uid)
	for _, p := range []*model.Payment{
		{ID: 11, TelegramID: uid, Method: model.PayMethodP2P, Months: 1, Amount: "199 ₽", Status: model.PaymentPaid,
			CreatedAt: "2026-08-01T06:15:00Z", ExtID: "p2p:7"},
		{ID: 12, TelegramID: uid, Method: model.PayMethodStars, Months: 1, Amount: "199 ₽", Status: model.PaymentRefunded,
			CreatedAt: "2026-09-01T06:15:00Z", ExtID: "st_1", Snapshot: &model.PlanSnapshot{Code: "base", Months: 1}},
		{ID: 13, TelegramID: uid, Method: model.PayMethodBalance, Months: 1, Amount: "199 ₽", Status: model.PaymentPaid,
			CreatedAt: "2026-10-01T06:15:00Z", ExtID: "bal:1", Snapshot: &model.PlanSnapshot{Code: "base", Months: 1}},
		{ID: 14, TelegramID: uid, Method: model.PayMethodCryptoBot, Months: 0, Amount: "500 ₽", Status: model.PaymentPaid,
			CreatedAt: "2026-10-25T06:15:00Z", ExtID: "cb:1"},
		{ID: 15, TelegramID: uid, Method: model.PayMethodYooKassa, Months: 1, Amount: "199 ₽", Status: model.PaymentPaid,
			CreatedAt: "2026-11-01T09:40:00Z", ExtID: "2f9a6c1e-000f-5000-8000-1a2b3c4d5e6f",
			Snapshot: &model.PlanSnapshot{Code: "gone", Name: "Летний <old>", Months: 1, TrafficGB: 100, DeviceLimit: 3,
				IntSquads: []string{"sq-vip"}, AddSub: boolPtr(false), SwitchDays: 9}},
		{ID: 16, TelegramID: uid, Method: model.PayMethodGrant, Months: 3, Amount: "—", Status: model.PaymentPaid,
			CreatedAt: "2026-11-15T11:32:00Z", ExtID: "grant:555:ab", Snapshot: &model.PlanSnapshot{Code: "vip", Months: 3}},
		{ID: 17, TelegramID: uid, Method: model.PayMethodTrial, Months: 0, Amount: "—", Status: model.PaymentPaid,
			CreatedAt: "2026-07-14T06:15:00Z"},
	} {
		if err := fs.AddPayment(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
}

// Список платежей человека: каждая запись — кнопка с датой, тарифом, сроком и
// суммой; итог без выдач, триала и оплаты с баланса.
func TestUserPayments_List(t *testing.T) {
	_, srv := newGrantPanel(t, "2030-01-01T00:00:00Z")
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	seedUserPays(t, fs, grantUser)

	a.handleCallback(ctx, cb(100, "usr:view:555"))
	if !strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:pays:555:0") {
		t.Fatal("в карточке нет кнопки платежей")
	}
	a.handleCallback(ctx, cb(100, "usr:pays:555:0"))
	text := fm.last()
	for _, want := range []string{"Всего: 7", "Оплачено: 898 ₽", "Возвращено: 199 ₽", "Оплата с баланса в сумму не входит"} {
		if !strings.Contains(text, want) {
			t.Fatalf("в списке нет %q:\n%s", want, text)
		}
	}
	var labels []string
	for _, row := range fm.lastKB {
		for _, b := range row {
			labels = append(labels, b.Text)
		}
	}
	all := strings.Join(labels, "\n")
	for _, want := range []string{
		"🎁 15.11 · Премиум 3 мес · выдано",
		"✅ 01.11 · Летний <o… · ЮKassa 199 ₽",
		"💳 25.10 · Пополнение · CryptoBot 500 ₽",
		"✅ 01.10 · Базовый 1 мес · с баланса",
		"↩️ 01.09 · Базовый 1 мес · Stars 199 ₽",
		"✅ 01.08 · 1 мес · P2P 199 ₽",
		"🆓 14.07 · Пробный период",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("нет строки %q среди:\n%s", want, all)
		}
	}
	if strings.Index(all, "15.11") > strings.Index(all, "14.07") {
		t.Fatalf("новые не сверху:\n%s", all)
	}
}

// Карточка платежа: сумма с тарифом и условиями сделки; имена экранированы.
func TestUserPayments_Card(t *testing.T) {
	_, srv := newGrantPanel(t, "2030-01-01T00:00:00Z")
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	seedUserPays(t, fs, grantUser)

	a.handleCallback(ctx, cb(100, "usr:pay:15:0"))
	card := fm.last()
	for _, want := range []string{"Платёж 01.11.2026 12:40 МСК", "Способ: ЮKassa", "Сумма: <b>199 ₽</b>",
		"Тариф: <b>Летний &lt;old&gt;</b>", "Трафик: 100 ГБ", "Устройств: до 3", "Сквады: Fast &lt;EU&gt;",
		"Доп-подписка: нет", "+9 дн.", "<code>2f9a6c1e-000f-5000-8000-1a2b3c4d5e6f</code>"} {
		if !strings.Contains(card, want) {
			t.Fatalf("в карточке нет %q:\n%s", want, card)
		}
	}
	if !strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:pays:555:0") {
		t.Fatal("из карточки платежа нет пути обратно в список")
	}

	a.handleCallback(ctx, cb(100, "usr:pay:11:0"))
	if !strings.Contains(fm.last(), "Тариф не записан") {
		t.Fatalf("старая покупка без снимка: %s", fm.last())
	}
	a.handleCallback(ctx, cb(100, "usr:pay:16:0"))
	if !strings.Contains(fm.last(), "Выдача 15.11.2026") || !strings.Contains(fm.last(), "Выдано администратором") {
		t.Fatalf("выдача в истории: %s", fm.last())
	}
}

// Без платежей кнопки в карточке нет; поиск из общей истории открывает
// платежи найденного.
func TestUserPayments_EmptyAndSearch(t *testing.T) {
	_, srv := newGrantPanel(t, "2030-01-01T00:00:00Z")
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "usr:view:555"))
	if strings.Contains(strings.Join(fm.allCallbackData(), " "), "usr:pays:") {
		t.Fatal("кнопка платежей без платежей")
	}
	seedUserPays(t, fs, grantUser)
	a.handleCallback(ctx, cb(100, "pay:user"))
	a.handleMessage(ctx, msgText(100, "555"))
	if !strings.Contains(fm.last(), "Всего: 7") {
		t.Fatalf("поиск по ID не открыл платежи: %s", fm.last())
	}
}

func boolPtr(v bool) *bool { return &v }

// Покупка с баланса не складывается в общую выручку дважды: деньги уже
// посчитаны пополнением.
func TestRevenueSkipsBalancePurchases(t *testing.T) {
	_, srv := newGrantPanel(t, "2030-01-01T00:00:00Z")
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	seedUserPays(t, fs, grantUser)

	a.handleCallback(ctx, cb(100, "menu:payments"))
	if !strings.Contains(fm.last(), "Оплачено: 898 ₽") {
		t.Fatalf("итог истории платежей: %s", fm.last())
	}
	a.handleCallback(ctx, cb(100, "menu:analytics"))
	if !strings.Contains(fm.last(), "всего: 898 ₽") {
		t.Fatalf("выручка в аналитике: %s", fm.last())
	}
}

// Общая история платежей: платёж — кнопка с тем, кто платил, тарифом, сроком
// и суммой; каждая подпись влезает в телефон, даже с длинной почтой и именем
// тарифа. Экран закрыт от пересылки, карточка ведёт обратно в историю.
func TestPaymentsHistoryButtons(t *testing.T) {
	_, srv := newGrantPanel(t, "2030-01-01T00:00:00Z")
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	seedUserPays(t, fs, grantUser)
	const mailAcc int64 = -1000000071
	_ = fs.UpsertUser(ctx, mailAcc)
	_ = fs.CreateWebUser(ctx, &model.WebUser{TgID: mailAcc, Email: "very.long.address.for.testing@subdomain.example.com"})
	_ = fs.AddPayment(ctx, &model.Payment{ID: 18, TelegramID: mailAcc, Method: model.PayMethodYooKassa, Months: 12, Amount: "12 990 ₽",
		Status: model.PaymentPaid, CreatedAt: "2026-11-15T12:00:00Z", ExtID: "yk-long",
		Snapshot: &model.PlanSnapshot{Code: "longplan", Name: "Очень длинное название тарифа для семьи", Months: 12}})

	a.handleCallback(ctx, cb(100, "menu:payments"))
	if strings.Contains(fm.last(), "<pre>") {
		t.Fatalf("в истории осталась таблица:\n%s", fm.last())
	}
	if !fm.isProt(fm.seq) {
		t.Fatal("история платежей ушла без запрета пересылки")
	}
	fm.mu.Lock()
	rows := fm.lastKB
	fm.mu.Unlock()
	var mail string
	for _, r := range rows {
		if !rowFits(r) {
			t.Errorf("ряд не влезает в телефон: %v", kbLabels([][]models.InlineKeyboardButton{r}))
		}
		if strings.Contains(r[0].Text, "very.long") {
			mail = r[0].Text
		}
	}
	if !strings.Contains(mail, "…") || !strings.Contains(mail, "12 990 ₽") {
		t.Fatalf("длинная строка не укорочена или потеряла срок и сумму: %q", mail)
	}
	if !hasCB(fm.allCallbackData(), "pay:card:18:0") {
		t.Fatal("платёж не открывается")
	}
	a.handleCallback(ctx, cb(100, "pay:card:18:0"))
	cbs := fm.allCallbackData()
	if !strings.Contains(fm.last(), "yk-long") || !hasCB(cbs, "pay:page:0") || !hasCB(cbs, "usr:pays:-1000000071:0") {
		t.Fatalf("карточка платежа из истории: %s\n%v", fm.last(), cbs)
	}
	fm.mu.Lock()
	card := fm.lastKB
	fm.mu.Unlock()
	for _, r := range card {
		if !rowFits(r) {
			t.Errorf("ряд карточки не влезает в телефон: %v", kbLabels([][]models.InlineKeyboardButton{r}))
		}
	}
}
