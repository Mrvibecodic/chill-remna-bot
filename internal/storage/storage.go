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

	"remnabot/internal/crypto"
	"remnabot/internal/model"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер "pgx"
	_ "modernc.org/sqlite"             // драйвер "sqlite" (чистый Go, без CGO)
)

// Storage — контракт хранилища. Один и тот же набор тестов гоняется против
// обеих реализаций (см. storage_contract_test.go), что гарантирует идентичное
// поведение перед переключением/миграцией БД.
type Storage interface {
	Migrate(ctx context.Context) error
	// LoadConfig возвращает конфигурацию и флаг существования (false на чистой БД).
	LoadConfig(ctx context.Context) (*model.BotConfig, bool, error)
	SaveConfig(ctx context.Context, cfg *model.BotConfig) error
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
