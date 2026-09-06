package storage

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*/*.sql
var migrationsFS embed.FS

func runMigrations(ctx context.Context, b *base, dialect string) error {
	if _, err := b.db.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)"); err != nil {
		return fmt.Errorf("создание schema_migrations: %w", err)
	}

	dir := "migrations/" + dialect
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return fmt.Errorf("чтение миграций %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// Применённые версии читаем ОДНИМ запросом до цикла. Внутри цикла нельзя:
	// у SQLite пул из одного соединения, и запрос при открытой транзакции
	// встал бы в очередь сам за собой — навсегда.
	done, err := appliedVersions(ctx, b)
	if err != nil {
		return err
	}

	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("имя миграции %q должно начинаться с номера: %w", name, err)
		}
		if done[version] {
			continue
		}
		stmt, err := migrationsFS.ReadFile(dir + "/" + name)
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, b, dialect, name, version, string(stmt)); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, b *base) (map[int]bool, error) {
	rows, err := b.db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// applyMigration выполняет шаг и отмечает версию ОДНОЙ транзакцией.
//
// Раньше это были два отдельных запроса, и обрыв между ними (моргнуло питание,
// убили контейнер) оставлял базу в состоянии «колонка добавлена, версия не
// записана». При следующем старте миграция шла заново и падала на «колонка уже
// есть» — бот не поднимался вообще, лечилось только руками в базе.
func applyMigration(ctx context.Context, b *base, dialect, name string, version int, stmt string) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("миграция %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()

	// Postgres после ошибки внутри транзакции отвергает ВСЕ последующие
	// запросы («current transaction is aborted»), поэтому шаг, который может
	// оказаться уже применённым, огораживаем точкой сохранения.
	const sp = "SAVEPOINT rb_step"
	if dialect == "pg" {
		if _, err := tx.ExecContext(ctx, sp); err != nil {
			return fmt.Errorf("миграция %s: %w", name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		if !isAlreadyApplied(err) {
			return fmt.Errorf("миграция %s: %w", name, err)
		}
		// Цель миграции уже достигнута — фиксируем версию и едем дальше.
		// История dev-канала знает случай, когда колонку добавляли двумя
		// редакциями миграций; а на Postgres сюда же попадают установки,
		// оборвавшиеся между шагом и записью версии до этой правки.
		fmt.Printf("миграция %s: изменение уже применено — отмечено как выполненное\n", name)
		if dialect == "pg" {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO "+sp); err != nil {
				return fmt.Errorf("миграция %s: %w", name, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version) VALUES ("+b.ph(1)+")", version); err != nil {
		return fmt.Errorf("миграция %s: запись версии: %w", name, err)
	}
	return tx.Commit()
}

// isAlreadyApplied — ошибка означает «это изменение схемы уже есть».
func isAlreadyApplied(err error) bool {
	if pg := pgError(err); pg != nil {
		switch pg.Code {
		case "42701", // duplicate_column
			"42P07", // duplicate_table (в том числе индекс)
			"42710": // duplicate_object
			return true
		}
		return false
	}
	// modernc/sqlite кода «дублирующаяся колонка» не выделяет — только текст.
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

// pgError достаёт ошибку Postgres из цепочки. Разбор по КОДУ, а не по тексту:
// текст меняется от версии сервера и локали, а код зафиксирован стандартом.
func pgError(err error) *pgconn.PgError {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg
	}
	return nil
}

// sqliteCode — код ошибки SQLite (0, если ошибка не от него).
func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

// pgUniqueViolation — нарушение уникального ограничения в Postgres.
const pgUniqueViolation = "23505"

// уникальные ограничения, различаемые по коду
const (
	sqliteUniqueIndex = sqlite3.SQLITE_CONSTRAINT_UNIQUE     // нарушен UNIQUE-индекс
	sqliteUniquePK    = sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY // столкнулись первичные ключи
)
