package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// repairPanel отдаёт пользователя с заданными лимитами и запоминает апдейты.
func repairPanel(t *testing.T, devices int, traffic int64, patches *[]map[string]any) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			mu.Lock()
			devices, traffic := devices, traffic
			mu.Unlock()
			resp := map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555",
				"subscriptionUrl": "https://sub/x", "expireAt": "2099-01-01T00:00:00Z",
				"hwidDeviceLimit": devices, "trafficLimitBytes": traffic,
			}}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/api/users") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			*patches = append(*patches, body)
			// Панель запоминает применённое: без этого повторный проход
			// сверки видел бы прежние лимиты и «чинил» их бесконечно, а тест
			// не проверял бы сходимость.
			if v, ok := body["hwidDeviceLimit"].(float64); ok {
				devices = int(v)
			}
			if v, ok := body["trafficLimitBytes"].(float64); ok {
				traffic = int64(v)
			}
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2099-01-01T00:00:00Z"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func repairFixture(t *testing.T, devices int, traffic int64) (*App, *fakeStore, *[]map[string]any) {
	t.Helper()
	patches := &[]map[string]any{}
	srv := repairPanel(t, devices, traffic, patches)
	a, fs := snapApp(t, srv.URL)
	return a, fs, patches
}

const repairGB = int64(50) * 1024 * 1024 * 1024

func seedRepairUser(t *testing.T, fs *fakeStore, expire string, paymentSnap *model.PlanSnapshot) {
	t.Helper()
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.SetSubExpiry(ctx, 555, expire, "paid")
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{
		Months: 1, DeviceLimit: 5, TrafficGB: 50, Strategy: "MONTH", IntSquads: []string{"squad-sold"},
	})
	_ = fs.AddPayment(ctx, &model.Payment{
		TelegramID: 555, Method: model.PayMethodYooKassa, Months: 1, Amount: "150",
		Status: model.PaymentPaid, ExtID: "yk_r1", Snapshot: paymentSnap,
	})
}

// Человеку выдали меньше, чем продали, — сверка обязана вернуть проданное.
func TestSubRepair_RestoresUnderdeliveredLimits(t *testing.T) {
	a, fs, patches := repairFixture(t, 3, repairGB)
	sold := &model.PlanSnapshot{Months: 1, DeviceLimit: 5, TrafficGB: 50}
	seedRepairUser(t, fs, time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339), sold)

	st := a.repairSubscriptions(context.Background())
	if st.checked != 1 || st.fixed != 1 {
		t.Fatalf("сверка: checked=%d fixed=%d (ожидалось 1/1)", st.checked, st.fixed)
	}
	if len(*patches) != 1 {
		t.Fatalf("ожидался один апдейт панели, got %d", len(*patches))
	}
	if got := (*patches)[0]["hwidDeviceLimit"]; got != float64(5) {
		t.Fatalf("лимит устройств не восстановлен: %v", got)
	}
}

// Если в панели дали БОЛЬШЕ проданного — это осознанное решение админа, и
// фоновая сверка не имеет права его отбирать.
func TestSubRepair_NeverTakesAway(t *testing.T) {
	a, fs, patches := repairFixture(t, 10, repairGB*4)
	sold := &model.PlanSnapshot{Months: 1, DeviceLimit: 5, TrafficGB: 50}
	seedRepairUser(t, fs, time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339), sold)

	st := a.repairSubscriptions(context.Background())
	if st.fixed != 0 || len(*patches) != 0 {
		t.Fatalf("сверка урезала выданное сверх проданного: fixed=%d patches=%d", st.fixed, len(*patches))
	}
}

// Последнюю покупку провёл образ бота без снимков — то есть предыдущий, во
// время отката. Условия применялись из тогдашнего конфига, а что продали на
// самом деле, знает счёт: его выставлял уже новый образ.
func TestSubRepair_FullReapplyAfterRollback(t *testing.T) {
	a, fs, patches := repairFixture(t, 3, repairGB)
	ctx := context.Background()
	seedRepairUser(t, fs, time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339), nil)
	// Счёт, по которому платили во время отката: условия ДРУГИЕ и щедрее, чем
	// в снимке пользователя от прошлой покупки.
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{
		ID: 9001, Method: model.PayMethodYooKassa, ExtID: "yk_r1", TelegramID: 555, Months: 12,
		Snapshot: &model.PlanSnapshot{Months: 12, DeviceLimit: 10, TrafficGB: 200, IntSquads: []string{"squad-new"}},
	})

	st := a.repairSubscriptions(ctx)
	if st.fixed != 1 || len(*patches) != 1 {
		t.Fatalf("полная переприменка не выполнена: fixed=%d patches=%d", st.fixed, len(*patches))
	}
	// Ключевое: чиним по условиям ТОЙ покупки, а не по прошлому снимку
	// пользователя (5 устройств, squad-sold) — иначе сверка отобрала бы
	// оплаченное.
	if got := (*patches)[0]["hwidDeviceLimit"]; got != float64(10) {
		t.Fatalf("применены условия не той сделки: %v", got)
	}
	sq, _ := (*patches)[0]["activeInternalSquads"].([]any)
	if len(sq) != 1 || sq[0] != "squad-new" {
		t.Fatalf("применены сквады не той сделки: %v", (*patches)[0]["activeInternalSquads"])
	}

	// Второй проход обязан промолчать: иначе сверка ходила бы по этой
	// подписке вечно и переприменяла условия каждые 12 часов.
	if st2 := a.repairSubscriptions(ctx); st2.fixed != 0 {
		t.Fatalf("повторный проход снова чинит уже починенное: fixed=%d", st2.fixed)
	}
}

// Покупка без снимка и без счёта (Stars, баланс, перевод, Tribute): что именно
// продали — неизвестно. Гадать нельзя ни в плюс, ни в минус.
func TestSubRepair_NoGuessWithoutInvoice(t *testing.T) {
	a, fs, patches := repairFixture(t, 1, repairGB/10)
	seedRepairUser(t, fs, time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339), nil)

	st := a.repairSubscriptions(context.Background())
	if st.fixed != 0 || len(*patches) != 0 {
		t.Fatalf("сверка применила условия, которых не знает: fixed=%d patches=%d", st.fixed, len(*patches))
	}
}

// Истёкшая подписка не чинится: лишний апдейт сдвинул бы срок.
func TestSubRepair_SkipsExpired(t *testing.T) {
	a, fs, patches := repairFixture(t, 1, repairGB)
	sold := &model.PlanSnapshot{Months: 1, DeviceLimit: 5, TrafficGB: 50}
	seedRepairUser(t, fs, time.Now().UTC().AddDate(0, 0, -3).Format(time.RFC3339), sold)

	st := a.repairSubscriptions(context.Background())
	if st.checked != 0 || len(*patches) != 0 {
		t.Fatalf("истёкшая подписка не должна трогаться: checked=%d patches=%d", st.checked, len(*patches))
	}
}
