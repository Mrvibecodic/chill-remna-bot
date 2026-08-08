package storage

import (
	"context"
	"database/sql"
	"errors"

	"remnabot/internal/model"
)

// intentCols — единственный список колонок намерения покупки (см. пояснение к
// planCols).
const intentCols = "telegram_id, plan_code, months, days, plan_snapshot, created_at"

// SetPurchaseIntent запоминает выбор пользователя на экране «выбор срока».
// Намерение одно на пользователя: новый выбор вытесняет предыдущий.
func (b *base) SetPurchaseIntent(ctx context.Context, in *model.PurchaseIntent) error {
	if in == nil {
		return errors.New("storage: пустое намерение покупки")
	}
	if in.CreatedAt == "" {
		in.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO purchase_intents ("+intentCols+") VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET plan_code = excluded.plan_code, "+
			"months = excluded.months, days = excluded.days, "+
			"plan_snapshot = excluded.plan_snapshot, created_at = excluded.created_at",
		in.TelegramID, in.PlanCode, in.Months, in.Days, in.Snapshot.Encode(), in.CreatedAt)
	return err
}

// PurchaseIntent возвращает последний выбор пользователя; nil, nil — выбора нет.
func (b *base) PurchaseIntent(ctx context.Context, telegramID int64) (*model.PurchaseIntent, error) {
	var in model.PurchaseIntent
	var snapRaw string
	err := b.db.QueryRowContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
		"SELECT "+intentCols+" FROM purchase_intents WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&in.TelegramID, &in.PlanCode, &in.Months, &in.Days, &snapRaw, &in.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	in.Snapshot = model.DecodePlanSnapshot(snapRaw)
	return &in, nil
}

// DeletePurchaseIntent убирает выбор пользователя.
func (b *base) DeletePurchaseIntent(ctx context.Context, telegramID int64) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, err := b.db.ExecContext(ctx, "DELETE FROM purchase_intents WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}
