package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"remnabot/internal/crypto"
	"remnabot/internal/model"
)

func baseOf(t *testing.T, st Storage) *base {
	t.Helper()
	switch s := st.(type) {
	case *sqliteStore:
		return &s.base
	case *pgStore:
		return &s.base
	}
	t.Fatalf("неизвестное хранилище %T", st)
	return nil
}

// allTables — все таблицы базы, как их видит сама СУБД.
func allTables(t *testing.T, b *base) []string {
	t.Helper()
	q := "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'"
	if b.kind == model.DBPostgres {
		q = "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'"
	}
	rows, err := b.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != "schema_migrations" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func TestBackupTablesMatchSchema(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		got := allTables(t, baseOf(t, st))
		want := append([]string(nil), backupTables...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("таблицы базы и backupTables разошлись — новую таблицу надо внести в бэкап:\nбаза:   %v\nбэкап:  %v", got, want)
		}
	})
}

// fillEveryColumn кладёт в каждую таблицу по три строки, где у КАЖДОЙ колонки
// своё, не умолчательное значение. Так тест ловит любую колонку, которую бэкап
// потерял бы, — включая колонки, добавленные позже этого теста.
func fillEveryColumn(t *testing.T, st Storage, salt int) {
	t.Helper()
	ctx := context.Background()
	b := baseOf(t, st)
	cfg := sampleConfig()
	cfg.Panel.APIToken = fmt.Sprintf("token-%d", salt)
	if err := st.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	for ti, table := range backupTables {
		if table == "settings" {
			continue
		}
		cols, err := b.tableColumns(ctx, b.db, table)
		if errors.Is(err, errNoTable) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, len(cols))
		ph := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
			ph[i] = b.ph(i + 1)
		}
		for r := 1; r <= 3; r++ {
			args := make([]any, len(cols))
			for i, c := range cols {
				switch {
				case c.isInt:
					args[i] = int64(1000000000 + salt*100000 + ti*1000 + r*100 + i)
				case strings.HasSuffix(c.name, "_at"):
					args[i] = fmt.Sprintf("2026-0%d-1%dT03:04:0%dZ", r, salt%10, i%10)
				default:
					args[i] = fmt.Sprintf("v%d-%s-%s-%d", salt, table, c.name, r)
				}
			}
			q := "INSERT INTO " + table + " (" + quoteCols(names) + ") VALUES (" + strings.Join(ph, ", ") + ")"
			if _, err := b.db.ExecContext(ctx, q, args...); err != nil {
				t.Fatalf("заполнение %s: %v", table, err)
			}
		}
	}
}

// rawDump — содержимое всех таблиц как строки, отсортированные внутри
// таблицы. Конфиг сравнивается расшифрованным: шифротекст у каждой записи свой.
func rawDump(t *testing.T, st Storage) map[string][]string {
	t.Helper()
	ctx := context.Background()
	b := baseOf(t, st)
	out := map[string][]string{}
	for _, table := range backupTables {
		cols, err := b.tableColumns(ctx, b.db, table)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
		}
		rows, err := b.db.QueryContext(ctx, "SELECT "+quoteCols(names)+" FROM "+table)
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			parts := make([]string, len(cols))
			for i, v := range vals {
				dv, err := dumpValue(v)
				if err != nil {
					t.Fatal(err)
				}
				if table == "settings" && names[i] == "config" {
					plain, err := b.crypter.Decrypt(dv.(string))
					if err != nil {
						t.Fatal(err)
					}
					var m map[string]any
					if err := json.Unmarshal(plain, &m); err != nil {
						t.Fatal(err)
					}
					norm, _ := json.Marshal(m)
					dv = string(norm)
				}
				// Одна метка времени в записи SQLite и Postgres выглядит по-разному.
				if s, ok := dv.(string); ok {
					for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
						if tm, err := time.Parse(layout, s); err == nil {
							dv = tm.UTC().Format(time.RFC3339Nano)
							break
						}
					}
				}
				// Пустая метка времени в SQLite и NULL в Postgres — одно и то же.
				if dv == nil {
					dv = ""
				}
				parts[i] = names[i] + "=" + fmt.Sprint(dv)
			}
			out[table] = append(out[table], strings.Join(parts, "|"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		sort.Strings(out[table])
	}
	return out
}

func backupBytes(t *testing.T, st Storage) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := st.WriteBackup(context.Background(), &buf, "test"); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func opener(data []byte) func() (io.Reader, error) {
	return func() (io.Reader, error) { return bytes.NewReader(data), nil }
}

func compareDumps(t *testing.T, want, got map[string][]string) {
	t.Helper()
	for _, table := range backupTables {
		if len(want[table]) == 0 {
			t.Fatalf("таблица %s пуста до бэкапа — тест ничего не проверяет", table)
		}
		if !reflect.DeepEqual(want[table], got[table]) {
			t.Fatalf("таблица %s не вернулась как была:\nбыло:  %v\nстало: %v", table, want[table], got[table])
		}
	}
}

func TestBackupRestoreEveryColumn(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		before := rawDump(t, st)
		data := backupBytes(t, st)

		info, err := st.CheckBackup(ctx, opener(data))
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range backupTables {
			if info.Rows[table] != int64(len(before[table])) {
				t.Fatalf("сводка %s: %d строк, в базе %d", table, info.Rows[table], len(before[table]))
			}
		}

		// После бэкапа база живёт дальше: новые строки и правки старых.
		// Восстановление обязано вернуть ровно состояние файла.
		cleanAll(t, st)
		fillEveryColumn(t, st, 2)
		if reflect.DeepEqual(before, rawDump(t, st)) {
			t.Fatal("база не изменилась — проверка восстановления ничего не докажет")
		}

		if err := st.RestoreBackup(ctx, opener(data), nil); err != nil {
			t.Fatal(err)
		}
		compareDumps(t, withKind(before, st.Kind()), rawDump(t, st))

		cfg, ok, err := st.LoadConfig(ctx)
		if err != nil || !ok || cfg.Panel.APIToken != "token-1" || cfg.DBKind != st.Kind() {
			t.Fatalf("конфиг после восстановления: ok=%v err=%v", ok, err)
		}
	})
}

// withKind — ожидаемое состояние после переезда: в конфиге движок новой базы.
func withKind(d map[string][]string, kind string) map[string][]string {
	out := map[string][]string{}
	for k, v := range d {
		out[k] = v
	}
	var rows []string
	for _, r := range d["settings"] {
		for _, from := range []string{model.DBSQLite, model.DBPostgres} {
			r = strings.Replace(r, `"db_kind":"`+from+`"`, `"db_kind":"`+kind+`"`, 1)
		}
		rows = append(rows, r)
	}
	out["settings"] = rows
	return out
}

func cleanAll(t *testing.T, st Storage) {
	t.Helper()
	b := baseOf(t, st)
	for _, table := range backupTables {
		if _, err := b.db.Exec("DELETE FROM " + table); err != nil {
			t.Fatal(err)
		}
	}
}

// Новый сервер: другая СУБД и другой ключ шифрования настроек. Файл обязан
// открыться без старого ключа и вернуть всё.
func TestBackupCrossEngine(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN не задан")
	}
	ctx := context.Background()
	openPG := func(key string) Storage {
		c, err := crypto.NewFromKeyMaterial([]byte(key))
		if err != nil {
			t.Fatal(err)
		}
		st, err := Open(model.DBPostgres, dsn, c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		cleanPGData(t, dsn)
		return st
	}
	t.Run("sqlite→postgres", func(t *testing.T) {
		src := openSQLiteTest(t)
		fillEveryColumn(t, src, 3)
		before := rawDump(t, src)
		dst := openPG("другой-ключ")
		fillEveryColumn(t, dst, 4)
		if err := dst.RestoreBackup(ctx, opener(backupBytes(t, src)), nil); err != nil {
			t.Fatal(err)
		}
		compareDumps(t, withKind(before, model.DBPostgres), rawDump(t, dst))
	})
	t.Run("postgres→sqlite", func(t *testing.T) {
		src := openPG("ключ-источника")
		fillEveryColumn(t, src, 5)
		before := rawDump(t, src)
		dst := openSQLiteTest(t)
		fillEveryColumn(t, dst, 6)
		if err := dst.RestoreBackup(ctx, opener(backupBytes(t, src)), nil); err != nil {
			t.Fatal(err)
		}
		compareDumps(t, withKind(before, model.DBSQLite), rawDump(t, dst))
	})
}

func TestBackupRejectsBrokenFiles(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		data := backupBytes(t, st)
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		join := func(ls []string) []byte { return []byte(strings.Join(ls, "\n") + "\n") }

		// Строка таблицы users — первая строка после её заголовка.
		usersRow := -1
		for i, l := range lines {
			if strings.HasPrefix(l, `{"t":"tb","name":"users"`) {
				usersRow = i + 1
				break
			}
		}
		if usersRow < 0 || !strings.HasPrefix(lines[usersRow], "[") {
			t.Fatal("в файле нет строк users")
		}
		dropRow := append(append([]string(nil), lines[:usersRow]...), lines[usersRow+1:]...)
		extraRow := append(append([]string(nil), lines[:usersRow+1]...), lines[usersRow:]...)
		newer := append([]string(nil), lines...)
		newer[0] = strings.Replace(newer[0], `"schema":`, `"schema":9`, 1)

		cases := []struct {
			name string
			data []byte
			want error
		}{
			{"обрезан конец", join(lines[:len(lines)-1]), ErrBackupFormat},
			{"обрезан посередине", join(lines[:len(lines)/2]), ErrBackupFormat},
			{"потеряна строка", join(dropRow), ErrBackupFormat},
			{"лишняя строка", join(extraRow), ErrBackupFormat},
			{"мусор", []byte("hello\n"), ErrBackupFormat},
			{"пустой", nil, ErrBackupFormat},
			{"новее бота", join(newer), ErrBackupNewer},
		}
		before := rawDump(t, st)
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := st.CheckBackup(ctx, opener(tc.data)); !errors.Is(err, tc.want) {
					t.Fatalf("CheckBackup: %v, ждали %v", err, tc.want)
				}
				if err := st.RestoreBackup(ctx, opener(tc.data), nil); !errors.Is(err, tc.want) {
					t.Fatalf("RestoreBackup: %v, ждали %v", err, tc.want)
				}
				compareDumps(t, before, rawDump(t, st))
			})
		}
	})
}

// Сбой на последней таблице откатывает всё: база остаётся как была, а не
// «наполовину восстановленной».
func TestBackupRestoreIsAtomic(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		data := string(backupBytes(t, st))
		cleanAll(t, st)
		fillEveryColumn(t, st, 2)
		before := rawDump(t, st)

		// В последней таблице число подменяем текстом — файл устроен верно,
		// но вставка строки падает уже после очистки и вставки остальных.
		last := backupTables[len(backupTables)-1]
		i := strings.Index(data, `{"t":"tb","name":"`+last+`"`)
		j := i + strings.Index(data[i:], "\n[") + 2
		k := j + strings.IndexAny(data[j:], ",]")
		broken := data[:j] + `"не число"` + data[k:]
		if err := st.RestoreBackup(ctx, opener([]byte(broken)), nil); err == nil {
			t.Fatal("испорченная строка восстановилась")
		}
		compareDumps(t, before, rawDump(t, st))
	})
}

// Бэкап старой версии: после вставки строк досчитываются миграции, вышедшие
// позже бэкапа, — как если бы обновилась сама старая база.
func TestBackupReplaysLaterMigrations(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		b := baseOf(t, st)
		if _, err := b.db.ExecContext(ctx, "UPDATE web_users SET email_verified_at = ''"); err != nil {
			t.Fatal(err)
		}
		data := string(backupBytes(t, st))
		cur, err := currentSchema(b.dialect())
		if err != nil {
			t.Fatal(err)
		}
		old := strings.Replace(data, fmt.Sprintf(`"schema":%d`, cur), `"schema":65`, 1)
		if old == data {
			t.Fatal("версия схемы в заголовке не найдена")
		}
		if err := st.RestoreBackup(ctx, opener([]byte(old)), nil); err != nil {
			t.Fatal(err)
		}
		rows, err := b.db.QueryContext(ctx, "SELECT created_at, email_verified_at FROM web_users")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		n := 0
		for rows.Next() {
			var created, verified string
			if err := rows.Scan(&created, &verified); err != nil {
				t.Fatal(err)
			}
			if verified != created {
				t.Fatalf("миграция 0066 не догнала восстановленную строку: %q / %q", created, verified)
			}
			n++
		}
		if n == 0 {
			t.Fatal("нет строк web_users")
		}
		// А бэкап текущей версии те же строки не трогает.
		if err := st.RestoreBackup(ctx, opener([]byte(data)), nil); err != nil {
			t.Fatal(err)
		}
		var cnt int
		if err := b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM web_users WHERE email_verified_at = ''").Scan(&cnt); err != nil {
			t.Fatal(err)
		}
		if cnt == 0 {
			t.Fatal("бэкап текущей версии прогнал старые миграции")
		}
	})
}

// replaceConfig подменяет конфиг в строке settings файла бэкапа.
func replaceConfig(t *testing.T, data []byte, cfg string) []byte {
	t.Helper()
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, `{"t":"tb","name":"settings"`) {
			var cols []string
			var m struct {
				Cols []string `json:"cols"`
			}
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatal(err)
			}
			cols = m.Cols
			var row []any
			if err := json.Unmarshal([]byte(lines[i+1]), &row); err != nil {
				t.Fatal(err)
			}
			for j, c := range cols {
				if c == "config" {
					row[j] = cfg
				}
			}
			raw, _ := json.Marshal(row)
			lines[i+1] = string(raw)
			return []byte(strings.Join(lines, "\n"))
		}
	}
	t.Fatal("в файле нет settings")
	return nil
}

// Конфиг, с которым бот после замены не поднялся бы или открыл мастер
// установки, отвергается ДО замены.
func TestBackupRejectsUnusableConfig(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		data := backupBytes(t, st)
		before := rawDump(t, st)
		noSettings := strings.Replace(string(data), `{"t":"te","rows":1}`, `{"t":"te","rows":0}`, 1)
		if i := strings.Index(noSettings, `{"t":"tb","name":"settings"`); i >= 0 {
			j := i + strings.Index(noSettings[i:], "\n") + 1
			k := j + strings.Index(noSettings[j:], "\n") + 1
			noSettings = noSettings[:j] + noSettings[k:]
		}
		cases := map[string][]byte{
			"массив":         replaceConfig(t, data, `[]`),
			"не настроен":    replaceConfig(t, data, `{"installed":false}`),
			"чужой тип поля": replaceConfig(t, data, `{"installed":"да"}`),
			"без настроек":   []byte(noSettings),
		}
		for name, file := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := st.CheckBackup(ctx, opener(file)); !errors.Is(err, ErrBackupFormat) {
					t.Fatalf("CheckBackup: %v", err)
				}
				if err := st.RestoreBackup(ctx, opener(file), nil); !errors.Is(err, ErrBackupFormat) {
					t.Fatalf("RestoreBackup: %v", err)
				}
				compareDumps(t, before, rawDump(t, st))
			})
		}
	})
}

// Настоящий старый файл: нет таблицы и колонки, которые появились позже.
// Восстановление проходит, недостающее получает значения по умолчанию.
func TestBackupRestoresOlderLayout(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		lines := strings.Split(strings.TrimSuffix(string(backupBytes(t, st)), "\n"), "\n")
		var out []string
		skip := false
		kopIdx := -1
		tables := 0
		for _, l := range lines {
			switch {
			case strings.HasPrefix(l, `{"t":"tb","name":"email_tokens"`):
				skip = true
				continue
			case skip && strings.HasPrefix(l, `{"t":"te"`):
				skip = false
				continue
			case skip:
				continue
			case strings.HasPrefix(l, `{"t":"tb","name":"p2p_requests"`):
				var m backupMeta
				_ = json.Unmarshal([]byte(l), &m)
				kopIdx = slices.Index(m.Cols, "kopecks")
				m.Cols = slices.Delete(m.Cols, kopIdx, kopIdx+1)
				raw, _ := json.Marshal(m)
				out = append(out, string(raw))
				continue
			case kopIdx >= 0 && strings.HasPrefix(l, "["):
				var row []any
				_ = json.Unmarshal([]byte(l), &row)
				row = slices.Delete(row, kopIdx, kopIdx+1)
				raw, _ := json.Marshal(row)
				out = append(out, string(raw))
				continue
			case strings.HasPrefix(l, `{"t":"te"`):
				kopIdx = -1
				tables++
			case strings.HasPrefix(l, `{"t":"end"`):
				l = fmt.Sprintf(`{"t":"end","tables":%d}`, tables)
			}
			out = append(out, l)
		}
		old := []byte(strings.Join(out, "\n") + "\n")
		if err := st.RestoreBackup(ctx, opener(old), nil); err != nil {
			t.Fatal(err)
		}
		b := baseOf(t, st)
		var n, kop int
		_ = b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_tokens").Scan(&n)
		_ = b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM p2p_requests WHERE kopecks <> 0").Scan(&kop)
		if n != 0 || kop != 0 {
			t.Fatalf("старый файл: email_tokens=%d, kopecks≠0 у %d строк", n, kop)
		}
		var p2p int
		_ = b.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM p2p_requests").Scan(&p2p)
		if p2p != 3 {
			t.Fatalf("строки p2p_requests не восстановлены: %d", p2p)
		}
	})
}

// Байты не в UTF-8 возвращаются байт в байт.
func TestBackupKeepsInvalidUTF8(t *testing.T) {
	st := openSQLiteTest(t)
	ctx := context.Background()
	fillEveryColumn(t, st, 1)
	b := baseOf(t, st)
	if _, err := b.db.ExecContext(ctx, "UPDATE payment_log SET detail = ?", "обрез\xd0"); err != nil {
		t.Fatal(err)
	}
	before := rawDump(t, st)
	data := backupBytes(t, st)
	cleanAll(t, st)
	if err := st.RestoreBackup(ctx, opener(data), nil); err != nil {
		t.Fatal(err)
	}
	compareDumps(t, withKind(before, model.DBSQLite), rawDump(t, st))
	var got string
	_ = b.db.QueryRowContext(ctx, "SELECT detail FROM payment_log LIMIT 1").Scan(&got)
	if got != "обрез\xd0" {
		t.Fatalf("байты изменились: %q", got)
	}
}

// Метка SQLite без зоны — это UTC; Postgres в другом поясе не должен её
// сдвигать.
func TestBackupSQLiteTimeToPostgresInOtherZone(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN не задан")
	}
	ctx := context.Background()
	src := openSQLiteTest(t)
	fillEveryColumn(t, src, 7)
	if _, err := baseOf(t, src).db.ExecContext(ctx, "UPDATE media_cache SET updated_at = '2026-11-03 14:07:51'"); err != nil {
		t.Fatal(err)
	}
	dst, err := Open(model.DBPostgres, dsn+"&timezone=Asia/Tokyo", testCrypter(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dst.Close() })
	if err := dst.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cleanPGData(t, dsn)
	if err := dst.RestoreBackup(ctx, opener(backupBytes(t, src)), nil); err != nil {
		t.Fatal(err)
	}
	var got time.Time
	if err := baseOf(t, dst).db.QueryRowContext(ctx, "SELECT updated_at FROM media_cache LIMIT 1").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 3, 14, 7, 51, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("метка сдвинута: %s, ждали %s", got.UTC(), want)
	}
}
