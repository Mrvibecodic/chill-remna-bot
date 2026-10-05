package i18n

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Тексты, которые админ может переписать под себя (раздел «Тексты бота»).
//
// В каталог входит только то, что видит пользователь. Подстановки стандартного
// текста (%s, %d) админ пишет именованными переменными в фигурных скобках —
// {тариф} или {plan}; хранится текст с каноническими (латинскими) именами.

// Kind — куда уходит текст и что в нём допустимо.
type Kind uint8

const (
	// KindHTML — сообщение в чате: разметка Telegram.
	KindHTML Kind = iota
	// KindButton — подпись кнопки или значение, которое в неё подставляется:
	// одна строка без разметки.
	KindButton
	// KindPlain — письмо, описание платежа, текст для страницы: без разметки.
	KindPlain
)

// EditVar — переменная текста, по позиции подстановки стандартного текста.
type EditVar struct {
	Name    string    // каноническое имя (латиница)
	Ru      string    // русское имя
	Desc    [2]string // ru, en
	Example [2]string // ru, en: значение уже в том виде, в каком оно подставится
}

// Editable — текст, доступный редактору.
type Editable struct {
	Key     string
	Section string
	Kind    Kind
	// MaxLen — предел длины для текстов, уходящих во внешние API (0 — нет).
	MaxLen int
	// Optional — текст по умолчанию пуст и тогда не показывается; «убрать»
	// возвращает его к пустому.
	Optional bool
	// Hideable — текст можно убрать совсем: сохраняется пустым, и место, куда
	// он подставляется, показывается без него.
	Hideable bool
	Where    [2]string
	Vars     []EditVar
}

// Разделы в порядке показа.
var editSections = []string{"start", "buy", "pay", "balance", "sub", "autopay", "trial", "ref", "legal", "block", "mail", "errors"}

var (
	editableIdx map[string]*Editable
	// editVerbs — подстановки стандартного текста по языкам, в порядке следования.
	editVerbs map[string]map[string][]string
)

var editVerbRe = regexp.MustCompile(`%[-+# 0]*[0-9]*(?:\.[0-9]+)?[a-zA-Z%]`)

func init() {
	editableIdx = make(map[string]*Editable, len(editableList))
	editVerbs = map[string]map[string][]string{}
	for i := range editableList {
		e := &editableList[i]
		editableIdx[e.Key] = e
	}
	for lang, b := range bundles {
		m := map[string][]string{}
		for key := range editableIdx {
			m[key] = verbsOf(b[key])
		}
		editVerbs[lang] = m
	}
}

func verbsOf(s string) []string {
	var out []string
	for _, v := range editVerbRe.FindAllString(s, -1) {
		if v != "%%" {
			out = append(out, v)
		}
	}
	return out
}

func li(lang string) int {
	if lang == "en" {
		return 1
	}
	return 0
}

// EditableByKey возвращает текст каталога.
func EditableByKey(key string) (*Editable, bool) {
	e, ok := editableIdx[key]
	return e, ok
}

// EditSections — разделы каталога в порядке показа.
func EditSections() []string { return append([]string(nil), editSections...) }

// EditableIn — тексты раздела, отсортированные по ключу.
func EditableIn(section string) []*Editable {
	var out []*Editable
	for i := range editableList {
		if editableList[i].Section == section {
			out = append(out, &editableList[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// EditableAll — весь каталог, отсортированный по разделам и ключам.
func EditableAll() []*Editable {
	var out []*Editable
	for _, s := range editSections {
		out = append(out, EditableIn(s)...)
	}
	return out
}

// WhereText — подсказка «где видно» на языке lang.
func (e *Editable) WhereText(lang string) string { return e.Where[li(lang)] }

// VarName — имя переменной для показа админу на языке lang.
func (v EditVar) VarName(lang string) string {
	if li(lang) == 0 && v.Ru != "" {
		return v.Ru
	}
	return v.Name
}

// IsLinkVar — переменная подставляет адрес (её можно ставить в ссылку).
func (e *Editable) IsLinkVar(name string) bool {
	i, ok := e.varByName(name)
	if !ok {
		return false
	}
	ex := e.Vars[i].Example[0]
	return strings.HasPrefix(ex, "https://") || strings.HasPrefix(ex, "http://") || strings.HasPrefix(ex, "tg://")
}

// UniqueVars — переменные без повторов (одно значение может стоять дважды).
func (e *Editable) UniqueVars() []EditVar {
	seen := map[string]bool{}
	var out []EditVar
	for _, v := range e.Vars {
		if !seen[v.Name] {
			seen[v.Name] = true
			out = append(out, v)
		}
	}
	return out
}

func (e *Editable) varByName(name string) (int, bool) {
	for i, v := range e.Vars {
		if v.Name == name {
			return i, true
		}
	}
	return 0, false
}

// Default — стандартный текст (без подстановок).
func Default(lang, key string) string {
	if v := lookup(lang, key); v != "" {
		return v
	}
	return lookup(Fallback, key)
}

// DefaultCanonical — стандартный текст в виде шаблона редактора:
// подстановки заменены на {имя}, «%%» — на «%» (у текстов с подстановками).
func DefaultCanonical(lang, key string) string {
	e, ok := editableIdx[key]
	src := Default(lang, key)
	if !ok || len(e.Vars) == 0 {
		return src
	}
	i := 0
	return editVerbRe.ReplaceAllStringFunc(src, func(v string) string {
		if v == "%%" {
			return "%"
		}
		if i >= len(e.Vars) {
			return v
		}
		name := e.Vars[i].Name
		i++
		return "{" + name + "}"
	})
}

// DefaultHash — отпечаток стандартного текста: по нему видно, что после
// обновления бота стандартный вариант изменённого текста поменялся.
func DefaultHash(lang, key string) string {
	sum := sha256.Sum256([]byte(DefaultCanonical(lang, key)))
	return hex.EncodeToString(sum[:6])
}

// placeholderRe — кандидаты в переменные: буквы, цифры, «_» и пробелы.
var placeholderRe = regexp.MustCompile(`\{\s*([\p{L}\p{N}_ ]{1,40}?)\s*\}`)

// canonRe — переменная в каноническом виде.
var canonRe = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

func normName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), "_")
	return strings.ReplaceAll(s, "ё", "е")
}

// Canonicalize приводит переменные текста к каноническим именам. Имена
// принимаются русские и английские, в любом регистре, «ё» равна «е».
// unknown — имена, которых у текста нет (текст с ними не сохраняется).
func Canonicalize(key, text string) (out string, unknown []string) {
	e, ok := editableIdx[key]
	byName := map[string]string{}
	if ok {
		for _, v := range e.Vars {
			byName[normName(v.Name)] = v.Name
			byName[normName(v.Ru)] = v.Name
		}
	}
	seen := map[string]bool{}
	out = placeholderRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := placeholderRe.FindStringSubmatch(m)
		if canon, ok := byName[normName(sub[1])]; ok {
			return "{" + canon + "}"
		}
		if !seen[sub[1]] {
			seen[sub[1]] = true
			unknown = append(unknown, strings.TrimSpace(sub[1]))
		}
		return m
	})
	return out, unknown
}

// Display показывает канонический текст с именами переменных языка lang.
func Display(lang, key, canonical string) string {
	e, ok := editableIdx[key]
	if !ok || len(e.Vars) == 0 {
		return canonical
	}
	return canonRe.ReplaceAllStringFunc(canonical, func(m string) string {
		name := m[1 : len(m)-1]
		if i, ok := e.varByName(name); ok {
			return "{" + e.Vars[i].VarName(lang) + "}"
		}
		return m
	})
}

// MissingVars — переменные, которых нет в тексте.
func MissingVars(key, canonical string) []EditVar {
	e, ok := editableIdx[key]
	if !ok {
		return nil
	}
	used := map[string]bool{}
	for _, m := range canonRe.FindAllStringSubmatch(canonical, -1) {
		used[m[1]] = true
	}
	var out []EditVar
	for _, v := range e.UniqueVars() {
		if !used[v.Name] {
			out = append(out, v)
		}
	}
	return out
}

// RenderWorst — как RenderExample, но числа подставлены шире примера
// (9999): предел длины проверяется по худшему случаю, а не по «3 мес».
func RenderWorst(lang, key, canonical string) string {
	e, ok := editableIdx[key]
	if !ok || len(e.Vars) == 0 {
		return canonical
	}
	verbs := editVerbs[lang][key]
	idx := li(lang)
	return canonRe.ReplaceAllStringFunc(canonical, func(m string) string {
		i, ok := e.varByName(m[1 : len(m)-1])
		if !ok {
			return m
		}
		if i < len(verbs) && strings.HasSuffix(verbs[i], "d") {
			return fmt.Sprintf(verbs[i], 9999)
		}
		return e.Vars[i].Example[idx]
	})
}

// RenderExample подставляет в канонический текст примеры значений.
func RenderExample(lang, key, canonical string) string {
	e, ok := editableIdx[key]
	if !ok || len(e.Vars) == 0 {
		return canonical
	}
	idx := li(lang)
	return canonRe.ReplaceAllStringFunc(canonical, func(m string) string {
		if i, ok := e.varByName(m[1 : len(m)-1]); ok {
			return e.Vars[i].Example[idx]
		}
		return m
	})
}
