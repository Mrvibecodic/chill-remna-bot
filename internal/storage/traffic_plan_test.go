package storage

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

func TestTrafficPlanRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		p := &model.Plan{
			Code: "trf150", Name: "Трафик 150", Enabled: true, TrafficGB: 150, DeviceLimit: 3,
			Strategy: "MONTH", Currency: "₽", Kind: model.PlanKindTraffic,
			// Сроки у трафик-тарифа не хранятся никогда.
			Durations: []model.PlanDuration{{Months: 1, Base: "100"}},
			Traffic: &model.PlanTraffic{
				Price: " 450 ", Stars: 300, AddSubStrategy: "DAY", RefGB: 10, RefTo: model.RefToBoth,
				Packs: []model.PlanPack{
					{ID: "abc123", GB: 50, Price: "160", Stars: 110},
					{ID: "abc123", GB: 10, Price: "1"}, // дубль ID — выброшен
					{ID: "zzz999", GB: 0, Price: "1"},  // пустой объём — выброшен
					{ID: "BAD", GB: 5, Price: "1"},     // кривой ID — выброшен
					{ID: "qwe456", GB: 100, Price: "290"},
				},
			},
		}
		if err := st.SavePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetPlan(ctx, "trf150")
		if err != nil || got == nil {
			t.Fatalf("GetPlan: %+v %v", got, err)
		}
		if !got.IsTraffic() || got.Strategy != "NO_RESET" || len(got.Durations) != 0 {
			t.Fatalf("инварианты трафик-тарифа не соблюдены: %+v", got)
		}
		tr := got.Traffic
		if tr == nil || tr.Price != "450" || tr.Stars != 300 || tr.AddSubStrategy != "DAY" || tr.RefGB != 10 || tr.RefTo != model.RefToBoth ||
			len(tr.Packs) != 2 || tr.Packs[0].ID != "abc123" || tr.Packs[1].GB != 100 {
			t.Fatalf("условия трафика искажены: %+v", tr)
		}
		if got.Pack("qwe456") == nil || got.Pack("nope00") != nil {
			t.Fatal("поиск пакета по ID")
		}

		// Предыдущая версия бота сохраняет тариф своим набором колонок: вид и
		// условия трафика обязаны пережить её правку.
		b := baseOf(t, st)
		if _, err := b.db.ExecContext(ctx,
			"INSERT INTO plans (code, name, description, icon, sort_order, enabled, traffic_gb, device_limit, "+
				"strategy, int_squads, ext_squad, availability, addsub, addsub_name, addsub_desc, currency, durations, from_config, created_at, updated_at, "+
				"addsub_traffic_gb, addsub_squads) VALUES ("+
				b.ph(1)+", 'Переименован', '', '', 0, 1, 150, 3, 'MONTH', '', '', 'all', '', '', '', '₽', "+
				"'[{\"months\":1,\"base\":\"99\"}]', 0, '', '', -1, '') "+
				"ON CONFLICT (code) DO UPDATE SET name = excluded.name, durations = excluded.durations, strategy = excluded.strategy",
			"trf150"); err != nil {
			t.Fatal(err)
		}
		got, _ = st.GetPlan(ctx, "trf150")
		if got == nil || got.Name != "Переименован" || !got.IsTraffic() || got.Traffic == nil || len(got.Traffic.Packs) != 2 {
			t.Fatalf("правка старой версией стёрла трафик-тариф: %+v", got)
		}
		if len(got.Durations) != 0 {
			t.Fatalf("сроки, заведённые старой версией, обязаны игнорироваться: %+v", got.Durations)
		}

		// Срочный тариф: вида и условий трафика нет.
		if err := st.SavePlan(ctx, &model.Plan{Code: "plain1", Name: "Срочный", Durations: []model.PlanDuration{{Months: 1, Base: "100"}}}); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetPlan(ctx, "plain1"); got == nil || got.Kind != "" || got.Traffic != nil || len(got.Durations) != 1 {
			t.Fatalf("срочный тариф искажён: %+v", got)
		}
	})
}

func TestTrafficInvoices(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		snap := &model.PlanSnapshot{Code: "trf150", Kind: model.PlanKindTraffic, PackID: "abc123", PackGB: 50, Price: "160", Currency: "₽"}
		inv := &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay-trf-1", TelegramID: 1000000001, Purpose: "pack", Snapshot: snap}
		if err := st.AddTrafficInvoice(ctx, inv); err != nil {
			t.Fatal(err)
		}
		got, err := st.TrafficInvoiceByExtID(ctx, "pay-trf-1")
		if err != nil || got == nil || got.Purpose != "pack" || got.Snapshot == nil || got.Snapshot.PackGB != 50 || got.Resolved {
			t.Fatalf("счёт искажён: %+v %v", got, err)
		}
		// Старая очередь его не видит.
		if p, _ := st.PendingByExtID(ctx, "pay-trf-1"); p != nil {
			t.Fatal("счёт трафика попал в очередь срочных счетов")
		}
		list, _ := st.ListUnresolvedTraffic(ctx, "9999-01-01T00:00:00Z", 10)
		if len(list) != 1 {
			t.Fatalf("в очереди %d счетов", len(list))
		}
		if err := st.ResolveTraffic(ctx, got.ID); err != nil {
			t.Fatal(err)
		}
		if list, _ := st.ListUnresolvedTraffic(ctx, "9999-01-01T00:00:00Z", 10); len(list) != 0 {
			t.Fatal("закрытый счёт остался в очереди")
		}
		ex, err := st.Export(ctx)
		if err != nil || len(ex.TrafficInvoices) != 1 {
			t.Fatalf("выгрузка без счетов трафика: %v", err)
		}

		// Намерение: выбранный пакет.
		if err := st.SetPurchaseIntent(ctx, &model.PurchaseIntent{TelegramID: 1000000001, PlanCode: "trf150", PackID: "abc123"}); err != nil {
			t.Fatal(err)
		}
		if in, _ := st.PurchaseIntent(ctx, 1000000001); in == nil || in.PackID != "abc123" || in.Months != 0 {
			t.Fatalf("намерение с пакетом: %+v", in)
		}
	})
}

// «Популярный срок» не считает трафик-тарифы: месяц в их строке — только
// метка покупки.
func TestMostPopularPlanSkipsTraffic(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		trafficSnap := &model.PlanSnapshot{Code: "trf150", Kind: model.PlanKindTraffic, TrafficGB: 150, Months: 1}
		for i, p := range []*model.Payment{
			{TelegramID: 1000000001, Method: model.PayMethodBalance, Months: 1, Amount: "450 ₽", Status: model.PaymentPaid, ExtID: "t-1", Snapshot: trafficSnap},
			{TelegramID: 1000000002, Method: model.PayMethodBalance, Months: 1, Amount: "450 ₽", Status: model.PaymentPaid, ExtID: "t-2", Snapshot: trafficSnap},
			{TelegramID: 1000000003, Method: model.PayMethodBalance, Months: 3, Amount: "549 ₽", Status: model.PaymentPaid, ExtID: "t-3",
				Snapshot: &model.PlanSnapshot{Code: "term01", Months: 3}},
		} {
			if err := st.AddPayment(ctx, p); err != nil {
				t.Fatalf("платёж %d: %v", i, err)
			}
		}
		months, total, err := st.MostPopularPlan(ctx)
		if err != nil || months != 3 || total != 1 {
			t.Fatalf("популярный срок: %d из %d (%v)", months, total, err)
		}
	})
}

// Счета трафика одного человека для «Проверить оплату»: только незакрытые,
// только его и не старше since.
func TestListUserTraffic(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		snap := &model.PlanSnapshot{Code: "trf150", Kind: model.PlanKindTraffic, TrafficGB: 150}
		add := func(ext string, tg int64, created string) *model.PendingInvoice {
			p := &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: ext, TelegramID: tg, Purpose: "traffic", Snapshot: snap, CreatedAt: created}
			if err := st.AddTrafficInvoice(ctx, p); err != nil {
				t.Fatal(err)
			}
			return p
		}
		add("u-old", 1000000001, "2026-01-01T00:00:00Z")
		done := add("u-done", 1000000001, "2026-11-18T10:00:00Z")
		add("u-new", 1000000001, "2026-11-18T11:00:00Z")
		add("other", 1000000002, "2026-11-18T11:00:00Z")
		_ = st.ResolveTraffic(ctx, done.ID)
		got, err := st.ListUserTraffic(ctx, 1000000001, "2026-11-01T00:00:00Z", 10)
		if err != nil || len(got) != 1 || got[0].ExtID != "u-new" || !got[0].Snapshot.IsTraffic() {
			t.Fatalf("счета человека: %+v (%v)", got, err)
		}
	})
}
