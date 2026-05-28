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
	"time"

	"remnabot/internal/crypto"
	"remnabot/internal/model"
)

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

	CreateP2PRequest(ctx context.Context, r *model.P2PRequest) error
	GetP2PRequest(ctx context.Context, id int64) (*model.P2PRequest, error)
	UpdateP2PRequest(ctx context.Context, r *model.P2PRequest) error

	AddPayment(ctx context.Context, p *model.Payment) error
	ListPayments(ctx context.Context, limit, offset int) ([]model.Payment, int, error)
	HasPaidPayment(ctx context.Context, telegramID int64) (bool, error)
	PaymentByExtID(ctx context.Context, extID string) (bool, error)

	// LoadMediaFileID возвращает кэшированный Telegram file_id для раздела
	// (если уже отправляли картинку этого раздела по URL и получили id обратно).
	// ok=false означает «надо отправить по URL и закэшировать новый id».
	LoadMediaFileID(ctx context.Context, section string) (id string, ok bool, err error)
	SaveMediaFileID(ctx context.Context, section, fileID string) error

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
	cfg, ok, err := src.LoadConfig(ctx)
	if err != nil {
		return err
	}
	if ok {
		if err := dst.SaveConfig(ctx, cfg); err != nil {
			return err
		}
	}
	return nil
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
	err := b.db.QueryRowContext(ctx,
		"SELECT username, first_name, p2p_approved, blocked, created_at FROM users WHERE telegram_id = "+b.ph(1), telegramID).
		Scan(&username, &firstName, &approved, &blocked, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &model.User{TelegramID: telegramID, Username: username, FirstName: firstName, P2PApproved: approved != 0, Blocked: blocked != 0, CreatedAt: created}, nil
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
	return err
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

func (b *base) HasPaidPayment(ctx context.Context, telegramID int64) (bool, error) {
	var n int
	err := b.db.QueryRowContext(ctx,
		"SELECT COUNT(1) FROM payments WHERE telegram_id = "+b.ph(1)+" AND status = "+b.ph(2),
		telegramID, model.PaymentPaid).Scan(&n)
	return n > 0, err
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
