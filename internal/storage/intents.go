package storage

import (
	"context"
	"database/sql"
	"errors"

	"remnabot/internal/model"
)

// intentCols — единственный список колонок намерения покупки (см. пояснение к
// planCols).
const intentCols = "telegram_id, plan_code, months, days, created_at"

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
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET plan_code = excluded.plan_code, "+
			"months = excluded.months, days = excluded.days, created_at = excluded.created_at",
		in.TelegramID, in.PlanCode, in.Months, in.Days, in.CreatedAt)
	return err
}

// PurchaseIntent возвращает последний выбор пользователя; nil, nil — выбора нет.
func (b *base) PurchaseIntent(ctx context.Context, telegramID int64) (*model.PurchaseIntent, error) {
	var in model.PurchaseIntent
	err := b.db.QueryRowContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
		"SELECT "+intentCols+" FROM purchase_intents WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&in.TelegramID, &in.PlanCode, &in.Months, &in.Days, &in.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// DeletePurchaseIntent убирает выбор пользователя.
func (b *base) DeletePurchaseIntent(ctx context.Context, telegramID int64) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, err := b.db.ExecContext(ctx, "DELETE FROM purchase_intents WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

// DeletePurchaseIntentFor убирает выбор, только если он всё ещё на этот срок.
// Условие в самом запросе, а не проверкой перед удалением: между чтением и
// удалением человек успевает выбрать другой срок, и стирать этот свежий выбор
// нельзя.
func (b *base) DeletePurchaseIntentFor(ctx context.Context, telegramID int64, months int) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"DELETE FROM purchase_intents WHERE telegram_id = "+b.ph(1)+" AND months = "+b.ph(2),
		telegramID, months)
	return err
}

// invoiceSnapCols — колонки условий выставленного счёта.
const invoiceSnapCols = "telegram_id, method, months, plan_snapshot, created_at"

// SetInvoiceSnapshot запоминает условия сделки по выставленному счёту, у
// которого нет строки в очереди незакрытых счетов (Stars).
func (b *base) SetInvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int, snap *model.PlanSnapshot) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO invoice_snapshots ("+invoiceSnapCols+") VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+") "+
			"ON CONFLICT (telegram_id, method, months) DO UPDATE SET "+
			"plan_snapshot = excluded.plan_snapshot, created_at = excluded.created_at",
		telegramID, method, months, snap.Encode(), nowStr())
	return err
}

// InvoiceSnapshot возвращает условия выставленного счёта; nil — их нет.
func (b *base) InvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int) (*model.PlanSnapshot, error) {
	var raw string
	err := b.db.QueryRowContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"SELECT plan_snapshot FROM invoice_snapshots WHERE telegram_id = "+b.ph(1)+
			" AND method = "+b.ph(2)+" AND months = "+b.ph(3),
		telegramID, method, months).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return model.DecodePlanSnapshot(raw), nil
}

// DeleteInvoiceSnapshot убирает условия счёта после того, как он оплачен.
func (b *base) DeleteInvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"DELETE FROM invoice_snapshots WHERE telegram_id = "+b.ph(1)+
			" AND method = "+b.ph(2)+" AND months = "+b.ph(3),
		telegramID, method, months)
	return err
}
