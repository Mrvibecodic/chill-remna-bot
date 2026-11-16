package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var jsFuncDecl = regexp.MustCompile(`(?m)^(?:async )?function (\w+)\(`)

// Общий скрипт двух дизайнов: каждая его функция объявлена ровно один раз —
// в shared.js, а не в страницах. Иначе правка в одном месте молча не доходила
// бы до второго дизайна, от чего общий файл и избавляет.
func TestSharedScriptIsTheOnlyCopy(t *testing.T) {
	shared, err := miniStaticFS.ReadFile("miniapp_static/shared.js")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, m := range jsFuncDecl.FindAllStringSubmatch(string(shared), -1) {
		if names[m[1]] {
			t.Errorf("функция %s объявлена в shared.js дважды", m[1])
		}
		names[m[1]] = true
	}
	if len(names) < 10 || !names["mountChat"] || !names["startParams"] {
		t.Fatalf("в shared.js не нашлось ожидаемых функций: %d", len(names))
	}
	if strings.Contains(strings.ToLower(string(shared)), "</script") {
		t.Fatal("shared.js вставляется внутрь <script> и не может содержать закрывающий тег")
	}
	for _, page := range []string{"index.html", "minimal.html"} {
		raw, err := miniStaticFS.ReadFile("miniapp_static/" + page)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(raw), sharedMarker); n != 1 {
			t.Errorf("%s: меток общего скрипта %d, нужна одна", page, n)
		}
		for _, m := range jsFuncDecl.FindAllStringSubmatch(string(raw), -1) {
			if names[m[1]] {
				t.Errorf("%s объявляет %s заново — она уже есть в shared.js", page, m[1])
			}
		}
		for name := range names {
			// Вызов из самой страницы или из другой общей функции.
			if !strings.Contains(string(raw), name+"(") && strings.Count(string(shared), name+"(") < 2 {
				t.Errorf("%s не пользуется %s — в общем файле ей не место", page, name)
			}
		}
	}
}

// Обе страницы уходят человеку с уже вставленным общим скриптом: метки нет,
// функции на месте и стоят раньше основного скрипта страницы.
func TestPagesAreServedWithSharedScript(t *testing.T) {
	for _, design := range []string{"classic", designMinimal} {
		h := brandServer(&brandMini{ui: WebUIDTO{Design: design}})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/miniapp/", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Contains(body, sharedMarker) {
			t.Fatalf("%s: код %d, метка осталась=%v", design, w.Code, strings.Contains(body, sharedMarker))
		}
		def := strings.Index(body, "function mountChat(")
		use := strings.Index(body, "mountChat(m.chat_src)")
		if def < 0 || use < 0 || def > use || strings.Count(body, "function mountChat(") != 1 {
			t.Fatalf("%s: общий скрипт не на месте (объявление %d, вызов %d)", design, def, use)
		}
	}
}

// Свой дизайн оператора без метки отдаётся как есть.
func TestCustomPageWithoutMarkerIsUntouched(t *testing.T) {
	page := []byte("<html><script>function mountChat(){}</script></html>")
	if got := injectShared(page); string(got) != string(page) {
		t.Fatalf("страница без метки изменена: %s", got)
	}
}

// Свой дизайн оператора, сделанный из свежей копии встроенной страницы, несёт
// ту же метку — общий скрипт вставляется и в него, иначе страница осталась бы
// без своих функций.
func TestCustomPageWithMarkerGetsSharedScript(t *testing.T) {
	dir := t.TempDir()
	page := "<html><script>" + sharedMarker + "</script><script>mountChat('x')</script></html>"
	if err := os.WriteFile(filepath.Join(dir, "miniapp.html"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	s.SetStaticDir(dir)
	s.SetMiniApp(&brandMini{ui: WebUIDTO{Design: designMinimal}})
	w := httptest.NewRecorder()
	s.wrap(s.mux()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/miniapp/", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, sharedMarker) ||
		!strings.Contains(body, "function mountChat(") || !strings.Contains(body, "mountChat('x')") {
		t.Fatalf("своя страница с меткой: код %d, тело %.200s", w.Code, body)
	}
}
