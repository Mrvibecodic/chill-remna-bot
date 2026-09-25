package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Вызов i18n.T с числом аргументов, не совпадающим с подстановками шаблона,
// компилируется и проходит vet (T — не printf-обёртка), а в сообщение уходит
// %!(EXTRA …) или %!s(MISSING). Сверяем литеральные вызовы по всему коду.
func TestCallArgsMatchTemplates(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.Walk("../..", func(path string, info os.FileInfo, err error) error {
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
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "internal/i18n/") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Errorf("%s: %v", path, perr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 || call.Ellipsis.IsValid() {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "T" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "i18n" {
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			tmpl, ok := ru[key]
			if !ok {
				// T на отсутствующем ключе печатает сам ключ.
				t.Errorf("%s: i18n.T(%q) — ключа нет в словаре", fset.Position(call.Pos()), key)
				return true
			}
			// Индексные подстановки (%[1]d) считаются иначе — не сверяем.
			if strings.Contains(tmpl, "%[") {
				return true
			}
			args := len(call.Args) - 2
			if want := len(verbs(tmpl)); want != args {
				t.Errorf("%s: i18n.T(%q) — аргументов %d, подстановок в шаблоне %d", fset.Position(call.Pos()), key, args, want)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
