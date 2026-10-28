package storage

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

// Журнал по человеку с id больше 2^31 (обычный для Telegram) читается на
// обеих базах; без человека — только по номеру платежа.
func TestPayLogsByLargeTelegramID(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		const big int64 = 5000000001
		_ = st.AddPayLog(ctx, &model.PayLogEntry{ExtID: "pl-big-1", TelegramID: big, Method: "m", Stage: "s", Detail: "d"})
		_ = st.AddPayLog(ctx, &model.PayLogEntry{ExtID: "pl-big-2", TelegramID: 1000000002, Method: "m", Stage: "s", Detail: "d"})
		got, err := st.PayLogs(ctx, "", big, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].TelegramID != big {
			t.Fatalf("журнал по человеку: %+v", got)
		}
		got, err = st.PayLogs(ctx, "pl-big-2", 0, 10)
		if err != nil || len(got) != 1 || got[0].ExtID != "pl-big-2" {
			t.Fatalf("журнал по номеру: %v %+v", err, got)
		}
	})
}
