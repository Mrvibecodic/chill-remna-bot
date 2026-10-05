package i18n

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Свои тексты админа поверх стандартных. Набор меняется целиком и читается
// без замков: T зовут из обработчиков и фоновых горутин одновременно.

var (
	// ErrNotEditable — ключа нет в каталоге редактора.
	ErrNotEditable = errors.New("текст не редактируется")
	// ErrUnknownVar — в тексте переменная, которой у этого текста нет.
	ErrUnknownVar = errors.New("неизвестная переменная")
	// ErrEmpty — пустой текст.
	ErrEmpty = errors.New("пустой текст")
	// ErrBadChars — < > & в кнопке или обычном тексте: такие значения
	// подставляются и в сообщения с разметкой, где сломали бы её.
	ErrBadChars = errors.New("символы < > & в тексте без разметки")
	// ErrNewline — перенос строки в тексте, который обязан быть одной строкой.
	ErrNewline = errors.New("перенос строки в однострочном тексте")
	// ErrTooLong — текст длиннее предела своего места.
	ErrTooLong = errors.New("текст длиннее предела")
)

// ButtonMax — предел длины однострочного текста (кнопки) вместе со
// значениями переменных.
const ButtonMax = 64

var (
	markupTagRe = regexp.MustCompile(`<[^<>]+>`)
)

type segment struct {
	lit string
	pos int // -1 — литерал, иначе позиция подстановки
}

type compiled []segment

var active atomic.Pointer[map[string]map[string]compiled]

// Compile проверяет канонический текст: ключ есть в каталоге, переменные —
// его собственные, соблюдены правила места (одна строка, предел длины). Те же
// проверки идут при загрузке конфига: текст, ставший недопустимым после
// обновления бота, не применяется.
func Compile(lang, key, canonical string) error {
	_, err := compile(lang, key, canonical)
	return err
}

func compile(lang, key, canonical string) (compiled, error) {
	e, ok := editableIdx[key]
	if !ok {
		return nil, ErrNotEditable
	}
	if strings.TrimSpace(canonical) == "" {
		if e.Hideable {
			return compiled{}, nil
		}
		return nil, ErrEmpty
	}
	if e.Kind != KindHTML && strings.ContainsAny(canonical, "<>&") {
		return nil, ErrBadChars
	}
	var out compiled
	last := 0
	for _, m := range canonRe.FindAllStringSubmatchIndex(canonical, -1) {
		name := canonical[m[2]:m[3]]
		pos, ok := e.varByName(name)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownVar, name)
		}
		if m[0] > last {
			out = append(out, segment{lit: canonical[last:m[0]], pos: -1})
		}
		out = append(out, segment{pos: pos})
		last = m[1]
	}
	if last < len(canonical) {
		out = append(out, segment{lit: canonical[last:], pos: -1})
	}
	worst := RenderWorst(lang, key, canonical)
	switch e.Kind {
	case KindButton:
		if strings.ContainsAny(canonical, "\r\n") {
			return nil, ErrNewline
		}
		if utf8.RuneCountInString(worst) > ButtonMax {
			return nil, ErrTooLong
		}
	case KindHTML:
		worst = html.UnescapeString(markupTagRe.ReplaceAllString(worst, ""))
	}
	if e.MaxLen > 0 && utf8.RuneCountInString(worst) > e.MaxLen {
		return nil, ErrTooLong
	}
	return out, nil
}

// SetOverrides заменяет свои тексты целиком (язык → ключ → канонический
// текст). Возвращает то, что не применено, с причиной: такие ключи
// показываются стандартным текстом.
func SetOverrides(texts map[string]map[string]string) map[string]map[string]error {
	next := map[string]map[string]compiled{}
	var rejected map[string]map[string]error
	for lang, m := range texts {
		for key, text := range m {
			c, err := compile(lang, key, text)
			if err != nil {
				if rejected == nil {
					rejected = map[string]map[string]error{}
				}
				if rejected[lang] == nil {
					rejected[lang] = map[string]error{}
				}
				rejected[lang][key] = err
				continue
			}
			if next[lang] == nil {
				next[lang] = map[string]compiled{}
			}
			next[lang][key] = c
		}
	}
	active.Store(&next)
	return rejected
}

// ResetOverrides убирает все свои тексты.
func ResetOverrides() { active.Store(nil) }

func override(lang, key string) (compiled, bool) {
	p := active.Load()
	if p == nil {
		return nil, false
	}
	c, ok := (*p)[lang][key]
	return c, ok
}

// render собирает свой текст. false — число аргументов не совпало с
// подстановками стандартного текста (код вызывает текст иначе, чем знает
// каталог): тогда показывается стандартный текст.
func (c compiled) render(lang, key string, args []any) (string, bool) {
	verbs := editVerbs[lang][key]
	if len(args) != len(verbs) {
		return "", false
	}
	var b strings.Builder
	for _, s := range c {
		if s.pos < 0 {
			b.WriteString(s.lit)
			continue
		}
		b.WriteString(fmt.Sprintf(verbs[s.pos], args[s.pos]))
	}
	return b.String(), true
}

// Effective — текст, который сейчас видит пользователь, в каноническом виде,
// и признак «свой текст».
func Effective(lang, key string) (string, bool) {
	p := active.Load()
	if p != nil {
		if _, ok := (*p)[lang][key]; ok {
			// Собираем обратно из сегментов: хранится ровно то, что
			// применено.
			var b strings.Builder
			e := editableIdx[key]
			for _, s := range (*p)[lang][key] {
				if s.pos < 0 {
					b.WriteString(s.lit)
				} else {
					b.WriteString("{" + e.Vars[s.pos].Name + "}")
				}
			}
			return b.String(), true
		}
	}
	return DefaultCanonical(lang, key), false
}
