package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/platega"
)

func TestParsePlPayload_Mangled(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		tg      int64
		months  int
	}{
		{"обычный", "telegram_id=777&months=1", 777, 1},
		{"html-экранирование", "telegram_id=777&amp;months=1", 777, 1},
		{"точка с запятой", "telegram_id=777;months=1", 777, 1},
		{"процентное кодирование", "telegram_id%3D777%26months%3D1", 777, 1},
		{"хвост потерян", "telegram_id=777", 777, 0},
		{"чужая строка", "custom-payload", 0, 0},
		{"пусто", "", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tg, months := parsePlPayload(c.payload)
			if tg != c.tg || months != c.months {
				t.Fatalf("payload %q → tg=%d months=%d, ожидалось tg=%d months=%d", c.payload, tg, months, c.tg, c.months)
			}
		})
	}
}

func TestFinalizePlatega_LostMonthsTakenFromPendingInvoice(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	fm := &fakeMsg{}
	a.msg = fm
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		Method: model.PayMethodPlatega, ExtID: "tx-1", TelegramID: u, Months: 1,
	})

	a.finalizePlatega(ctx, "tx-1", &platega.Transaction{
		ID: "tx-1", Status: "CONFIRMED", Amount: 108, Currency: "RUB",
		Payload: "telegram_id=555",
	})

	if ok, _ := fs.HasPaidPayment(ctx, u); !ok {
		t.Fatalf("подписка не выдана, хотя срок лежал в строке счёта:\n%s", fm.joined())
	}
	if strings.Contains(fm.joined(), "срок подписки определить не удалось") {
		t.Fatalf("админа позвали зря:\n%s", fm.joined())
	}
}

func TestFinalizePlatega_NoPeriodAnywhere_AdminOnly(t *testing.T) {
	var patched map[string]any
	srv := snapPanel(t, &patched)
	a, fs := snapApp(t, srv.URL)
	fm := &fakeMsg{}
	a.msg = fm
	ctx := context.Background()
	const u int64 = 555
	_ = fs.UpsertUser(ctx, u)

	a.finalizePlatega(ctx, "tx-2", &platega.Transaction{
		ID: "tx-2", Status: "CONFIRMED", Amount: 108, Currency: "RUB",
		Payload: "telegram_id=555",
	})

	if ok, _ := fs.HasPaidPayment(ctx, u); ok {
		t.Fatal("подписка выдана по неизвестному сроку")
	}
	if patched != nil {
		t.Fatalf("панель трогать было нельзя: %+v", patched)
	}
	if len(fm.texts) != 1 {
		t.Fatalf("ожидалось ровно одно сообщение (админу), получено %d:\n%s", len(fm.texts), fm.joined())
	}
	if !strings.Contains(fm.joined(), "срок подписки определить не удалось") {
		t.Fatalf("админ не уведомлён:\n%s", fm.joined())
	}
	var seen bool
	for _, e := range fs.paylogs {
		if strings.Contains(e.Detail, `payload="telegram_id=555"`) {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("сырой payload не записан в журнал: %+v", fs.paylogs)
	}
}
