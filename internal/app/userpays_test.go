package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func seedUserPays(t *testing.T, fs *fakeStore, uid int64) {
	t.Helper()
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
		"🎁 15.11.26 · Премиум · 3 мес · выдано",
		"✅ 01.11.26 · Летний <old> · 1 мес · 199 ₽",
		"💳 25.10.26 · Пополнение баланса · 500 ₽",
		"✅ 01.10.26 · Базовый · 1 мес · с баланса",
		"↩️ 01.09.26 · Базовый · 1 мес · 199 ₽",
		"✅ 01.08.26 · 1 мес · 199 ₽",
		"🆓 14.07.26 · Пробный период",
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("нет строки %q среди:\n%s", want, all)
		}
	}
	if strings.Index(all, "15.11.26") > strings.Index(all, "14.07.26") {
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

// Моноширинная таблица общей истории: способы — текстом, без эмодзи, иначе
// колонки съезжают.
func TestPaymentsTableNoEmojiMethods(t *testing.T) {
	_, srv := newGrantPanel(t, "2030-01-01T00:00:00Z")
	a, fm, fs := grantApp(t, srv.URL)
	ctx := context.Background()
	seedUserPays(t, fs, grantUser)

	a.handleCallback(ctx, cb(100, "menu:payments"))
	table := fm.last()
	if i := strings.Index(table, "<pre>"); i >= 0 {
		table = table[i:]
	}
	if strings.Contains(table, "⭐") || strings.Contains(table, "🎁") {
		t.Fatalf("эмодзи в таблице:\n%s", table)
	}
	if !strings.Contains(table, "stars") || !strings.Contains(table, "grant") {
		t.Fatalf("способы не подписаны:\n%s", table)
	}
}
