package storage

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"remnabot/internal/model"
)

// Бэкап — построчная выгрузка КАЖДОЙ таблицы со ВСЕМИ колонками, которые
// реально есть в базе. Типизированный снимок (Export) для этого не годится:
// в него попадает только то, что в нём перечислено руками, и любая новая
// колонка или таблица молча выпадала бы из бэкапа.
//
// Формат — строки JSON (по одной на строку файла):
//
//	{"t":"h", ...}               заголовок
//	{"t":"tb","name":..,"cols":[..]}  начало таблицы
//	[v1, v2, ...]                строка таблицы
//	{"t":"te","rows":N}          конец таблицы
//	{"t":"end","tables":K}       конец файла
//
// Конец таблицы и конец файла со счётчиками нужны, чтобы обрезанный файл не
// восстановился «наполовину» без единого слова.
//
// Колонка config таблицы settings пишется расшифрованной: ключ шифрования
// настроек у нового сервера свой, а файл целиком запирается паролем бэкапа.

const (
	backupKind   = "chill-remna-bot-backup"
	backupFormat = 1
	// backupMaxLine — потолок одной строки файла. Самые длинные строки — это
	// конфиг и снимки условий, они на порядки меньше.
	backupMaxLine = 16 << 20
)

// backupTables — все таблицы бота, кроме служебной schema_migrations. Список
// фиксированный (имена таблиц в запрос подставляются только отсюда), его
// полноту сторожит тест: новая таблица без строки здесь валит сборку.
var backupTables = []string{
	"settings",
	"users",
	"web_users",
	"email_tokens",
	"payments",
	"pending_invoices",
	"payment_log",
	"p2p_requests",
	"autopay",
	"invoice_snapshots",
	"purchase_intents",
	"plans",
	"plan_access",
	"promo_codes",
	"promo_redemptions",
	"whitelist",
	"invites",
	"media_cache",
	"torrent_reports",
	"torrent_strikes",
	"screen_state",
}

var (
	// ErrBackupFormat — файл не бэкап бота или повреждён.
	ErrBackupFormat = errors.New("файл не похож на бэкап бота или повреждён")
	// ErrBackupNewer — бэкап снят версией бота новее текущей.
	ErrBackupNewer = errors.New("бэкап снят более новой версией бота — сначала обновите бота")
)

var identRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// BackupInfo — сводка файла бэкапа.
type BackupInfo struct {
	CreatedAt string
	App       string
	DB        string
	Schema    int
	Rows      map[string]int64
}

type backupMeta struct {
	T         string   `json:"t"`
	Kind      string   `json:"kind,omitempty"`
	Format    int      `json:"format,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	App       string   `json:"app,omitempty"`
	DB        string   `json:"db,omitempty"`
	Schema    int      `json:"schema,omitempty"`
	Name      string   `json:"name,omitempty"`
	Cols      []string `json:"cols,omitempty"`
	Rows      *int64   `json:"rows,omitempty"`
	Tables    *int     `json:"tables,omitempty"`
}

type colInfo struct {
	name     string
	isInt    bool
	isTime   bool
	nullable bool
	pg       bool
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (b *base) dialect() string {
	if b.kind == model.DBPostgres {
		return "pg"
	}
	return "sqlite"
}

// tableColumns — колонки таблицы в том виде, в каком они есть в базе.
func (b *base) tableColumns(ctx context.Context, q queryer, table string) ([]colInfo, error) {
	var rows *sql.Rows
	var err error
	if b.kind == model.DBPostgres {
		rows, err = q.QueryContext(ctx,
			"SELECT column_name, data_type, is_nullable FROM information_schema.columns "+
				"WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position", table)
	} else {
		rows, err = q.QueryContext(ctx, `SELECT name, type, "notnull" FROM pragma_table_info(?) ORDER BY cid`, table)
	}
	if err != nil {
		return nil, fmt.Errorf("колонки %s: %w", table, err)
	}
	defer rows.Close()
	var out []colInfo
	for rows.Next() {
		var name, typ string
		var c colInfo
		if b.kind == model.DBPostgres {
			var nullable string
			if err := rows.Scan(&name, &typ, &nullable); err != nil {
				return nil, err
			}
			c.nullable = nullable == "YES"
			c.isTime = strings.HasPrefix(typ, "timestamp")
			c.isInt = typ == "integer" || typ == "bigint" || typ == "smallint"
		} else {
			var notNull int
			if err := rows.Scan(&name, &typ, &notNull); err != nil {
				return nil, err
			}
			c.nullable = notNull == 0
			c.isInt = strings.Contains(strings.ToUpper(typ), "INT")
		}
		if !identRe.MatchString(name) {
			return nil, fmt.Errorf("колонка %s.%q: недопустимое имя", table, name)
		}
		c.name = name
		c.pg = b.kind == model.DBPostgres
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s", errNoTable, table)
	}
	return out, nil
}

// errNoTable — такой таблицы в базе нет.
var errNoTable = errors.New("таблица не найдена")

func quoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = `"` + c + `"`
	}
	return strings.Join(q, ", ")
}

// currentSchema — номер последней миграции, известной этой версии бота.
func currentSchema(dialect string) (int, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations/"+dialect)
	if err != nil {
		return 0, err
	}
	maxV := 0
	for _, e := range entries {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return 0, fmt.Errorf("имя миграции %q: %w", e.Name(), err)
		}
		if v > maxV {
			maxV = v
		}
	}
	return maxV, nil
}

func dumpValue(v any) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		return x, nil
	case int32:
		return int64(x), nil
	case int:
		return int64(x), nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case string:
		return textValue(x), nil
	case []byte:
		return textValue(string(x)), nil
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano), nil
	}
	return nil, fmt.Errorf("неожиданный тип значения %T", v)
}

// textValue — строка для файла. Байты не в UTF-8 JSON молча заменил бы на
// «�», поэтому такие строки пишутся base64 в объекте {"x": ...}.
func textValue(s string) any {
	if utf8.ValidString(s) {
		return s
	}
	return map[string]string{"x": base64.StdEncoding.EncodeToString([]byte(s))}
}

func writeLine(w *bufio.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// Строку длиннее потолка разбор не примет — такой файл не восстановится.
	// Сканеру нужен ещё байт на перевод строки.
	if len(raw) >= backupMaxLine {
		return fmt.Errorf("строка бэкапа длиннее %d МБ", backupMaxLine>>20)
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

// WriteBackup пишет полный бэкап базы в w. Все таблицы читаются одной
// транзакцией: оплата, пришедшая посреди выгрузки, не разъедется между
// таблицами (платёж без пользователя и наоборот).
func (b *base) WriteBackup(ctx context.Context, w io.Writer, app string) (*BackupInfo, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if b.kind == model.DBPostgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := b.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("начало выгрузки: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var schema sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&schema); err != nil {
		return nil, fmt.Errorf("версия схемы: %w", err)
	}
	info := &BackupInfo{
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		App:       app,
		DB:        b.kind,
		Schema:    int(schema.Int64),
		Rows:      map[string]int64{},
	}
	bw := bufio.NewWriterSize(w, 256<<10)
	if err := writeLine(bw, backupMeta{T: "h", Kind: backupKind, Format: backupFormat,
		CreatedAt: info.CreatedAt, App: app, DB: b.kind, Schema: info.Schema}); err != nil {
		return nil, err
	}
	tables := 0
	for _, table := range backupTables {
		n, err := b.dumpTable(ctx, tx, bw, table)
		// Таблицы нет в базе — значит, нет и данных в ней (так выглядит
		// файл, снятый более старой версией). У живого бота все таблицы на
		// месте: это сторожит тест на совпадение списка со схемой.
		if errors.Is(err, errNoTable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info.Rows[table] = n
		tables++
	}
	if err := writeLine(bw, backupMeta{T: "end", Tables: &tables}); err != nil {
		return nil, err
	}
	if err := bw.Flush(); err != nil {
		return nil, err
	}
	return info, nil
}

func (b *base) dumpTable(ctx context.Context, tx *sql.Tx, w *bufio.Writer, table string) (int64, error) {
	cols, err := b.tableColumns(ctx, tx, table)
	if err != nil {
		return 0, err
	}
	names := make([]string, len(cols))
	cfgIdx := -1
	for i, c := range cols {
		names[i] = c.name
		if table == "settings" && c.name == "config" {
			cfgIdx = i
		}
	}
	if err := writeLine(w, backupMeta{T: "tb", Name: table, Cols: names}); err != nil {
		return 0, err
	}
	// #nosec G202 -- имя таблицы из фиксированного backupTables, колонки из схемы базы и проверены identRe
	rows, err := tx.QueryContext(ctx, "SELECT "+quoteCols(names)+" FROM "+table)
	if err != nil {
		return 0, fmt.Errorf("выгрузка %s: %w", table, err)
	}
	defer rows.Close()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	out := make([]any, len(cols))
	var n int64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, fmt.Errorf("выгрузка %s: %w", table, err)
		}
		for i, v := range vals {
			dv, err := dumpValue(v)
			if err != nil {
				return 0, fmt.Errorf("выгрузка %s.%s: %w", table, names[i], err)
			}
			out[i] = dv
		}
		if cfgIdx >= 0 {
			enc, _ := out[cfgIdx].(string)
			plain, err := b.crypter.Decrypt(enc)
			if err != nil {
				return 0, fmt.Errorf("расшифровка настроек: %w", err)
			}
			out[cfgIdx] = string(plain)
		}
		if err := writeLine(w, out); err != nil {
			return 0, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("выгрузка %s: %w", table, err)
	}
	if err := writeLine(w, backupMeta{T: "te", Rows: &n}); err != nil {
		return 0, err
	}
	return n, nil
}

// backupReader разбирает файл бэкапа построчно.
type backupReader struct {
	sc *bufio.Scanner
}

func newBackupReader(r io.Reader) *backupReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), backupMaxLine)
	return &backupReader{sc: sc}
}

// next возвращает очередную строку: метку (meta) или строку таблицы (row).
func (br *backupReader) next() (*backupMeta, []any, error) {
	if !br.sc.Scan() {
		if err := br.sc.Err(); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrBackupFormat, err)
		}
		return nil, nil, io.EOF
	}
	line := br.sc.Bytes()
	if len(line) == 0 {
		return nil, nil, ErrBackupFormat
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	switch line[0] {
	case '{':
		var m backupMeta
		if err := dec.Decode(&m); err != nil {
			return nil, nil, ErrBackupFormat
		}
		return &m, nil, nil
	case '[':
		var row []any
		if err := dec.Decode(&row); err != nil {
			return nil, nil, ErrBackupFormat
		}
		return nil, row, nil
	}
	return nil, nil, ErrBackupFormat
}

// backupVisitor получает таблицы и строки по мере разбора.
type backupVisitor struct {
	table func(name string, cols []string) error
	row   func(row []any) error
	end   func() error
}

// walkBackup проходит файл целиком и проверяет его устройство: заголовок,
// известные таблицы без повторов, длину каждой строки, счётчики строк и
// таблиц. Обрезанный или склеенный файл даёт ErrBackupFormat.
func walkBackup(r io.Reader, curSchema int, v backupVisitor) (*BackupInfo, error) {
	br := newBackupReader(r)
	m, _, err := br.next()
	if err != nil || m == nil || m.T != "h" {
		return nil, ErrBackupFormat
	}
	if m.Kind != backupKind || m.Format != backupFormat {
		return nil, ErrBackupFormat
	}
	if m.Schema > curSchema {
		return nil, ErrBackupNewer
	}
	info := &BackupInfo{CreatedAt: m.CreatedAt, App: m.App, DB: m.DB, Schema: m.Schema, Rows: map[string]int64{}}
	known := map[string]bool{}
	for _, t := range backupTables {
		known[t] = true
	}
	seen := map[string]bool{}
	cur := ""
	ncols := 0
	var count int64
	for {
		m, row, err := br.next()
		if errors.Is(err, io.EOF) {
			// Файл кончился без метки конца — обрезан.
			return nil, ErrBackupFormat
		}
		if err != nil {
			return nil, err
		}
		if row != nil {
			if cur == "" || len(row) != ncols {
				return nil, ErrBackupFormat
			}
			count++
			if v.row != nil {
				if err := v.row(row); err != nil {
					return nil, err
				}
			}
			continue
		}
		switch m.T {
		case "tb":
			if cur != "" || !known[m.Name] || seen[m.Name] || len(m.Cols) == 0 {
				return nil, ErrBackupFormat
			}
			dup := map[string]bool{}
			for _, c := range m.Cols {
				if !identRe.MatchString(c) || dup[c] {
					return nil, ErrBackupFormat
				}
				dup[c] = true
			}
			cur, ncols, count = m.Name, len(m.Cols), 0
			seen[cur] = true
			if v.table != nil {
				if err := v.table(cur, m.Cols); err != nil {
					return nil, err
				}
			}
		case "te":
			if cur == "" || m.Rows == nil || *m.Rows != count {
				return nil, ErrBackupFormat
			}
			info.Rows[cur] = count
			if v.end != nil {
				if err := v.end(); err != nil {
					return nil, err
				}
			}
			cur = ""
		case "end":
			if cur != "" || m.Tables == nil || *m.Tables != len(seen) {
				return nil, ErrBackupFormat
			}
			// После метки конца ничего быть не должно.
			if _, _, err := br.next(); !errors.Is(err, io.EOF) {
				return nil, ErrBackupFormat
			}
			if info.Rows["settings"] > 1 {
				return nil, ErrBackupFormat
			}
			return info, nil
		default:
			return nil, ErrBackupFormat
		}
	}
}

// CheckBackup проверяет файл целиком и сверяет его с текущей схемой: каждая
// колонка файла должна быть в базе. Ничего не меняет.
func (b *base) CheckBackup(ctx context.Context, open func() (io.Reader, error)) (*BackupInfo, error) {
	return b.checkBackup(ctx, open, true)
}

// checkBackup — проверка файла. strict требует годный конфиг настроенного
// бота: восстановление из админки без него оставило бы бота неработающим. Перенос
// базы при смене движка идёт и на ненастроенном боте — там strict выключен.
func (b *base) checkBackup(ctx context.Context, open func() (io.Reader, error), strict bool) (*BackupInfo, error) {
	cur, err := currentSchema(b.dialect())
	if err != nil {
		return nil, err
	}
	schemaCols := map[string]map[string]bool{}
	for _, t := range backupTables {
		cols, err := b.tableColumns(ctx, b.db, t)
		if err != nil {
			return nil, err
		}
		set := map[string]bool{}
		for _, c := range cols {
			set[c.name] = true
		}
		schemaCols[t] = set
	}
	r, err := open()
	if err != nil {
		return nil, err
	}
	cfgIdx := -1
	hasCfg := false
	info, err := walkBackup(r, cur, backupVisitor{
		table: func(name string, cols []string) error {
			cfgIdx = -1
			for i, c := range cols {
				if !schemaCols[name][c] {
					return fmt.Errorf("%w: в базе нет колонки %s.%s", ErrBackupNewer, name, c)
				}
				if name == "settings" && c == "config" {
					cfgIdx = i
				}
			}
			return nil
		},
		row: func(row []any) error {
			if cfgIdx >= 0 {
				s, ok := row[cfgIdx].(string)
				if !ok || !configParses(s) || strict && !configUsable(s) {
					return ErrBackupFormat
				}
				hasCfg = true
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	// Без годного конфига бот после замены не поднялся бы или открыл мастер
	// первичной установки — а откатиться через бота уже нельзя.
	if strict && !hasCfg {
		return nil, ErrBackupFormat
	}
	return info, nil
}

// configParses — конфиг из файла — объект, который эта версия разбирает.
func configParses(s string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil || m == nil {
		return false
	}
	var cfg model.BotConfig
	return json.Unmarshal([]byte(s), &cfg) == nil
}

// configUsable — конфиг из файла разбирается этой версией бота и описывает
// настроенного бота.
func configUsable(s string) bool {
	var cfg model.BotConfig
	return configParses(s) && json.Unmarshal([]byte(s), &cfg) == nil && cfg.Installed
}

// restoredConfig — конфиг из файла в том виде, в каком он ляжет в базу:
// движок — той базы, куда восстанавливаем (бэкап SQLite, поднятый на
// Postgres, иначе показывал бы в статусе «sqlite»), плюс правка вызывающего.
// Файл не новее бота (это проверено раньше), так что разбор в BotConfig
// ничего из него не теряет.
func restoredConfig(cfgJSON, kind string, patch func(*model.BotConfig)) ([]byte, error) {
	if !configParses(cfgJSON) {
		return nil, ErrBackupFormat
	}
	var cfg model.BotConfig
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		return nil, ErrBackupFormat
	}
	cfg.DBKind = kind
	if patch != nil {
		patch(&cfg)
	}
	return json.Marshal(&cfg)
}

func restoreValue(v any, c colInfo) (any, error) {
	switch x := v.(type) {
	case nil:
		if c.nullable {
			return nil, nil
		}
		// Колонка допускает NULL в одной базе и нет в другой (метки времени
		// согласия и триала): в SQLite «нет значения» — пустая строка.
		if c.isInt {
			return int64(0), nil
		}
		return "", nil
	case json.Number:
		if !c.isInt {
			return nil, fmt.Errorf("колонка %s: ожидался текст, в файле число", c.name)
		}
		n, err := x.Int64()
		if err != nil {
			return nil, fmt.Errorf("колонка %s: %w", c.name, err)
		}
		return n, nil
	case map[string]any:
		enc, ok := x["x"].(string)
		raw, err := base64.StdEncoding.DecodeString(enc)
		if !ok || len(x) != 1 || err != nil || c.isInt {
			return nil, fmt.Errorf("колонка %s: неожиданное значение", c.name)
		}
		if c.pg {
			// Postgres байты не в UTF-8 не хранит вовсе.
			return strings.ToValidUTF8(string(raw), "\uFFFD"), nil
		}
		return string(raw), nil
	case string:
		if c.isInt {
			return nil, fmt.Errorf("колонка %s: ожидалось число, в файле текст", c.name)
		}
		if c.isTime {
			if x == "" {
				if c.nullable {
					return nil, nil
				}
				return nil, fmt.Errorf("колонка %s: пустая метка времени", c.name)
			}
			// SQLite пишет datetime('now') без зоны, но в UTC. Postgres
			// прочёл бы такую метку в поясе сервера и сдвинул время.
			if t, err := time.Parse("2006-01-02 15:04:05", x); err == nil {
				return t.UTC().Format(time.RFC3339), nil
			}
		}
		return x, nil
	}
	return nil, fmt.Errorf("колонка %s: неожиданное значение %T", c.name, v)
}

// RestoreBackup ПОЛНОСТЬЮ заменяет данные базы содержимым файла. Всё идёт
// одной транзакцией: при любой ошибке база остаётся как была. Бэкап более
// старой версии после вставки догоняется миграциями, вышедшими позже него, —
// ровно так, как обновилась бы сама старая база.
//
// patch правит восстановленный конфиг перед записью — в той же транзакции,
// что и замена (метка «база восстановлена» не может разойтись с самой заменой).
func (b *base) RestoreBackup(ctx context.Context, open func() (io.Reader, error), patch func(*model.BotConfig)) error {
	return b.restoreBackup(ctx, open, patch, true)
}

func (b *base) restoreBackup(ctx context.Context, open func() (io.Reader, error), patch func(*model.BotConfig), strict bool) error {
	info, err := b.checkBackup(ctx, open, strict)
	if err != nil {
		return err
	}
	r, err := open()
	if err != nil {
		return err
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, t := range backupTables {
		// #nosec G202 -- имя таблицы из фиксированного backupTables
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
			return fmt.Errorf("очистка %s: %w", t, err)
		}
	}

	var stmt *sql.Stmt
	var targets []colInfo
	cfgIdx := -1
	args := []any{}
	closeStmt := func() {
		if stmt != nil {
			_ = stmt.Close()
			stmt = nil
		}
	}
	defer closeStmt()
	_, err = walkBackup(r, int(^uint(0)>>1), backupVisitor{
		table: func(name string, cols []string) error {
			all, err := b.tableColumns(ctx, tx, name)
			if err != nil {
				return err
			}
			byName := map[string]colInfo{}
			for _, c := range all {
				byName[c.name] = c
			}
			targets = targets[:0]
			cfgIdx = -1
			ph := make([]string, len(cols))
			for i, c := range cols {
				ci, ok := byName[c]
				if !ok {
					return fmt.Errorf("%w: в базе нет колонки %s.%s", ErrBackupNewer, name, c)
				}
				targets = append(targets, ci)
				ph[i] = b.ph(i + 1)
				if name == "settings" && c == "config" {
					cfgIdx = i
				}
			}
			// #nosec G202 -- имя таблицы проверено по backupTables, колонки — по схеме базы, значения идут параметрами
			s, err := tx.PrepareContext(ctx, "INSERT INTO "+name+" ("+quoteCols(cols)+") VALUES ("+strings.Join(ph, ", ")+")")
			if err != nil {
				return fmt.Errorf("вставка в %s: %w", name, err)
			}
			stmt = s
			args = make([]any, len(cols))
			return nil
		},
		row: func(row []any) error {
			for i, v := range row {
				if i == cfgIdx {
					s, _ := v.(string)
					plain, err := restoredConfig(s, b.kind, patch)
					if err != nil {
						return err
					}
					enc, err := b.crypter.Encrypt(plain)
					if err != nil {
						return err
					}
					args[i] = enc
					continue
				}
				rv, err := restoreValue(v, targets[i])
				if err != nil {
					return err
				}
				args[i] = rv
			}
			if _, err := stmt.ExecContext(ctx, args...); err != nil {
				return fmt.Errorf("вставка строки: %w", err)
			}
			return nil
		},
		end: func() error {
			closeStmt()
			return nil
		},
	})
	if err != nil {
		return err
	}
	if err := b.replayMigrationsTx(ctx, tx, info.Schema); err != nil {
		return err
	}
	return tx.Commit()
}

// replayMigrationsTx заново прогоняет миграции новее after внутри tx. Изменения
// схемы в них уже применены и пропускаются, а правки данных (досчёт, очистка
// кэша) ложатся на восстановленные строки.
func (b *base) replayMigrationsTx(ctx context.Context, tx *sql.Tx, after int) error {
	dialect := b.dialect()
	dir := "migrations/" + dialect
	entries, err := fs.ReadDir(migrationsFS, dir)
	if err != nil {
		return err
	}
	type mig struct {
		v    int
		name string
	}
	var list []mig
	for _, e := range entries {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return err
		}
		if v > after {
			list = append(list, mig{v, e.Name()})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v < list[j].v })
	for _, m := range list {
		stmt, err := migrationsFS.ReadFile(dir + "/" + m.name)
		if err != nil {
			return err
		}
		if dialect == "pg" {
			if _, err := tx.ExecContext(ctx, "SAVEPOINT rb_replay"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, string(stmt)); err != nil {
			if !isAlreadyApplied(err) {
				return fmt.Errorf("миграция %s: %w", m.name, err)
			}
			if dialect == "pg" {
				if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT rb_replay"); err != nil {
					return err
				}
			}
			continue
		}
		if dialect == "pg" {
			if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT rb_replay"); err != nil {
				return err
			}
		}
	}
	return nil
}
