package i18n

import (
	"strings"
	"testing"
)

// В текстах не должно быть «\n» буквами: при правке строк легко удвоить
// обратную косую черту, и вместо переноса человек видит «\n» на экране.
func TestNoLiteralBackslashN(t *testing.T) {
	for name, m := range map[string]map[string]string{"ru": ru, "en": en} {
		for k, v := range m {
			if strings.Contains(v, `\n`) {
				t.Errorf("%s/%s: «\\n» буквами вместо переноса", name, k)
			}
		}
	}
}
