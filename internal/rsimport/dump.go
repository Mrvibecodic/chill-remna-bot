// Package rsimport читает дамп базы бота remnashop (github.com/snoups/remnashop)
// и превращает его в записи, понятные нашему хранилищу.
//
// Источник — обычный plain-текстовый дамп pg_dump: ровно то, что отдаёт сам
// remnashop кнопкой «Бэкап» (файл db_backup_*.sql). Поддерживаются оба
// варианта, которые умеет писать pg_dump: блоки COPY ... FROM stdin (формат по
// умолчанию) и построчные INSERT INTO (ключ --inserts / --column-inserts).
package rsimport

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Table — одна распарсенная таблица дампа. Значение nil означает SQL NULL.
type Table struct {
	Name string
	Cols []string
	Rows [][]*string
}

// Col возвращает индекс колонки или -1, если её нет.
func (t *Table) Col(name string) int {
	for i, c := range t.Cols {
		if c == name {
			return i
		}
	}
	return -1
}

// Limits ограничивают аппетит парсера: дамп приходит от админа, но это не повод
// позволять ему съесть всю память процесса.
const (
	maxRowsPerTable = 1_000_000
	maxLineBytes    = 8 << 20
	// maxCells — потолок на общее число разобранных значений. Текст дампа
	// разворачивается в память в несколько раз, а бот живёт на том же
	// сервере, что и всё остальное: лучше честная ошибка, чем OOM.
	maxCells = 8_000_000
)

var errTooManyRows = errors.New("в дампе слишком много данных — импортируйте его частями или обратитесь к разработчику")

// ParseDump разбирает дамп и возвращает только запрошенные таблицы (имена — без
// схемы: "users", "subscriptions", ...). Файл может быть сжат gzip.
func ParseDump(r io.Reader, want ...string) (map[string]*Table, error) {
	wanted := map[string]bool{}
	for _, w := range want {
		wanted[w] = true
	}

	br := bufio.NewReaderSize(r, 1<<20)
	if head, err := br.Peek(2); err == nil && head[0] == 0x1f && head[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer zr.Close()
		br = bufio.NewReaderSize(zr, 1<<20)
	}

	out := map[string]*Table{}
	var copyInto *Table // nil, когда мы не внутри COPY-блока
	copySkip := false
	cells := 0

	for {
		line, err := readLine(br)
		if err != nil && line == "" {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		if copyInto != nil || copySkip {
			if line == `\.` {
				copyInto, copySkip = nil, false
				continue
			}
			if copySkip {
				continue
			}
			cells += len(copyInto.Cols)
			if len(copyInto.Rows) >= maxRowsPerTable || cells > maxCells {
				return nil, errTooManyRows
			}
			copyInto.Rows = append(copyInto.Rows, parseCopyRow(line, len(copyInto.Cols)))
			continue
		}

		trimmed := strings.TrimSpace(line)
		switch {
		case hasPrefixFold(trimmed, "COPY "):
			name, cols, ok := parseCopyHeader(trimmed)
			if !ok {
				continue
			}
			if !wanted[name] {
				copySkip = true
				continue
			}
			t := out[name]
			if t == nil {
				t = &Table{Name: name, Cols: cols}
				out[name] = t
			}
			copyInto = t
		case hasPrefixFold(trimmed, "INSERT INTO "):
			stmt := trimmed
			// pg_dump обычно пишет INSERT одной строкой, но многострочные
			// значения (переносы внутри кавычек) тоже встречаются.
			for !strings.HasSuffix(stmt, ";") {
				next, err := readLine(br)
				if next == "" && err != nil {
					break
				}
				stmt += "\n" + next
			}
			name, cols, rows, ok := parseInsert(stmt)
			if !ok || !wanted[name] {
				continue
			}
			t := out[name]
			if t == nil {
				t = &Table{Name: name, Cols: cols}
				out[name] = t
			}
			if len(t.Cols) == 0 {
				t.Cols = cols
			}
			cells += len(rows) * max(len(t.Cols), 1)
			if len(t.Rows)+len(rows) > maxRowsPerTable || cells > maxCells {
				return nil, errTooManyRows
			}
			t.Rows = append(t.Rows, rows...)
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
	}

	if len(out) == 0 {
		return nil, errors.New("в файле не найдено ни одной таблицы remnashop — это точно дамп его базы?")
	}
	return out, nil
}

// readLine читает строку целиком (без ограничения bufio.Scanner), но не длиннее
// maxLineBytes — остаток слишком длинной строки отбрасывается.
func readLine(br *bufio.Reader) (string, error) {
	var sb strings.Builder
	for {
		chunk, isPrefix, err := br.ReadLine()
		if sb.Len() < maxLineBytes {
			sb.Write(chunk)
		}
		if err != nil {
			return sb.String(), err
		}
		if !isPrefix {
			return sb.String(), nil
		}
	}
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// parseCopyHeader разбирает `COPY public.users (id, telegram_id) FROM stdin;`.
func parseCopyHeader(line string) (name string, cols []string, ok bool) {
	rest := strings.TrimSpace(line[len("COPY "):])
	open := strings.Index(rest, "(")
	close := strings.LastIndex(rest, ")")
	if open < 0 || close < open {
		return "", nil, false
	}
	name = normalizeIdent(strings.TrimSpace(rest[:open]))
	for _, c := range strings.Split(rest[open+1:close], ",") {
		cols = append(cols, normalizeIdent(strings.TrimSpace(c)))
	}
	return name, cols, name != "" && len(cols) > 0
}

// normalizeIdent убирает схему и кавычки: `public."users"` → `users`.
func normalizeIdent(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"`)
	return strings.ToLower(s)
}

// parseCopyRow разбирает строку COPY-блока (текстовый формат: значения через
// табуляцию, `\N` = NULL).
func parseCopyRow(line string, want int) []*string {
	parts := strings.Split(line, "\t")
	row := make([]*string, 0, len(parts))
	for _, p := range parts {
		if p == `\N` {
			row = append(row, nil)
			continue
		}
		v := unescapeCopy(p)
		row = append(row, &v)
	}
	for len(row) < want {
		row = append(row, nil)
	}
	return row
}

func unescapeCopy(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			sb.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			sb.WriteByte('\n')
		case 't':
			sb.WriteByte('\t')
		case 'r':
			sb.WriteByte('\r')
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'v':
			sb.WriteByte('\v')
		case '\\':
			sb.WriteByte('\\')
		default:
			// Восьмеричная последовательность \ooo.
			if s[i] >= '0' && s[i] <= '7' {
				val := 0
				n := 0
				for n < 3 && i < len(s) && s[i] >= '0' && s[i] <= '7' {
					val = val*8 + int(s[i]-'0')
					i++
					n++
				}
				i--
				sb.WriteByte(byte(val))
				continue
			}
			sb.WriteByte(s[i])
		}
	}
	return sb.String()
}

// parseInsert разбирает `INSERT INTO public.users (a, b) VALUES (1, 'x'), (2, NULL);`
// Колонки могут отсутствовать (pg_dump --inserts без --column-inserts) — тогда
// вернётся пустой список, и вызывающая сторона возьмёт колонки из COPY-блока
// или пропустит таблицу.
func parseInsert(stmt string) (name string, cols []string, rows [][]*string, ok bool) {
	rest := strings.TrimSpace(stmt[len("INSERT INTO "):])
	rest = strings.TrimSuffix(strings.TrimSpace(rest), ";")

	vi := indexFoldOutsideQuotes(rest, " VALUES ")
	if vi < 0 {
		return "", nil, nil, false
	}
	head := strings.TrimSpace(rest[:vi])
	tail := strings.TrimSpace(rest[vi+len(" VALUES "):])

	if open := strings.Index(head, "("); open >= 0 && strings.HasSuffix(head, ")") {
		for _, c := range strings.Split(head[open+1:len(head)-1], ",") {
			cols = append(cols, normalizeIdent(strings.TrimSpace(c)))
		}
		head = strings.TrimSpace(head[:open])
	}
	name = normalizeIdent(head)
	if name == "" {
		return "", nil, nil, false
	}

	for _, group := range splitValueGroups(tail) {
		rows = append(rows, parseValueList(group))
	}
	return name, cols, rows, len(rows) > 0
}

// indexFoldOutsideQuotes ищет подстроку без учёта регистра, игнорируя вхождения
// внутри одинарных кавычек.
func indexFoldOutsideQuotes(s, sub string) int {
	inQuote := false
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i] == '\'' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// splitValueGroups делит `(1, 'a'), (2, 'b')` на содержимое скобок.
func splitValueGroups(s string) []string {
	var out []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if inQuote && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inQuote = !inQuote
		case '(':
			if !inQuote {
				if depth == 0 {
					start = i + 1
				}
				depth++
			}
		case ')':
			if !inQuote {
				depth--
				if depth == 0 {
					out = append(out, s[start:i])
				}
			}
		}
	}
	return out
}

// parseValueList разбирает содержимое одной группы значений INSERT.
func parseValueList(s string) []*string {
	var out []*string
	var cur strings.Builder
	inQuote := false
	depth := 0
	flush := func() {
		raw := strings.TrimSpace(cur.String())
		cur.Reset()
		if strings.EqualFold(raw, "NULL") || raw == "" {
			out = append(out, nil)
			return
		}
		if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			v := strings.ReplaceAll(raw[1:len(raw)-1], "''", "'")
			out = append(out, &v)
			return
		}
		v := raw
		out = append(out, &v)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			cur.WriteByte(c)
			if inQuote && i+1 < len(s) && s[i+1] == '\'' {
				cur.WriteByte('\'')
				i++
				continue
			}
			inQuote = !inQuote
		case inQuote:
			cur.WriteByte(c)
		case c == '(':
			depth++
			cur.WriteByte(c)
		case c == ')':
			depth--
			cur.WriteByte(c)
		case c == ',' && depth == 0:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}
