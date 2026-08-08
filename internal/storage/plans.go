package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"remnabot/internal/model"
)

// planCols — единственный список колонок тарифа. Расхождение SELECT и Scan
// компилятор не ловит и тесты через подменённое хранилище тоже: запросы к
// настоящей базе идут только из этого файла. Отсюда одна константа на все
// запросы и round-trip тест против реальной БД.
const planCols = "code, name, description, icon, sort_order, enabled, traffic_gb, device_limit, " +
	"strategy, int_squads, ext_squad, availability, currency, durations, from_config, created_at, updated_at"

// ErrPlanCode — код тарифа не прошёл проверку.
var ErrPlanCode = errors.New("storage: недопустимый код тарифа")

// SavePlan создаёт или обновляет тариф. Код тарифа — первичный ключ и не
// меняется при переименовании.
func (b *base) SavePlan(ctx context.Context, p *model.Plan) error {
	if p == nil {
		return errors.New("storage: пустой тариф")
	}
	p.Normalize()
	if !model.ValidPlanCode(p.Code) {
		return fmt.Errorf("%w: %q", ErrPlanCode, p.Code)
	}
	now := nowStr()
	if p.CreatedAt == "" {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	_, err := b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO plans ("+planCols+") VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+
			b.ph(8)+", "+b.ph(9)+", "+b.ph(10)+", "+b.ph(11)+", "+b.ph(12)+", "+b.ph(13)+", "+b.ph(14)+", "+
			b.ph(15)+", "+b.ph(16)+", "+b.ph(17)+") "+
			"ON CONFLICT (code) DO UPDATE SET name = excluded.name, description = excluded.description, "+
			"icon = excluded.icon, sort_order = excluded.sort_order, enabled = excluded.enabled, "+
			"traffic_gb = excluded.traffic_gb, device_limit = excluded.device_limit, "+
			"strategy = excluded.strategy, int_squads = excluded.int_squads, ext_squad = excluded.ext_squad, "+
			"availability = excluded.availability, currency = excluded.currency, "+
			"durations = excluded.durations, from_config = excluded.from_config, "+
			"updated_at = excluded.updated_at",
		p.Code, p.Name, p.Description, p.Icon, p.Order, boolToInt(p.Enabled), p.TrafficGB, p.DeviceLimit,
		p.Strategy, model.EncodeStrings(p.IntSquads), p.ExtSquad, p.Availability, p.Currency,
		model.EncodeDurations(p.Durations), boolToInt(p.FromConfig), p.CreatedAt, p.UpdatedAt)
	return err
}

// GetPlan возвращает тариф по коду; nil, nil — тарифа нет.
func (b *base) GetPlan(ctx context.Context, code string) (*model.Plan, error) {
	row := b.db.QueryRowContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
		"SELECT "+planCols+" FROM plans WHERE code = "+b.ph(1), code)
	p, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListPlans возвращает все тарифы в порядке витрины.
func (b *base) ListPlans(ctx context.Context) ([]model.Plan, error) {
	rows, err := b.db.QueryContext(ctx, "SELECT "+planCols+" FROM plans ORDER BY sort_order, code")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// DeletePlan удаляет тариф. Проданные подписки от справочника не зависят: их
// условия зафиксированы снимком сделки, поэтому удаление тарифа никого не
// понижает.
func (b *base) DeletePlan(ctx context.Context, code string) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, err := b.db.ExecContext(ctx, "DELETE FROM plans WHERE code = "+b.ph(1), code)
	return err
}

// scanRow — общее у *sql.Row и *sql.Rows: один Scan на оба пути чтения, чтобы
// список колонок и аргументы Scan не разъехались между GetPlan и ListPlans.
type scanRow interface {
	Scan(dest ...any) error
}

func scanPlan(row scanRow) (*model.Plan, error) {
	var p model.Plan
	var enabled, fromConfig int
	var intSquads, durations string
	if err := row.Scan(&p.Code, &p.Name, &p.Description, &p.Icon, &p.Order, &enabled,
		&p.TrafficGB, &p.DeviceLimit, &p.Strategy, &intSquads, &p.ExtSquad, &p.Availability,
		&p.Currency, &durations, &fromConfig, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	p.FromConfig = fromConfig != 0
	p.IntSquads = model.DecodeStrings(intSquads)
	p.Durations = model.DecodeDurations(durations)
	return &p, nil
}
