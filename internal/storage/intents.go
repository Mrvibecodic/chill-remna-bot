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

// DeletePurchaseIntentFor убирает выбор, только если он всё ещё тот самый:
// и срок, и время записи. Условия в самом запросе, а не проверкой перед
// удалением: между чтением и удалением человек успевает нажать другую кнопку,
// и стирать этот свежий выбор нельзя.
func (b *base) DeletePurchaseIntentFor(ctx context.Context, telegramID int64, months int, createdAt string) error {
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"DELETE FROM purchase_intents WHERE telegram_id = "+b.ph(1)+" AND months = "+b.ph(2)+
			" AND created_at = "+b.ph(3),
		telegramID, months, createdAt)
	return err
}

// invoiceSnapCols — колонки условий выставленного счёта.
const invoiceSnapCols = "telegram_id, method, months, plan_snapshot, created_at"

// SetInvoiceSnapshot запоминает условия сделки по выставленному счёту, у
// которого нет строки в очереди незакрытых счетов (Stars).
func (b *base) SetInvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int, snap *model.PlanSnapshot) error {
	return b.setInvoiceSnapshotAt(ctx, telegramID, method, months, snap, nowStr())
}

// setInvoiceSnapshotAt — та же запись с явным временем: переезд базы обязан
// сохранять возраст строки, а не омолаживать брошенные счета.
func (b *base) setInvoiceSnapshotAt(ctx context.Context, telegramID int64, method string, months int, snap *model.PlanSnapshot, createdAt string) error {
	if createdAt == "" {
		createdAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO invoice_snapshots ("+invoiceSnapCols+") VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+") "+
			"ON CONFLICT (telegram_id, method, months) DO UPDATE SET "+
			"plan_snapshot = excluded.plan_snapshot, created_at = excluded.created_at",
		telegramID, method, months, snap.Encode(), createdAt)
	return err
}

// InvoiceSnapshot возвращает условия выставленного счёта и время его
// выставления; nil — условий нет.
func (b *base) InvoiceSnapshot(ctx context.Context, telegramID int64, method string, months int) (*model.PlanSnapshot, string, error) {
	var raw, createdAt string
	err := b.db.QueryRowContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"SELECT plan_snapshot, created_at FROM invoice_snapshots WHERE telegram_id = "+b.ph(1)+
			" AND method = "+b.ph(2)+" AND months = "+b.ph(3),
		telegramID, method, months).Scan(&raw, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return model.DecodePlanSnapshot(raw), createdAt, nil
}

// PurgeInvoiceSnapshots убирает условия счетов, которые давно никто не
// оплатит. Иначе таблица растёт вместе с числом когда-либо заходивших: строку
// на человека и срок никто не удаляет — при выдаче её оставляют намеренно
// (повторная доставка оплаты обязана применить те же условия).
func (b *base) PurgeInvoiceSnapshots(ctx context.Context, before string) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, err := b.db.ExecContext(ctx, "DELETE FROM invoice_snapshots WHERE created_at < "+b.ph(1), before)
	return err
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
