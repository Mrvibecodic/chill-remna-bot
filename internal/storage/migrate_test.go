package storage

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func migrationNames(t *testing.T, dialect string) []string {
	t.Helper()
	entries, err := fs.ReadDir(migrationsFS, "migrations/"+dialect)
	if err != nil {
		t.Fatalf("чтение миграций %s: %v", dialect, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	return names
}

// Классическая ошибка: миграцию завели только для PostgreSQL, а установки на
// SQLite (дефолт для мелких развёртываний) остались со старой схемой и падают
// уже в рантайме. Списки обязаны совпадать имя-в-имя.
func TestMigrationsParityBetweenDialects(t *testing.T) {
	pg := migrationNames(t, "pg")
	lite := migrationNames(t, "sqlite")

	inLite := map[string]bool{}
	for _, n := range lite {
		inLite[n] = true
	}
	for _, n := range pg {
		if !inLite[n] {
			t.Errorf("миграция %s есть для pg, но НЕТ для sqlite", n)
		}
	}
	inPG := map[string]bool{}
	for _, n := range pg {
		inPG[n] = true
	}
	for _, n := range lite {
		if !inPG[n] {
			t.Errorf("миграция %s есть для sqlite, но НЕТ для pg", n)
		}
	}
	if len(pg) == 0 {
		t.Fatal("миграции не найдены вовсе")
	}
}

// Номера должны быть уникальны и без дыр: runner берёт версию из имени файла,
// а два файла с одним номером означают, что второй никогда не применится.
func TestMigrationNumbersUniqueAndSequential(t *testing.T) {
	for _, dialect := range []string{"pg", "sqlite"} {
		seen := map[int]string{}
		max := 0
		for _, n := range migrationNames(t, dialect) {
			v, err := strconv.Atoi(strings.SplitN(n, "_", 2)[0])
			if err != nil {
				t.Fatalf("%s/%s: имя не начинается с номера: %v", dialect, n, err)
			}
			if prev, ok := seen[v]; ok {
				t.Errorf("%s: номер %d занят дважды (%s и %s) — вторая миграция не применится", dialect, v, prev, n)
			}
			seen[v] = n
			if v > max {
				max = v
			}
		}
		for v := 1; v <= max; v++ {
			if _, ok := seen[v]; !ok {
				t.Errorf("%s: пропущен номер миграции %d", dialect, v)
			}
		}
	}
}

// Runner выполняет файл ОДНИМ ExecContext. Несколько инструкций в файле
// работают не на всяком драйвере, и «применилась только первая» обнаружится
// уже на живой базе — поэтому держим одну инструкцию на файл.
func TestMigrationsAreSingleStatement(t *testing.T) {
	for _, dialect := range []string{"pg", "sqlite"} {
		for _, n := range migrationNames(t, dialect) {
			body, err := migrationsFS.ReadFile("migrations/" + dialect + "/" + n)
			if err != nil {
				t.Fatal(err)
			}
			var sb strings.Builder
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "--") {
					continue
				}
				sb.WriteString(line)
				sb.WriteString("\n")
			}
			sql := strings.TrimRight(strings.TrimSpace(sb.String()), ";")
			if strings.Contains(sql, ";") {
				t.Errorf("%s/%s: несколько инструкций в одном файле — разнесите по отдельным миграциям", dialect, n)
			}
		}
	}
}

// Полный прогон миграций на SQLite: все ложатся, версия совпадает с числом
// файлов, а индексы журнала платежей реально созданы (а не «первая инструкция
// применилась, вторая потерялась»).
func TestMigrationsApplyOnSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	st, err := Open(model.DBSQLite, path, testCrypter(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("миграции не ложатся на SQLite: %v", err)
	}
	// Повторный прогон обязан быть безопасным (перезапуск бота).
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("повторный прогон миграций упал: %v", err)
	}
	_ = st.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var applied int
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if want := len(migrationNames(t, "sqlite")); applied != want {
		t.Fatalf("применено %d миграций из %d", applied, want)
	}

	for _, idx := range []string{"idx_paylog_ext", "idx_paylog_tg", "idx_paylog_stage", "idx_paylog_created"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("индекс %s не создан на SQLite", idx)
		}
	}

	// Таблицы, которых касается платёжный контур, должны существовать.
	for _, tbl := range []string{"payment_log", "payments", "pending_invoices", "users", "autopay"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("таблица %s не создана на SQLite", tbl)
		}
	}
}
