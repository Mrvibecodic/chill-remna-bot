package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dynamicPrefixes — префиксы, из которых ключ собирается конкатенацией в коде
// (i18n.T(lang, "access.mode_"+mode)). Такой ключ по литералу не найти, и
// удалять его нельзя: T на отсутствующем ключе возвращает САМ КЛЮЧ, то есть
// поломка вылезет в проде молча и в редкой ветке.
var dynamicPrefixes = []string{
	"access.mode_",
	"access.hint_",
	"cabinet.appr_",
	"mail.ask_",
	"hl.admin_",
	"plans.av_",
	"plans.avd_",
	"lang.name_",
}

// Осиротевшие после рефакторингов ключи накапливаются незаметно: компилятор их
// не видит, тесты не падают. Этот сторож не даёт им копиться снова.
func TestNoUnusedKeys(t *testing.T) {
	root := "../.."
	var sources []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "site":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".html", ".js":
		default:
			return nil
		}
		// Сами словари не считаем: там ключ есть по определению.
		if strings.HasSuffix(path, "internal/i18n/ru.go") || strings.HasSuffix(path, "internal/i18n/en.go") {
			return nil
		}
		b, err := os.ReadFile(path) // #nosec G304 -- обход дерева репозитория в тесте
		if err != nil {
			return nil
		}
		sources = append(sources, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(sources, "\n")

	var dead []string
	for k := range ru {
		if strings.Contains(all, `"`+k+`"`) {
			continue
		}
		dyn := false
		for _, p := range dynamicPrefixes {
			if strings.HasPrefix(k, p) && strings.Contains(all, `"`+p+`"`) {
				dyn = true
				break
			}
		}
		if !dyn {
			dead = append(dead, k)
		}
	}
	if len(dead) > 0 {
		t.Fatalf("ключи перевода не используются нигде (%d): %v\n"+
			"Если ключ собирается конкатенацией — добавьте его префикс в dynamicPrefixes.", len(dead), dead)
	}
}
