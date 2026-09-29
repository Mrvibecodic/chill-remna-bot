package i18n

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

var (
	tagRe    = regexp.MustCompile(`<[a-zA-Z/][^>]*>`)
	keyRefRe = regexp.MustCompile(`«([a-z]+\.[a-z_*]+)»`)
)

// TestEditableCatalog сверяет каталог редактора со стандартными текстами: иначе
// правка ru.go молча ломает свой текст админа (не те переменные, не то число).
func TestEditableCatalog(t *testing.T) {
	if len(editableList) == 0 {
		t.Fatal("каталог пуст")
	}
	secs := map[string]bool{}
	for _, s := range editSections {
		secs[s] = true
	}
	ruByName := map[string]string{}
	nameByRu := map[string]string{}
	seen := map[string]bool{}
	nameRe := regexp.MustCompile(`^[a-z][a-z0-9_]{0,19}$`)
	ruRe := regexp.MustCompile(`^[а-яё][а-яё0-9_]{0,19}$`)
	for _, e := range editableList {
		if seen[e.Key] {
			t.Errorf("%s: ключ в каталоге дважды", e.Key)
		}
		seen[e.Key] = true
		ruT, okRu := ru[e.Key]
		enT, okEn := en[e.Key]
		if !okRu || !okEn {
			t.Errorf("%s: нет стандартного текста", e.Key)
			continue
		}
		if strings.Contains(ruT, "%[") || strings.Contains(enT, "%[") {
			t.Errorf("%s: явные индексы подстановок редактор не поддерживает", e.Key)
		}
		vr, ve := verbsOf(ruT), verbsOf(enT)
		if strings.Join(vr, ",") != strings.Join(ve, ",") {
			t.Errorf("%s: подстановки ru %v и en %v различаются", e.Key, vr, ve)
		}
		if len(e.Vars) != len(vr) {
			t.Errorf("%s: переменных %d, подстановок %d", e.Key, len(e.Vars), len(vr))
		}
		if !secs[e.Section] {
			t.Errorf("%s: неизвестный раздел %q", e.Key, e.Section)
		}
		if e.Where[0] == "" || e.Where[1] == "" {
			t.Errorf("%s: нет подсказки «где видно»", e.Key)
		}
		inKey := map[string]string{}
		for _, v := range e.Vars {
			if !nameRe.MatchString(v.Name) || !ruRe.MatchString(v.Ru) {
				t.Errorf("%s: имя переменной %q/%q", e.Key, v.Name, v.Ru)
			}
			if v.Desc[0] == "" || v.Desc[1] == "" || v.Example[0] == "" || v.Example[1] == "" {
				t.Errorf("%s.%s: нет описания или примера", e.Key, v.Name)
			}
			// Описание ссылается на другой текст по коду — код должен быть в
			// каталоге, иначе админ его не найдёт.
			for _, m := range keyRefRe.FindAllStringSubmatch(v.Desc[0]+v.Desc[1], -1) {
				ref := m[1]
				if strings.HasSuffix(ref, "*") {
					continue
				}
				if _, ok := editableIdx[ref]; !ok {
					t.Errorf("%s.%s: ссылка на текст %q, которого нет в каталоге", e.Key, v.Name, ref)
				}
			}
			if r, ok := ruByName[v.Name]; ok && r != v.Ru {
				t.Errorf("%s: %s называется и %q, и %q", e.Key, v.Name, r, v.Ru)
			}
			if n, ok := nameByRu[v.Ru]; ok && n != v.Name {
				t.Errorf("%s: %q означает и %s, и %s", e.Key, v.Ru, n, v.Name)
			}
			ruByName[v.Name], nameByRu[v.Ru] = v.Ru, v.Name
			if prev, ok := inKey[v.Name]; ok && prev != v.Ru {
				t.Errorf("%s: повтор %s с другим смыслом", e.Key, v.Name)
			}
			inKey[v.Name] = v.Ru
		}
		if (ruT == "" || enT == "") != e.Optional {
			t.Errorf("%s: пустой стандартный текст бывает только у необязательного", e.Key)
		}
		if e.Optional {
			if len(e.Vars) != 0 {
				t.Errorf("%s: у необязательного текста переменных не бывает", e.Key)
			}
			continue
		}
		for _, lang := range []string{"ru", "en"} {
			canon := DefaultCanonical(lang, e.Key)
			if len(e.Vars) > 0 && len(canonRe.FindAllString(canon, -1)) != len(e.Vars) {
				t.Errorf("%s/%s: в шаблоне остались подстановки: %q", e.Key, lang, canon)
			}
			if err := Compile(e.Key, canon); err != nil {
				t.Errorf("%s/%s: стандартный текст не собирается: %v", e.Key, lang, err)
			}
			if miss := MissingVars(e.Key, canon); len(miss) != 0 {
				t.Errorf("%s/%s: стандартный текст без переменных %v", e.Key, lang, miss)
			}
			ex := RenderExample(lang, e.Key, canon)
			switch e.Kind {
			case KindButton:
				if strings.Contains(ex, "\n") || tagRe.MatchString(ex) {
					t.Errorf("%s/%s: кнопка с переносом или разметкой: %q", e.Key, lang, ex)
				}
			case KindPlain:
				if tagRe.MatchString(ex) {
					t.Errorf("%s/%s: обычный текст с разметкой: %q", e.Key, lang, ex)
				}
			}
			if e.MaxLen > 0 {
				worst := tagRe.ReplaceAllString(RenderWorst(lang, e.Key, canon), "")
				if n := utf8.RuneCountInString(worst); n > e.MaxLen {
					t.Errorf("%s/%s: стандартный текст (%d) длиннее предела %d", e.Key, lang, n, e.MaxLen)
				}
			}
			if e.Kind != KindHTML && strings.ContainsAny(canon, "<>&") {
				t.Errorf("%s/%s: в тексте без разметки символы < > &", e.Key, lang)
			}
		}
	}
}

func withOverrides(t *testing.T, m map[string]map[string]string) map[string]map[string]error {
	t.Helper()
	t.Cleanup(ResetOverrides)
	return SetOverrides(m)
}

func firstWithVars(t *testing.T, n int) *Editable {
	t.Helper()
	for i := range editableList {
		if len(editableList[i].UniqueVars()) >= n && len(editableList[i].UniqueVars()) == len(editableList[i].Vars) {
			return &editableList[i]
		}
	}
	t.Fatalf("нет текста с %d переменными", n)
	return nil
}

func TestOverrideRendersNamedVars(t *testing.T) {
	e := firstWithVars(t, 2)
	v0, v1 := e.Vars[0], e.Vars[1]
	args := make([]any, len(e.Vars))
	verbs := editVerbs["ru"][e.Key]
	for i, vb := range verbs {
		if strings.HasSuffix(vb, "d") {
			args[i] = 40 + i
		} else if strings.HasSuffix(vb, "f") {
			args[i] = 1.5
		} else {
			args[i] = "знач" + string(rune('A'+i))
		}
	}
	// Порядок переставлен, одна переменная дважды, знак процента буквально.
	text := "<b>{" + v1.Name + "}</b> и {" + v0.Name + "}, снова {" + v1.Name + "} — 100%"
	if rej := withOverrides(t, map[string]map[string]string{"ru": {e.Key: text}}); rej != nil {
		t.Fatalf("отклонено: %v", rej)
	}
	a0 := fmtArg(verbs[0], args[0])
	a1 := fmtArg(verbs[1], args[1])
	want := "<b>" + a1 + "</b> и " + a0 + ", снова " + a1 + " — 100%"
	if got := T("ru", e.Key, args...); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Другой язык не задет.
	if got := T("en", e.Key, args...); strings.Contains(got, "снова") {
		t.Fatalf("свой текст ru попал в en: %q", got)
	}
	// Число аргументов не совпало — стандартный текст, а не мусор.
	if got := T("ru", e.Key, args[:1]...); strings.Contains(got, "снова") {
		t.Fatalf("при чужом числе аргументов показан свой текст: %q", got)
	}
	if got := T("ru", e.Key, append(args, "лишний")...); strings.Contains(got, "снова") {
		t.Fatalf("при лишнем аргументе показан свой текст: %q", got)
	}
	if got, own := Effective("ru", e.Key); !own || got != text {
		t.Fatalf("Effective: %q %v", got, own)
	}
}

func fmtArg(verb string, v any) string { return fmt.Sprintf(verb, v) }

func TestOverrideRejectsUnknownVar(t *testing.T) {
	e := firstWithVars(t, 1)
	rej := withOverrides(t, map[string]map[string]string{"ru": {e.Key: "текст {nope}", "admin.none": "x"}})
	if !errors.Is(rej["ru"][e.Key], ErrUnknownVar) {
		t.Fatalf("неизвестная переменная принята: %v", rej)
	}
	if !errors.Is(rej["ru"]["admin.none"], ErrNotEditable) {
		t.Fatalf("админский текст принят: %v", rej)
	}
	if got := T("ru", "admin.none"); got != ru["admin.none"] {
		t.Fatalf("админский текст подменён: %q", got)
	}
}

func TestOverrideWithoutVars(t *testing.T) {
	var key string
	for _, e := range editableList {
		if len(e.Vars) == 0 && e.Kind == KindHTML {
			key = e.Key
			break
		}
	}
	withOverrides(t, map[string]map[string]string{"ru": {key: "скидка 10% {не_переменная"}})
	if got := T("ru", key); got != "скидка 10% {не_переменная" {
		t.Fatalf("got %q", got)
	}
	ResetOverrides()
	if got := T("ru", key); got != ru[key] {
		t.Fatalf("после сброса: %q", got)
	}
}

func TestCanonicalize(t *testing.T) {
	e := firstWithVars(t, 1)
	v := e.Vars[0]
	in := "{ " + strings.ToUpper(v.Ru) + " } и {" + v.Name + "} и {нет_такой}"
	out, unknown := Canonicalize(e.Key, in)
	want := "{" + v.Name + "} и {" + v.Name + "} и {нет_такой}"
	if out != want || len(unknown) != 1 || unknown[0] != "нет_такой" {
		t.Fatalf("got %q %v", out, unknown)
	}
	if d := Display("ru", e.Key, "{"+v.Name+"}"); d != "{"+v.Ru+"}" {
		t.Fatalf("Display ru: %q", d)
	}
	if d := Display("en", e.Key, "{"+v.Name+"}"); d != "{"+v.Name+"}" {
		t.Fatalf("Display en: %q", d)
	}
	// «ё» и «е» — одно и то же.
	for _, x := range editableList {
		for _, xv := range x.Vars {
			if strings.Contains(xv.Ru, "ё") {
				out, unk := Canonicalize(x.Key, "{"+strings.ReplaceAll(xv.Ru, "ё", "е")+"}")
				if len(unk) != 0 || out != "{"+xv.Name+"}" {
					t.Fatalf("ё: %q %v", out, unk)
				}
				return
			}
		}
	}
}

func TestMissingVars(t *testing.T) {
	e := firstWithVars(t, 2)
	miss := MissingVars(e.Key, "только {"+e.Vars[0].Name+"}")
	if len(miss) != len(e.UniqueVars())-1 {
		t.Fatalf("пропущенные: %v", miss)
	}
}

// Необязательный текст по умолчанию пуст: T отдаёт пустую строку, а не ключ.
func TestOptionalEmptyByDefault(t *testing.T) {
	if got := T("ru", "method.yk_note"); got != "" {
		t.Fatalf("got %q", got)
	}
	withOverrides(t, map[string]map[string]string{"ru": {"method.yk_note": "Карты МИР и СБП"}})
	if got := T("ru", "method.yk_note"); got != "Карты МИР и СБП" {
		t.Fatalf("got %q", got)
	}
}
