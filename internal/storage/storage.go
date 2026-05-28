// Package storage — слой доступа к данным с единым интерфейсом и двумя
// реализациями (SQLite и PostgreSQL). Бизнес-логика работает только с
// интерфейсом Storage, поэтому движок БД меняется без переписывания кода.
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remnabot/internal/crypto"
	"remnabot/internal/model"
)

// ErrDuplicateExtID возвращается из AddPayment, когда запись с тем же
// (method, ext_id) уже существует — нарушение UNIQUE-индекса. Применяется
// для идемпотентности обработки повторных вебхуков платёжных провайдеров:
// вызывающий код различает «впервые» и «уже зачтено».
var ErrDuplicateExtID = errors.New("storage: payment with this ext_id already exists")

// Storage — контракт хранилища. Один и тот же набор тестов гоняется против
// обеих реализаций (см. storage_contract_test.go), что гарантирует идентичное
// поведение перед переключением/миграцией БД.
type Storage interface {
	Migrate(ctx context.Context) error
	// LoadConfig возвращает конфигурацию и флаг существования (false на чистой БД).
	LoadConfig(ctx context.Context) (*model.BotConfig, bool, error)
	SaveConfig(ctx context.Context, cfg *model.BotConfig) error

	UpsertUser(ctx context.Context, telegramID int64) error
	// SetUserInfo обновляет ник/имя существующей записи (строку не создаёт).
	SetUserInfo(ctx context.Context, telegramID int64, username, firstName string) error
	GetUser(ctx context.Context, telegramID int64) (*model.User, error)
	SetP2PApproved(ctx context.Context, telegramID int64, approved bool) error
	// HasApprovedPurchase сообщает, есть ли у пользователя одобренная покупка.
	HasApprovedPurchase(ctx context.Context, telegramID int64) (bool, error)
	// ListUsers возвращает страницу пользователей (по created_at) и общее число.
	ListUsers(ctx context.Context, limit, offset int) ([]model.User, int, error)
	SetBlocked(ctx context.Context, telegramID int64, blocked bool) error
	DeleteUser(ctx context.Context, telegramID int64) error
	// DeletePaymentsByUser/DeleteP2PRequestsByUser — каскадная зачистка локальной
	// истории пользователя при его удалении (чтобы после повторной регистрации
	// бот не выводил «Мои подписки» по старым записям).
	DeletePaymentsByUser(ctx context.Context, telegramID int64) error
	DeleteP2PRequestsByUser(ctx context.Context, telegramID int64) error
	// SetTermsAccepted сохраняет факт принятия пользовательского соглашения
	// (ts — ISO-время; пустая строка очищает флаг, если нужно перепринять).
	SetTermsAccepted(ctx context.Context, telegramID int64, ts string) error
	// SetTrialUsed — отметить, что юзер активировал триал (ISO-время).
	SetTrialUsed(ctx context.Context, telegramID int64, ts string) error
	// SetSubExpiry сохраняет срок текущей подписки/триала и сбрасывает счётчик
	// отправленных напоминаний (kind: "paid"|"trial").
	SetSubExpiry(ctx context.Context, telegramID int64, expireAt, kind string) error
	// MarkNotified записывает CSV уже отправленных окон напоминаний.
	MarkNotified(ctx context.Context, telegramID int64, sentCSV string) error
	// UsersForNotify возвращает пользователей с непустым sub_expire_at (кандидаты на напоминание).
	UsersForNotify(ctx context.Context) ([]model.User, error)

	CreateP2PRequest(ctx context.Context, r *model.P2PRequest) error
	GetP2PRequest(ctx context.Context, id int64) (*model.P2PRequest, error)
	UpdateP2PRequest(ctx context.Context, r *model.P2PRequest) error

	AddPayment(ctx context.Context, p *model.Payment) error
	ListPayments(ctx context.Context, limit, offset int) ([]model.Payment, int, error)
	HasPaidPayment(ctx context.Context, telegramID int64) (bool, error)
	// PaidPayments — все оплаченные платежи (для итогов: платящие юзеры + сумма).
	PaidPayments(ctx context.Context) ([]model.Payment, error)
	PaymentByExtID(ctx context.Context, extID string) (bool, error)
	// MostPopularPlan возвращает (months, totalPaidAcrossAllPlans).
	// months = 0, если оплаченных платежей нет вообще.
	// totalPaidAcrossAllPlans позволяет вызывающему коду решить, достаточно ли
	// статистики для показа подсказки (например, >= 10).
	MostPopularPlan(ctx context.Context) (months int, total int, err error)

	// LoadMediaFileID возвращает кэшированный Telegram file_id для раздела
	// (если уже отправляли картинку этого раздела по URL и получили id обратно).
	// ok=false означает «надо отправить по URL и закэшировать новый id».
	LoadMediaFileID(ctx context.Context, section string) (id string, ok bool, err error)
	SaveMediaFileID(ctx context.Context, section, fileID string) error
	// DeleteMediaFileID удаляет кэш для раздела — следующий sendKBSection
	// пойдёт по дефолтному URL из assets (используется кнопкой «Сбросить»).
	DeleteMediaFileID(ctx context.Context, section string) error

	Export(ctx context.Context) (*Snapshot, error)
	Import(ctx context.Context, s *Snapshot) error

	// pending_invoices — рабочий список реконсилятора (см. RunReconciler).
	AddPendingInvoice(ctx context.Context, p *model.PendingInvoice) error
	// ListUnresolvedPending — неподтверждённые инвойсы, созданные не позже
	// createdBefore (RFC3339 UTC), не более limit штук.
	ListUnresolvedPending(ctx context.Context, createdBefore string, limit int) ([]model.PendingInvoice, error)
	ResolvePending(ctx context.Context, id int64) error

	Kind() string
	Close() error
}

// Open подключается к выбранному движку. dsn для sqlite — путь к файлу,
// для postgres — строка подключения. crypter шифрует конфиг при записи.
func Open(kind, dsn string, crypter *crypto.Crypter) (Storage, error) {
	switch kind {
	case model.DBSQLite:
		return openSQLite(dsn, crypter)
	case model.DBPostgres:
		return openPostgres(dsn, crypter)
	default:
		return nil, fmt.Errorf("неизвестный движок БД: %q", kind)
	}
}

// base — общая часть обеих реализаций: хранит *sql.DB, диалект и crypter.
type base struct {
	db      *sql.DB
	kind    string
	ph      placeholderFunc // стиль плейсхолдеров ($1 для PG, ? для SQLite)
	crypter *crypto.Crypter
}

type placeholderFunc func(n int) string

func (b *base) Kind() string { return b.kind }
func (b *base) Close() error { return b.db.Close() }

// loadConfig читает единственную строку настроек (id=1) и расшифровывает её.
func (b *base) loadConfig(ctx context.Context) (*model.BotConfig, bool, error) {
	var enc string
	err := b.db.QueryRowContext(ctx, "SELECT config FROM settings WHERE id = 1").Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err := b.crypter.Decrypt(enc)
	if err != nil {
		return nil, false, fmt.Errorf("расшифровка конфига: %w", err)
	}
	var cfg model.BotConfig
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return nil, false, fmt.Errorf("разбор конфига: %w", err)
	}
	return &cfg, true, nil
}

// saveConfig сериализует и шифрует конфиг и сохраняет его в строку id=1 (upsert).
func (b *base) saveConfig(ctx context.Context, cfg *model.BotConfig, upsertSQL string) error {
	plain, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	enc, err := b.crypter.Encrypt(plain)
	if err != nil {
		return err
	}
	_, err = b.db.ExecContext(ctx, upsertSQL, enc)
	return err
}

// Transfer переносит данные из src в dst (при смене движка БД, напр. SQLite → PostgreSQL).
// Сейчас это таблица настроек; по мере роста схемы сюда добавляются остальные таблицы.
func Transfer(ctx context.Context, src, dst Storage) error {
	snap, err := src.Export(ctx)
	if err != nil {
		return err
	}
	if snap.Config != nil {
		if err := dst.SaveConfig(ctx, snap.Config); err != nil {
			return err
		}
	}
	return dst.Import(ctx, snap)
}

func nowStr() string { return time.Now().UTC().Format(time.RFC3339) }
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (b *base) UpsertUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, created_at) VALUES ("+b.ph(1)+", 0, "+b.ph(2)+") "+
			"ON CONFLICT (telegram_id) DO NOTHING",
		telegramID, nowStr())
	return err
}

func (b *base) SetUserInfo(ctx context.Context, telegramID int64, username, firstName string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET username = "+b.ph(1)+", first_name = "+b.ph(2)+" WHERE telegram_id = "+b.ph(3),
		username, firstName, telegramID)
	return err
}

func (b *base) HasApprovedPurchase(ctx context.Context, telegramID int64) (bool, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM p2p_requests WHERE telegram_id = "+b.ph(1)+" AND status = "+b.ph(2),
		telegramID, model.P2PApproved).Scan(&n)
	return n > 0, err
}

func (b *base) GetUser(ctx context.Context, telegramID int64) (*model.User, error) {
	var approved, blocked int
	var created, username, firstName string
	// terms_accepted_at: в PG это TIMESTAMPTZ NULL, в SQLite — TEXT с дефолтом
	// "". Через sql.NullString корректно ловим оба случая (NULL и пусто).
	var terms, trial sql.NullString
	var subExp, notifyKind, notifySent string
	err := b.db.QueryRowContext(ctx,
		"SELECT username, first_name, p2p_approved, blocked, created_at, terms_accepted_at, trial_used_at, sub_expire_at, notify_kind, notify_sent FROM users WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&username, &firstName, &approved, &blocked, &created, &terms, &trial, &subExp, &notifyKind, &notifySent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &model.User{TelegramID: telegramID, Username: username, FirstName: firstName, P2PApproved: approved != 0, Blocked: blocked != 0, CreatedAt: created, TermsAcceptedAt: terms.String, TrialUsedAt: trial.String, SubExpireAt: subExp, NotifyKind: notifyKind, NotifySent: notifySent}, nil
}

func (b *base) SetP2PApproved(ctx context.Context, telegramID int64, approved bool) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, created_at) VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET p2p_approved = excluded.p2p_approved",
		telegramID, boolToInt(approved), nowStr())
	return err
}

func (b *base) ListUsers(ctx context.Context, limit, offset int) ([]model.User, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, username, first_name, p2p_approved, blocked, created_at FROM users "+
			"ORDER BY created_at DESC, telegram_id DESC LIMIT "+b.ph(1)+" OFFSET "+b.ph(2),
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		var approved, blocked int
		if err := rows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &approved, &blocked, &u.CreatedAt); err != nil {
			return nil, 0, err
		}
		u.P2PApproved = approved != 0
		u.Blocked = blocked != 0
		out = append(out, u)
	}
	return out, total, rows.Err()
}

func (b *base) SetBlocked(ctx context.Context, telegramID int64, blocked bool) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, blocked, created_at) VALUES ("+b.ph(1)+", 0, "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET blocked = excluded.blocked",
		telegramID, boolToInt(blocked), nowStr())
	return err
}

func (b *base) DeleteUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM users WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) DeletePaymentsByUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM payments WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) DeleteP2PRequestsByUser(ctx context.Context, telegramID int64) error {
	_, err := b.db.ExecContext(ctx, "DELETE FROM p2p_requests WHERE telegram_id = "+b.ph(1), telegramID)
	return err
}

func (b *base) SetTermsAccepted(ctx context.Context, telegramID int64, ts string) error {
	// Пустую строку не пишем: PG-колонка — TIMESTAMPTZ NULL (не примет ""),
	// а в SQLite — TEXT NOT NULL с дефолтом "". В коде сброс соглашения не
	// используется, операция только set; пустой ts превращаем в no-op.
	if ts == "" {
		return nil
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET terms_accepted_at = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		ts, telegramID)
	return err
}

func (b *base) SetTrialUsed(ctx context.Context, telegramID int64, ts string) error {
	if ts == "" {
		return nil
	}
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET trial_used_at = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		ts, telegramID)
	return err
}

func (b *base) SetSubExpiry(ctx context.Context, telegramID int64, expireAt, kind string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET sub_expire_at = "+b.ph(1)+", notify_kind = "+b.ph(2)+", notify_sent = '' WHERE telegram_id = "+b.ph(3),
		expireAt, kind, telegramID)
	return err
}

func (b *base) MarkNotified(ctx context.Context, telegramID int64, sentCSV string) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE users SET notify_sent = "+b.ph(1)+" WHERE telegram_id = "+b.ph(2),
		sentCSV, telegramID)
	return err
}

func (b *base) UsersForNotify(ctx context.Context) ([]model.User, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, username, first_name, sub_expire_at, notify_kind, notify_sent FROM users WHERE sub_expire_at <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &u.SubExpireAt, &u.NotifyKind, &u.NotifySent); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (b *base) CreateP2PRequest(ctx context.Context, r *model.P2PRequest) error {
	if r.ID == 0 {
		r.ID = time.Now().UnixNano()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO p2p_requests (id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+")",
		r.ID, r.TelegramID, r.Months, r.Price, r.Status, r.Screenshot, r.Comment, r.CreatedAt, r.DecidedAt)
	return err
}

func (b *base) GetP2PRequest(ctx context.Context, id int64) (*model.P2PRequest, error) {
	r := &model.P2PRequest{}
	err := b.db.QueryRowContext(ctx,
		"SELECT id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at "+
			"FROM p2p_requests WHERE id = "+b.ph(1), id).
		Scan(&r.ID, &r.TelegramID, &r.Months, &r.Price, &r.Status, &r.Screenshot, &r.Comment, &r.CreatedAt, &r.DecidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (b *base) UpdateP2PRequest(ctx context.Context, r *model.P2PRequest) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE p2p_requests SET status = "+b.ph(1)+", screenshot = "+b.ph(2)+", comment = "+b.ph(3)+", decided_at = "+b.ph(4)+
			" WHERE id = "+b.ph(5),
		r.Status, r.Screenshot, r.Comment, r.DecidedAt, r.ID)
	return err
}

func (b *base) AddPayment(ctx context.Context, p *model.Payment) error {
	if p.ID == 0 {
		p.ID = time.Now().UnixNano()
	}
	if p.CreatedAt == "" {
		p.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO payments (id, telegram_id, method, months, amount, status, comment, ext_id, created_at) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", "+b.ph(7)+", "+b.ph(8)+", "+b.ph(9)+")",
		p.ID, p.TelegramID, p.Method, p.Months, p.Amount, p.Status, p.Comment, p.ExtID, p.CreatedAt)
	if err != nil && isUniqueViolation(err) {
		return ErrDuplicateExtID
	}
	return err
}

// isUniqueViolation распознаёт нарушение UNIQUE-ограничения для обоих
// драйверов (modernc/sqlite и pgx). Признаков несколько: SQLite пишет
// «UNIQUE constraint failed», PG — SQLSTATE 23505. Сравнение по тексту
// ошибки — компромисс ради независимости от драйверного типа.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "23505") ||
		strings.Contains(msg, "duplicate key")
}

func (b *base) ListPayments(ctx context.Context, limit, offset int) ([]model.Payment, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM payments").Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, method, months, amount, status, comment, ext_id, created_at FROM payments "+
			"ORDER BY created_at DESC, id DESC LIMIT "+b.ph(1)+" OFFSET "+b.ph(2),
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// MostPopularPlan — самый частый тариф среди оплаченных платежей и общее число
// оплаченных платежей. SQL общий для PG/SQLite; ORDER BY count DESC + LIMIT 1.
func (b *base) MostPopularPlan(ctx context.Context) (int, int, error) {
	var total int
	if err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE status = "+b.ph(1),
		model.PaymentPaid).Scan(&total); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	var months int
	err := b.db.QueryRowContext(ctx,
		"SELECT months FROM payments WHERE status = "+b.ph(1)+
			" GROUP BY months ORDER BY COUNT(1) DESC, months ASC LIMIT 1",
		model.PaymentPaid).Scan(&months)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, total, nil
	}
	if err != nil {
		return 0, total, err
	}
	return months, total, nil
}

func (b *base) HasPaidPayment(ctx context.Context, telegramID int64) (bool, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE telegram_id = "+b.ph(1)+" AND status = "+b.ph(2),
		telegramID, model.PaymentPaid).Scan(&n)
	return n > 0, err
}

func (b *base) PaidPayments(ctx context.Context) ([]model.Payment, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, method, months, amount, status, comment, ext_id, created_at FROM payments "+
			"WHERE status = "+b.ph(1)+" ORDER BY created_at DESC",
		model.PaymentPaid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Payment
	for rows.Next() {
		var p model.Payment
		if err := rows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (b *base) PaymentByExtID(ctx context.Context, extID string) (bool, error) {
	if extID == "" {
		return false, nil
	}
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE ext_id = "+b.ph(1), extID).Scan(&n)
	return n > 0, err
}

// LoadMediaFileID — реализация на *base, диалект-нейтральная через b.ph(n).
// Используется и pgStore, и sqliteStore через embedding.
func (b *base) LoadMediaFileID(ctx context.Context, section string) (string, bool, error) {
	var id string
	err := b.db.QueryRowContext(ctx,
		"SELECT file_id FROM media_cache WHERE section = "+b.ph(1), section).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// SaveMediaFileID — upsert по section. Синтаксис ON CONFLICT ... DO UPDATE
// поддерживают и PG, и SQLite; время пишем через nowStr() как в соседних
// методах (UpsertUser, и т.п.), чтобы избежать диалект-различий now()/datetime('now').
func (b *base) SaveMediaFileID(ctx context.Context, section, fileID string) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO media_cache (section, file_id, updated_at) VALUES ("+
			b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+") "+
			"ON CONFLICT (section) DO UPDATE SET file_id = excluded.file_id, updated_at = excluded.updated_at",
		section, fileID, nowStr())
	return err
}

// DeleteMediaFileID — used by admin «reset banner to default».
func (b *base) DeleteMediaFileID(ctx context.Context, section string) error {
	_, err := b.db.ExecContext(ctx,
		"DELETE FROM media_cache WHERE section = "+b.ph(1), section)
	return err
}

// --- перенос данных между движками БД (SQLite ↔ PostgreSQL) ---

// Snapshot — полный слепок данных бота. Config переносит Transfer через
// SaveConfig (upsert настроек диалект-специфичен), остальное — через Import.
type Snapshot struct {
	Config   *model.BotConfig
	Users    []model.User
	Payments []model.Payment
	P2P      []model.P2PRequest
	Media    []MediaItem
}

// MediaItem — запись media_cache. updated_at не переносим: это лишь кэш
// Telegram file_id, при импорте время проставляется заново.
type MediaItem struct {
	Section string
	FileID  string
}

// Export читает все данные через общий *base (диалект-нейтральные SELECT).
// Реализован один раз на base, поэтому оба драйвера (pg/sqlite) ведут себя
// одинаково — это и обеспечивает идентичность переноса в обе стороны.
func (b *base) Export(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{}
	if cfg, ok, err := b.loadConfig(ctx); err != nil {
		return nil, err
	} else if ok {
		snap.Config = cfg
	}

	urows, err := b.db.QueryContext(ctx,
		"SELECT telegram_id, username, first_name, p2p_approved, blocked, created_at, terms_accepted_at, trial_used_at FROM users")
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var u model.User
		var approved, blocked int
		var terms, trial sql.NullString
		if err := urows.Scan(&u.TelegramID, &u.Username, &u.FirstName, &approved, &blocked, &u.CreatedAt, &terms, &trial); err != nil {
			urows.Close()
			return nil, err
		}
		u.P2PApproved = approved != 0
		u.Blocked = blocked != 0
		u.TermsAcceptedAt = terms.String
		u.TrialUsedAt = trial.String
		snap.Users = append(snap.Users, u)
	}
	if err := urows.Err(); err != nil {
		urows.Close()
		return nil, err
	}
	urows.Close()

	prows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, method, months, amount, status, comment, ext_id, created_at FROM payments")
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var p model.Payment
		if err := prows.Scan(&p.ID, &p.TelegramID, &p.Method, &p.Months, &p.Amount, &p.Status, &p.Comment, &p.ExtID, &p.CreatedAt); err != nil {
			prows.Close()
			return nil, err
		}
		snap.Payments = append(snap.Payments, p)
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return nil, err
	}
	prows.Close()

	rrows, err := b.db.QueryContext(ctx,
		"SELECT id, telegram_id, months, price, status, screenshot, comment, created_at, decided_at FROM p2p_requests")
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var r model.P2PRequest
		if err := rrows.Scan(&r.ID, &r.TelegramID, &r.Months, &r.Price, &r.Status, &r.Screenshot, &r.Comment, &r.CreatedAt, &r.DecidedAt); err != nil {
			rrows.Close()
			return nil, err
		}
		snap.P2P = append(snap.P2P, r)
	}
	if err := rrows.Err(); err != nil {
		rrows.Close()
		return nil, err
	}
	rrows.Close()

	mrows, err := b.db.QueryContext(ctx, "SELECT section, file_id FROM media_cache")
	if err != nil {
		return nil, err
	}
	for mrows.Next() {
		var m MediaItem
		if err := mrows.Scan(&m.Section, &m.FileID); err != nil {
			mrows.Close()
			return nil, err
		}
		snap.Media = append(snap.Media, m)
	}
	if err := mrows.Err(); err != nil {
		mrows.Close()
		return nil, err
	}
	mrows.Close()

	return snap, nil
}

// Import пишет данные слепка (кроме Config — его переносит Transfer). Идемпотентно
// для users (ON CONFLICT) и payments (UNIQUE ext_id → пропуск дубля); рассчитан
// прежде всего на перенос в ПУСТУЮ новую БД при смене движка.
func (b *base) Import(ctx context.Context, s *Snapshot) error {
	if s == nil {
		return nil
	}
	for i := range s.Users {
		if err := b.importUser(ctx, &s.Users[i]); err != nil {
			return err
		}
	}
	for i := range s.Payments {
		if err := b.AddPayment(ctx, &s.Payments[i]); err != nil && !errors.Is(err, ErrDuplicateExtID) {
			return err
		}
	}
	for i := range s.P2P {
		if err := b.CreateP2PRequest(ctx, &s.P2P[i]); err != nil && !isUniqueViolation(err) {
			return err
		}
	}
	for i := range s.Media {
		if err := b.SaveMediaFileID(ctx, s.Media[i].Section, s.Media[i].FileID); err != nil {
			return err
		}
	}
	return nil
}

// importUser вставляет пользователя со всеми полями, СОХРАНЯЯ created_at.
// terms/trial выставляются отдельными сеттерами (диалект-безопасно: пустые
// значения не пишутся — в PG это NULL-колонки timestamptz).
func (b *base) importUser(ctx context.Context, u *model.User) error {
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO users (telegram_id, p2p_approved, blocked, created_at, username, first_name) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+") "+
			"ON CONFLICT (telegram_id) DO UPDATE SET "+
			"p2p_approved = excluded.p2p_approved, blocked = excluded.blocked, "+
			"created_at = excluded.created_at, username = excluded.username, first_name = excluded.first_name",
		u.TelegramID, boolToInt(u.P2PApproved), boolToInt(u.Blocked), u.CreatedAt, u.Username, u.FirstName)
	if err != nil {
		return err
	}
	if u.TermsAcceptedAt != "" {
		if err := b.SetTermsAccepted(ctx, u.TelegramID, u.TermsAcceptedAt); err != nil {
			return err
		}
	}
	if u.TrialUsedAt != "" {
		if err := b.SetTrialUsed(ctx, u.TelegramID, u.TrialUsedAt); err != nil {
			return err
		}
	}
	return nil
}

// --- pending_invoices (рабочий список реконсилятора) ---

func (b *base) AddPendingInvoice(ctx context.Context, p *model.PendingInvoice) error {
	if p.ID == 0 {
		p.ID = time.Now().UnixNano()
	}
	if p.CreatedAt == "" {
		p.CreatedAt = nowStr()
	}
	_, err := b.db.ExecContext(ctx,
		"INSERT INTO pending_invoices (id, method, ext_id, telegram_id, months, created_at, resolved) "+
			"VALUES ("+b.ph(1)+", "+b.ph(2)+", "+b.ph(3)+", "+b.ph(4)+", "+b.ph(5)+", "+b.ph(6)+", 0)",
		p.ID, p.Method, p.ExtID, p.TelegramID, p.Months, p.CreatedAt)
	return err
}

// ListUnresolvedPending — created_at хранится в RFC3339 UTC (фиксированная
// ширина), поэтому лексикографическое сравнение строк корректно работает как
// сравнение времени для обоих движков без диалект-специфичных функций дат.
func (b *base) ListUnresolvedPending(ctx context.Context, createdBefore string, limit int) ([]model.PendingInvoice, error) {
	rows, err := b.db.QueryContext(ctx,
		"SELECT id, method, ext_id, telegram_id, months, created_at FROM pending_invoices "+
			"WHERE resolved = 0 AND created_at <= "+b.ph(1)+" ORDER BY created_at ASC LIMIT "+b.ph(2),
		createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PendingInvoice
	for rows.Next() {
		var p model.PendingInvoice
		if err := rows.Scan(&p.ID, &p.Method, &p.ExtID, &p.TelegramID, &p.Months, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (b *base) ResolvePending(ctx context.Context, id int64) error {
	_, err := b.db.ExecContext(ctx,
		"UPDATE pending_invoices SET resolved = 1 WHERE id = "+b.ph(1), id)
	return err
}
