package i18n

import (
	"regexp"
	"testing"
)

var verbRe = regexp.MustCompile(`%[-+#0-9.]*[a-zA-Z%]`)

func verbs(s string) []string {
	var out []string
	for _, v := range verbRe.FindAllString(s, -1) {
		if v != "%%" {
			out = append(out, v[len(v)-1:])
		}
	}
	return out
}

// Шаблон с разным числом или порядком подстановок в ru и en ломает одну из
// локалей молча: fmt печатает %!d(MISSING) или EXTRA прямо в сообщение.
func TestPlaceholderParity(t *testing.T) {
	for k, rv := range ru {
		ev, ok := en[k]
		if !ok {
			continue
		}
		a, b := verbs(rv), verbs(ev)
		if len(a) != len(b) {
			t.Errorf("%q: подстановок ru=%v en=%v", k, a, b)
			continue
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("%q: подстановки расходятся ru=%v en=%v", k, a, b)
				break
			}
		}
	}
}
