package storage

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"remnabot/internal/model"
)

func TestDumpValue(t *testing.T) {
	tm := time.Date(2026, 11, 3, 14, 0, 0, 5, time.FixedZone("X", 3*3600))
	cases := []struct {
		in   any
		want any
	}{
		{nil, nil},
		{int64(7), int64(7)},
		{int32(7), int64(7)},
		{7, int64(7)},
		{true, int64(1)},
		{false, int64(0)},
		{"текст", "текст"},
		{[]byte("байты"), "байты"},
		{tm, "2026-11-03T11:00:00.000000005Z"},
	}
	for _, tc := range cases {
		got, err := dumpValue(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("dumpValue(%#v) = %#v, %v", tc.in, got, err)
		}
	}
	if m, _ := dumpValue("\xff"); m == nil {
		t.Fatal("битый UTF-8 не упакован")
	} else if _, ok := m.(map[string]string); !ok {
		t.Fatalf("битый UTF-8: %#v", m)
	}
	if _, err := dumpValue(1.5); err == nil {
		t.Fatal("неизвестный тип принят молча")
	}
}

func TestRestoreValue(t *testing.T) {
	intCol := colInfo{name: "n", isInt: true}
	textCol := colInfo{name: "s"}
	nullTime := colInfo{name: "t", isTime: true, nullable: true, pg: true}
	strictTime := colInfo{name: "t", isTime: true, pg: true}
	pgText := colInfo{name: "s", pg: true}
	ok := []struct {
		v    any
		c    colInfo
		want any
	}{
		{nil, colInfo{nullable: true}, nil},
		{nil, intCol, int64(0)},
		{nil, textCol, ""},
		{json.Number("42"), intCol, int64(42)},
		{"x", textCol, "x"},
		{"", nullTime, nil},
		{"2026-11-03 14:07:51", nullTime, "2026-11-03T14:07:51Z"},
		{"2026-11-03T14:07:51Z", nullTime, "2026-11-03T14:07:51Z"},
		{map[string]any{"x": "/w=="}, textCol, "\xff"},
		{map[string]any{"x": "/w=="}, pgText, "�"},
	}
	for _, tc := range ok {
		got, err := restoreValue(tc.v, tc.c)
		if err != nil || got != tc.want {
			t.Fatalf("restoreValue(%#v, %+v) = %#v, %v", tc.v, tc.c, got, err)
		}
	}
	bad := []struct {
		v any
		c colInfo
	}{
		{json.Number("42"), textCol},
		{json.Number("1.5"), intCol},
		{"x", intCol},
		{"", strictTime},
		{map[string]any{"x": "не base64"}, textCol},
		{map[string]any{"y": "AA=="}, textCol},
		{map[string]any{"x": "AA==", "y": "1"}, textCol},
		{map[string]any{"x": "AA=="}, intCol},
		{true, textCol},
	}
	for _, tc := range bad {
		if _, err := restoreValue(tc.v, tc.c); err == nil {
			t.Fatalf("restoreValue(%#v, %+v) принят", tc.v, tc.c)
		}
	}
}

func TestWriteLineLimit(t *testing.T) {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	if err := writeLine(w, []any{strings.Repeat("я", backupMaxLine/2+1)}); err == nil {
		t.Fatal("строка длиннее потолка записана")
	}
	if err := writeLine(w, []any{"ok"}); err != nil {
		t.Fatal(err)
	}
}

// Устройство файла проверяется целиком: каждая поломка — ErrBackupFormat.
func TestWalkBackupStructure(t *testing.T) {
	h := `{"t":"h","kind":"chill-remna-bot-backup","format":1,"schema":1}`
	tb := `{"t":"tb","name":"users","cols":["telegram_id"]}`
	te := `{"t":"te","rows":1}`
	end := `{"t":"end","tables":1}`
	good := []string{h, tb, `[1]`, te, end}
	if _, err := walkBackup(strings.NewReader(strings.Join(good, "\n")+"\n"), 1, backupVisitor{}); err != nil {
		t.Fatalf("исправный файл: %v", err)
	}
	cases := map[string][]string{
		"чужой формат":           {`{"t":"h","kind":"x","format":1}`, end},
		"другая версия формата":  {`{"t":"h","kind":"chill-remna-bot-backup","format":2}`, end},
		"без заголовка":          {tb, `[1]`, te, end},
		"строка вне таблицы":     {h, `[1]`, end},
		"строка не той длины":    {h, tb, `[1,2]`, te, end},
		"неизвестная таблица":    {h, `{"t":"tb","name":"evil","cols":["a"]}`, te, end},
		"таблица дважды":         {h, tb, `[1]`, te, tb, `[1]`, te, `{"t":"end","tables":2}`},
		"таблица без колонок":    {h, `{"t":"tb","name":"users","cols":[]}`, `{"t":"te","rows":0}`, end},
		"колонка дважды":         {h, `{"t":"tb","name":"users","cols":["a","a"]}`, `{"t":"te","rows":0}`, end},
		"колонка с кавычкой":     {h, `{"t":"tb","name":"users","cols":["a\"; DROP"]}`, `{"t":"te","rows":0}`, end},
		"вложенная таблица":      {h, tb, `{"t":"tb","name":"payments","cols":["id"]}`},
		"конец таблицы без нач.": {h, te, end},
		"конец при откр. табл.":  {h, tb, `[1]`, end},
		"неверное число таблиц":  {h, tb, `[1]`, te, `{"t":"end","tables":5}`},
		"мусор после конца":      {h, tb, `[1]`, te, end, `[1]`},
		"неизвестная метка":      {h, `{"t":"zz"}`, end},
		"пустая строка":          {h, ``, end},
		"не JSON":                {h, `{oops`, end},
		"битая строка таблицы":   {h, tb, `[1,`, te, end},
		"две строки настроек":    {h, `{"t":"tb","name":"settings","cols":["id"]}`, `[1]`, `[2]`, `{"t":"te","rows":2}`, end},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := walkBackup(strings.NewReader(strings.Join(lines, "\n")+"\n"), 1, backupVisitor{})
			if !errors.Is(err, ErrBackupFormat) {
				t.Fatalf("ждали ErrBackupFormat, получили %v", err)
			}
		})
	}
	// Ошибка посетителя прерывает разбор и возвращается как есть.
	stop := errors.New("стоп")
	_, err := walkBackup(strings.NewReader(strings.Join(good, "\n")+"\n"), 1, backupVisitor{row: func([]any) error { return stop }})
	if !errors.Is(err, stop) {
		t.Fatalf("ошибка посетителя: %v", err)
	}
}

// Колонка из файла, которой нет в базе, — файл новее бота.
func TestCheckBackupUnknownColumn(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		data := string(backupBytes(t, st))
		data = strings.Replace(data, `"cols":["telegram_id",`, `"cols":["future_col",`, 1)
		if _, err := st.CheckBackup(ctx, opener([]byte(data))); !errors.Is(err, ErrBackupNewer) {
			t.Fatalf("CheckBackup: %v", err)
		}
		if _, err := baseOf(t, st).tableColumns(ctx, baseOf(t, st).db, "no_such_table"); err == nil {
			t.Fatal("несуществующая таблица дала колонки")
		}
	})
}

// Закрытая база и сбой открытия файла — ошибки, а не паника и не «готово».
func TestBackupStoreErrors(t *testing.T) {
	st := openSQLiteTest(t)
	ctx := context.Background()
	fillEveryColumn(t, st, 1)
	data := backupBytes(t, st)
	openErr := errors.New("не открылся")
	failing := func() (io.Reader, error) { return nil, openErr }
	if _, err := st.CheckBackup(ctx, failing); !errors.Is(err, openErr) {
		t.Fatalf("CheckBackup: %v", err)
	}
	if err := st.RestoreBackup(ctx, failing, nil); !errors.Is(err, openErr) {
		t.Fatalf("RestoreBackup: %v", err)
	}
	// Второй проход (сама замена) не открылся — база не тронута.
	before := rawDump(t, st)
	calls := 0
	second := func() (io.Reader, error) {
		calls++
		if calls > 1 {
			return nil, openErr
		}
		return bytes.NewReader(data), nil
	}
	if err := st.RestoreBackup(ctx, second, nil); !errors.Is(err, openErr) {
		t.Fatalf("RestoreBackup второй проход: %v", err)
	}
	compareDumps(t, before, rawDump(t, st))
	_ = st.Close()
	if _, err := st.WriteBackup(ctx, io.Discard, "x"); err == nil {
		t.Fatal("WriteBackup на закрытой базе")
	}
	if _, err := st.CheckBackup(ctx, opener(data)); err == nil {
		t.Fatal("CheckBackup на закрытой базе")
	}
}

// Самая длинная строка, которую примет разбор, записывается; на байт длиннее
// — нет.
func TestWriteLineBoundary(t *testing.T) {
	sizeOf := func(n int) int { raw, _ := json.Marshal([]any{strings.Repeat("a", n)}); return len(raw) }
	pad := sizeOf(0)
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	if err := writeLine(w, []any{strings.Repeat("a", backupMaxLine-1-pad)}); err != nil {
		t.Fatalf("строка на границе: %v", err)
	}
	_ = w.Flush()
	if _, row, err := newBackupReader(&b).next(); err != nil || row == nil {
		t.Fatalf("строка на границе не прочиталась: %v", err)
	}
	if err := writeLine(w, []any{strings.Repeat("a", backupMaxLine-pad)}); err == nil {
		t.Fatal("строка длиной в потолок записана")
	}
}

// migrateUpTo — база на старой схеме: применены миграции до max.
func migrateUpTo(t *testing.T, b *base, max int) {
	t.Helper()
	ctx := context.Background()
	if _, err := b.db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	dir := "migrations/" + b.dialect()
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		v, _ := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if v > max {
			continue
		}
		stmt, _ := migrationsFS.ReadFile(dir + "/" + e.Name())
		if err := applyMigration(ctx, b, b.dialect(), e.Name(), v, string(stmt)); err != nil {
			t.Fatal(err)
		}
	}
}

// Настоящий старый файл: снят с базы, которая жила на схеме 65. После
// восстановления в текущую версию строки получают досчёт поздних миграций, а
// колонки, которых тогда не было, — значения по умолчанию.
func TestBackupFromRealOldSchema(t *testing.T) {
	run := func(t *testing.T, old, cur Storage) {
		ctx := context.Background()
		ob := baseOf(t, old)
		migrateUpTo(t, ob, 65)
		cols, err := ob.tableColumns(ctx, ob.db, "p2p_requests")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cols {
			if c.name == "kopecks" {
				t.Fatal("старая схема уже с колонкой kopecks")
			}
		}
		fillEveryColumn(t, old, 1)
		if _, err := ob.db.ExecContext(ctx, "UPDATE web_users SET email_verified_at = ''"); err != nil {
			t.Fatal(err)
		}
		data := backupBytes(t, old)
		if !strings.Contains(string(data), `"schema":65`) {
			t.Fatal("в заголовке не та версия схемы")
		}
		fillEveryColumn(t, cur, 2)
		if err := cur.RestoreBackup(ctx, opener(data), nil); err != nil {
			t.Fatal(err)
		}
		cb := baseOf(t, cur)
		var empty, kop, p2p int
		_ = cb.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM web_users WHERE email_verified_at = ''").Scan(&empty)
		_ = cb.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM p2p_requests WHERE kopecks <> 0").Scan(&kop)
		_ = cb.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM p2p_requests").Scan(&p2p)
		if empty != 0 || kop != 0 || p2p != 3 {
			t.Fatalf("после старого файла: пустых подтверждений %d, kopecks≠0 %d, заявок %d", empty, kop, p2p)
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		c := testCrypter(t)
		old, err := Open(model.DBSQLite, filepath.Join(t.TempDir(), "old.db"), c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = old.Close() })
		run(t, old, openSQLiteTest(t))
	})
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		if _, err := admin.Exec("DROP SCHEMA IF EXISTS bk_old CASCADE"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec("CREATE SCHEMA bk_old"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA IF EXISTS bk_old CASCADE") })
		old, err := Open(model.DBPostgres, dsn+"&search_path=bk_old", testCrypter(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = old.Close() })
		cur, err := Open(model.DBPostgres, dsn, testCrypter(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cur.Close() })
		if err := cur.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		cleanPGData(t, dsn)
		run(t, old, cur)
	})
}

// Поля конфига, которых эта версия не знает (файл с более свежего бота той
// же схемы), восстанавливаются как были.
func TestBackupKeepsUnknownConfigFields(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		b := baseOf(t, st)
		plain := `{"installed":true,"language":"ru","future_feature":{"x":1},"db_kind":"sqlite"}`
		enc, _ := b.crypter.Encrypt([]byte(plain))
		if _, err := b.db.ExecContext(ctx, "UPDATE settings SET config = "+b.ph(1), enc); err != nil {
			t.Fatal(err)
		}
		data := backupBytes(t, st)
		if err := st.RestoreBackup(ctx, opener(data), nil); err != nil {
			t.Fatal(err)
		}
		var got string
		_ = b.db.QueryRowContext(ctx, "SELECT config FROM settings WHERE id = 1").Scan(&got)
		raw, _ := b.crypter.Decrypt(got)
		if !strings.Contains(string(raw), `"future_feature":{"x":1}`) || !strings.Contains(string(raw), `"db_kind":"`+st.Kind()+`"`) {
			t.Fatalf("конфиг после восстановления: %s", raw)
		}
	})
}

// Метка восстановления ложится в состояние бэкапа той же транзакцией, а
// настройки бэкапа из файла сохраняются.
func TestRestoreWritesGuardIntoState(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		fillEveryColumn(t, st, 1)
		data := backupBytes(t, st)
		_ = st.SaveBackupState(ctx, &model.BackupConfig{Password: "другой"})
		g := &model.RestoreGuard{At: "2026-11-03T12:00:00Z", BackupAt: "2026-11-02T12:00:00Z", Chat: 100, AutoPayHeld: true}
		if err := st.RestoreBackup(ctx, opener(data), g); err != nil {
			t.Fatal(err)
		}
		bs, ok, err := st.LoadBackupState(ctx)
		if err != nil || !ok || bs.Password != "пароль-1" || bs.Hours != 6 || bs.Guard == nil || !reflect.DeepEqual(*bs.Guard, *g) {
			t.Fatalf("состояние после восстановления: %+v %v %v", bs, ok, err)
		}
	})
}

// Файл версии, где настройки бэкапа жили в конфиге: они переезжают в свою
// таблицу, из конфига уходят.
func TestRestoreLegacyStateFromConfig(t *testing.T) {
	st := openSQLiteTest(t)
	ctx := context.Background()
	fillEveryColumn(t, st, 1)
	data := string(backupBytes(t, st))
	// Убираем таблицу состояния и кладём настройки в конфиг, как было раньше.
	i := strings.Index(data, `{"t":"tb","name":"backup_state"`)
	j := i + strings.Index(data[i:], `{"t":"te"`)
	k := j + strings.Index(data[j:], "\n") + 1
	data = data[:i] + data[k:]
	data = strings.Replace(data, `"tables":22}`, `"tables":21}`, 1)
	file := replaceConfig(t, []byte(data), `{"installed":true,"backup":{"hours":12,"password":"старый","chat_id":-1000000000077}}`)
	g := &model.RestoreGuard{At: "2026-11-03T12:00:00Z", Chat: 100}
	if err := st.RestoreBackup(ctx, opener(file), g); err != nil {
		t.Fatal(err)
	}
	bs, ok, _ := st.LoadBackupState(ctx)
	if !ok || bs.Password != "старый" || bs.Hours != 12 || bs.ChatID != -1000000000077 || bs.Guard == nil {
		t.Fatalf("состояние из старого файла: %+v", bs)
	}
	cfg, _, _ := st.LoadConfig(ctx)
	if cfg.Backup != nil {
		t.Fatal("настройки бэкапа остались в конфиге")
	}
}

func TestBackupStateRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if _, ok, err := st.LoadBackupState(ctx); ok || err != nil {
			t.Fatalf("пустая база: ok=%v err=%v", ok, err)
		}
		want := &model.BackupConfig{Hours: 2, Password: "п", BotToken: "1:x", BotName: "b", LastAt: "2026-11-03T10:00:00Z"}
		if err := st.SaveBackupState(ctx, want); err != nil {
			t.Fatal(err)
		}
		want.Hours = 4
		_ = st.SaveBackupState(ctx, want)
		got, ok, err := st.LoadBackupState(ctx)
		if err != nil || !ok || got.Hours != 4 || got.Password != "п" || got.BotToken != "1:x" {
			t.Fatalf("состояние: %+v %v %v", got, ok, err)
		}
	})
}

// hookWriter выполняет действие на первой записи — посреди выгрузки.
type hookWriter struct {
	io.Writer
	once func()
}

func (h *hookWriter) Write(p []byte) (int, error) {
	if h.once != nil {
		f := h.once
		h.once = nil
		f()
	}
	return h.Writer.Write(p)
}

// Postgres: выгрузка — один снимок. Строка, записанная посреди выгрузки, в
// файл не попадает (иначе таблицы разъехались бы между собой).
func TestBackupPostgresSnapshotIsConsistent(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN не задан")
	}
	ctx := context.Background()
	st, err := Open(model.DBPostgres, dsn, testCrypter(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.Migrate(ctx)
	cleanPGData(t, dsn)
	fillEveryColumn(t, st, 1)
	other, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var buf bytes.Buffer
	w := &hookWriter{Writer: &buf, once: func() {
		if _, err := other.Exec("INSERT INTO screen_state (chat_id, msg_id) VALUES (999, 1)"); err != nil {
			t.Error(err)
		}
	}}
	if _, err := st.WriteBackup(ctx, w, "test"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "[999,1]") {
		t.Fatal("строка, записанная посреди выгрузки, попала в файл")
	}
}
