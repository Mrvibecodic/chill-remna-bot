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
	// Список допущенных умирает вместе с тарифом: осиротевшие записи молча
	// ожили бы, если код когда-нибудь будет занят другим тарифом (импорт).
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, _ = b.db.ExecContext(ctx, "DELETE FROM plan_access WHERE plan_code = "+b.ph(1), code)
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, err := b.db.ExecContext(ctx, "DELETE FROM plans WHERE code = "+b.ph(1), code)
	return err
}

// planAccessCols — единственный список колонок списка допущенных, по той же
// причине, что и planCols: расхождение SELECT и Scan не ловится ничем, кроме
// round-trip теста против настоящей базы.
const planAccessCols = "plan_code, telegram_id, email, created_at"

// ErrPlanAccessEntry — запись списка допущенных не прошла проверку: в ней
// должен быть либо Telegram ID, либо e-mail, и ровно что-то одно.
var ErrPlanAccessEntry = errors.New("storage: недопустимая запись списка допущенных")

// normPlanAccess проверяет и канонизирует запись списка. Почта хранится в
// нижнем регистре — так же её пишет кабинет и так же её ищет HasPlanAccess.
func normPlanAccess(code string, tgID int64, email string) (int64, string, error) {
	email = model.NormalizeEmail(email)
	if !model.ValidPlanCode(code) {
		return 0, "", fmt.Errorf("%w: %q", ErrPlanCode, code)
	}
	if (tgID == 0) == (email == "") {
		return 0, "", ErrPlanAccessEntry
	}
	return tgID, email, nil
}

// GrantPlanAccess добавляет запись в список допущенных тарифа. Повторная
// выдача той же записи не ошибка: список правится и из карточки тарифа, и из
// карточки пользователя, и при импорте.
func (b *base) GrantPlanAccess(ctx context.Context, code string, tgID int64, email string) error {
	return b.grantPlanAccessAt(ctx, code, tgID, email, nowStr())
}

// grantPlanAccessAt — то же с явным временем добавления: перенос базы обязан
// сохранять исходный порядок списка.
func (b *base) grantPlanAccessAt(ctx context.Context, code string, tgID int64, email, createdAt string) error {
	tgID, email, err := normPlanAccess(code, tgID, email)
	if err != nil {
		return err
	}
	if createdAt == "" {
		createdAt = nowStr()
	}
	_, err = b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"INSERT INTO plan_access ("+planAccessCols+") VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+") ON CONFLICT (plan_code, telegram_id, email) DO NOTHING",
		code, tgID, email, createdAt)
	return err
}

// RevokePlanAccess убирает запись из списка допущенных тарифа.
func (b *base) RevokePlanAccess(ctx context.Context, code string, tgID int64, email string) error {
	tgID, email, err := normPlanAccess(code, tgID, email)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"DELETE FROM plan_access WHERE plan_code = "+b.ph(1)+" AND telegram_id = "+b.ph(2)+" AND email = "+b.ph(3),
		code, tgID, email)
	return err
}

// HasPlanAccess отвечает, есть ли пользователь в списке допущенных тарифа.
// Совпадением считается и запись по Telegram ID, и запись по почте: у
// e-mail-аккаунтов кабинета синтетический отрицательный ID, который админ в
// список не положит, — их пускают по почте.
func (b *base) HasPlanAccess(ctx context.Context, code string, tgID int64, email string) (bool, error) {
	email = model.NormalizeEmail(email)
	var n int
	err := b.db.QueryRowContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значения передаются биндовыми параметрами
		"SELECT COUNT(*) FROM plan_access WHERE plan_code = "+b.ph(1)+
			" AND ((telegram_id != 0 AND telegram_id = "+b.ph(2)+") OR (email != '' AND email = "+b.ph(3)+"))",
		code, tgID, email).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListPlanAccess возвращает список допущенных тарифа в порядке добавления.
// Порядок доопределён самой записью: время добавления в RFC3339 сортируется
// как текст, а равные отметки времени упорядочиваются детерминированно.
func (b *base) ListPlanAccess(ctx context.Context, code string) ([]model.PlanAccess, error) {
	rows, err := b.db.QueryContext(ctx,
		// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
		"SELECT "+planAccessCols+" FROM plan_access WHERE plan_code = "+b.ph(1)+
			" ORDER BY created_at, telegram_id, email", code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanAccess(rows)
}

// ListAllPlanAccess возвращает списки допущенных всех тарифов — для снимка
// базы.
func (b *base) ListAllPlanAccess(ctx context.Context) ([]model.PlanAccess, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT "+planAccessCols+" FROM plan_access ORDER BY plan_code, created_at, telegram_id, email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanAccess(rows)
}

// ClearPlanAccess очищает список допущенных тарифа: при удалении тарифа и при
// уходе с режима «по списку» (иначе у публичного тарифа копились бы «мусорные»
// разрешения, молча оживающие при возврате режима).
func (b *base) ClearPlanAccess(ctx context.Context, code string) error {
	// #nosec G202 -- b.ph выдаёт только placeholder драйвера ($1/?), значение передаётся биндовым параметром
	_, err := b.db.ExecContext(ctx, "DELETE FROM plan_access WHERE plan_code = "+b.ph(1), code)
	return err
}

// PrunePlanAccess удаляет записи списков, у которых больше нет тарифа.
// Осиротевшие строки оставляет предыдущий образ бота: его DeletePlan про
// таблицу списков не знает, и запись молча ожила бы, достанься код новому
// тарифу. Зовётся на старте.
func (b *base) PrunePlanAccess(ctx context.Context) error {
	_, err := b.db.ExecContext(ctx,
		"DELETE FROM plan_access WHERE plan_code NOT IN (SELECT code FROM plans)")
	return err
}

func scanPlanAccess(rows *sql.Rows) ([]model.PlanAccess, error) {
	var out []model.PlanAccess
	for rows.Next() {
		var e model.PlanAccess
		if err := rows.Scan(&e.PlanCode, &e.TelegramID, &e.Email, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
