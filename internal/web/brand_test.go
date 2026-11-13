package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brandMini — двойник с настройками оформления и гейтом документов.
type brandMini struct {
	fakeMini
	ui    WebUIDTO
	logo  []byte
	legal bool
	title string
	desc  string
}

func (f *brandMini) WebUI() WebUIDTO { return f.ui }
func (f *brandMini) BrandLogo(_ context.Context, dark bool) ([]byte, bool) {
	if dark || f.logo == nil {
		return nil, false
	}
	return f.logo, true
}
func (f *brandMini) MiniLegalRequired(context.Context, int64) bool { return f.legal }
func (f *brandMini) CabinetTitle() string                          { return f.title }
func (f *brandMini) CabinetDescription() string                    { return f.desc }
func (f *brandMini) CabinetFavicon() string                        { return "" }
func (f *brandMini) CabinetAntiFP() bool                           { return false }
func (f *brandMini) MiniPlans(context.Context, int64) MiniPlansDTO { return MiniPlansDTO{} }

func brandServer(f *brandMini) http.Handler {
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	s.SetMiniApp(f)
	return s.wrap(s.mux())
}

// Выбор дизайна: новый дизайн отдаёт свою страницу с подставленными
// настройками, классический — прежнюю страницу; в обе вставлен общий скрипт.
func TestDesignSelection(t *testing.T) {
	f := &brandMini{ui: WebUIDTO{Design: designMinimal, Theme: "dark", Name: "Acme </script><b>", Accent: "#112233"}}
	h := brandServer(f)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/miniapp/", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, brandMarker) {
		t.Fatalf("новый дизайн: код %d, маркер остался=%v", w.Code, strings.Contains(body, brandMarker))
	}
	if strings.Contains(body, "</script><b>") {
		t.Fatal("название попало в страницу без экранирования")
	}
	if !strings.Contains(body, `"accent":"#112233"`) || !strings.Contains(body, `"surface":"miniapp"`) {
		t.Fatal("настройки оформления не подставлены")
	}

	f.ui.Design = "classic"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/miniapp/", nil))
	classic, _ := miniStaticFS.ReadFile("miniapp_static/index.html")
	if w.Body.String() != string(injectShared(classic)) {
		t.Fatal("классический дизайн обязан отдаваться без изменений, кроме вставки общего скрипта")
	}
}

// Кабинет нового дизайна: заголовок уходит в настройки, а не заменой текста по
// разметке; без фавикона вкладку подписывает логотип.
func TestCabinetMinimalBranding(t *testing.T) {
	f := &brandMini{ui: WebUIDTO{Design: designMinimal, Logo: "/brand/logo?v=1"}, title: "Acme"}
	h := brandServer(f)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
	body := w.Body.String()
	if !strings.Contains(body, "<title>Acme</title>") {
		t.Fatal("заголовок вкладки не подставлен")
	}
	i := strings.Index(body, "window.BRAND=")
	if i < 0 {
		t.Fatal("нет настроек оформления")
	}
	raw := body[i+len("window.BRAND="):]
	raw = raw[:strings.Index(raw, ";</script>")]
	var got WebUIDTO
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("настройки не разбираются: %v (%s)", err, raw)
	}
	if got.Title != "Acme" || got.Surface != "cabinet" {
		t.Fatalf("настройки кабинета: %+v", got)
	}
	if !strings.Contains(body, `<link rel="icon" href="/brand/logo?v=1">`) {
		t.Fatal("логотип не стал фавиконом")
	}
}

// Логотип отдаётся только картинкой и в песочнице: SVG по прямой ссылке не
// должен исполнять скрипты на домене кабинета.
func TestBrandLogoServing(t *testing.T) {
	f := &brandMini{logo: []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`)}
	h := brandServer(f)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/brand/logo", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("svg: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("логотип без песочницы")
	}
	f.logo = []byte("<html><script>alert(1)</script></html>")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/brand/logo", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("не картинка обязана не отдаваться: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/brand/logo-dark", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("тёмного логотипа нет — 404: %d", w.Code)
	}
}

func TestSniffImage(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for _, c := range []struct {
		data string
		ok   bool
	}{
		{string(png), true},
		{"<svg xmlns='http://www.w3.org/2000/svg'/>", true},
		{"\xef\xbb\xbf<?xml version='1.0'?>\n<svg/>", true},
		{"<html><body>x</body></html>", false},
		{"<?xml version='1.0'?><note/>", false},
		{"", false},
	} {
		if _, ok := SniffImage([]byte(c.data)); ok != c.ok {
			t.Fatalf("%q: ожидалось %v", c.data, c.ok)
		}
	}
}

// Согласие с документами закрывает действия, а не чтение: иначе стартовая
// загрузка витрины получала отказ раньше, чем человек видел документы.
func TestLegalGateOnActionsOnly(t *testing.T) {
	f := &brandMini{legal: true}
	h := brandServer(f)
	tok := webToken(&f.fakeMini, 1000000001, 0)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodGet, "/api/miniapp/plans", "", tok, "203.0.113.30"))
	if w.Code != http.StatusOK {
		t.Fatalf("чтение витрины до согласия обязано работать: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/miniapp/checkout", `{"months":1}`, tok, "203.0.113.31"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("покупка до согласия обязана быть закрыта: %d", w.Code)
	}
}

func TestInitDataStartParam(t *testing.T) {
	if p := initDataStartParam("user=%7B%7D&start_param=ref_1000000001&hash=x"); p != "ref_1000000001" {
		t.Fatalf("start_param: %q", p)
	}
	if p := initDataStartParam("start_param=" + strings.Repeat("a", 65)); p != "" {
		t.Fatal("слишком длинный параметр обязан отбрасываться")
	}
}

// Без своего заголовка кабинет в обоих дизайнах называется одинаково — и во
// вкладке, и в шапке.
func TestCabinetDefaultTitle(t *testing.T) {
	for _, design := range []string{"classic", designMinimal} {
		f := &brandMini{ui: WebUIDTO{Design: design}}
		w := httptest.NewRecorder()
		brandServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
		body := w.Body.String()
		if !strings.Contains(body, "<title>Личный кабинет</title>") {
			t.Fatalf("%s: вкладка названа иначе", design)
		}
		if strings.Contains(body, "'Кабинет'") || strings.Contains(body, ">Кабинет<") {
			t.Fatalf("%s: в странице остался заголовок «Кабинет»", design)
		}
		if strings.ContainsRune(body, 0) {
			t.Fatalf("%s: в странице осталась метка подстановки", design)
		}
	}
}

// Мини-апп называется во вкладке так же, как кабинет.
func TestMiniAppTitle(t *testing.T) {
	for _, design := range []string{"classic", designMinimal} {
		f := &brandMini{ui: WebUIDTO{Design: design}}
		w := httptest.NewRecorder()
		brandServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/miniapp/", nil))
		if !strings.Contains(w.Body.String(), "<title>Личный кабинет</title>") {
			t.Fatalf("%s: вкладка мини-аппа названа иначе", design)
		}
	}
}

// Своя страница оператора из старой копии встроенной несёт прежнюю метку
// заголовка: свой заголовок кабинета подставляется и в неё.
func TestCabinetTitleInOldCustomPage(t *testing.T) {
	dir := t.TempDir()
	page := "<!doctype html><html><head><title>Кабинет</title></head><body><h1>Личный кабинет</h1></body></html>"
	if err := os.WriteFile(filepath.Join(dir, "cabinet.html"), []byte(page), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	s.SetMiniApp(&brandMini{title: "Acme"})
	s.SetStaticDir(dir)
	w := httptest.NewRecorder()
	s.wrap(s.mux()).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
	body := w.Body.String()
	if !strings.Contains(body, "<title>Acme</title>") || !strings.Contains(body, "<h1>Acme</h1>") {
		t.Fatalf("свой заголовок не подставлен в старую страницу: %s", body)
	}
}

// Подставленное в страницу повторно не разбирается: свой заголовок со словами
// встроенного не задваивается, а заголовок, похожий на метку страницы, остаётся
// текстом. Описание встаёт перед названием вкладки один раз.
func TestCabinetTitleSubstitutedOnce(t *testing.T) {
	for _, design := range []string{"classic", designMinimal} {
		f := &brandMini{ui: WebUIDTO{Design: design}, title: "Личный кабинет Acme", desc: "Описание"}
		w := httptest.NewRecorder()
		brandServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
		body := w.Body.String()
		if !strings.Contains(body, "<meta name=\"description\" content=\"Описание\">\n<title>Личный кабинет Acme</title>") {
			t.Fatalf("%s: вкладка или описание не на месте", design)
		}
		if strings.Contains(body, "Acme Acme") || strings.Count(body, "name=\"description\"") != 1 {
			t.Fatalf("%s: подставленное разобрано повторно", design)
		}
		if design == "classic" && !strings.Contains(body, "<h1>Личный кабинет Acme</h1>") {
			t.Fatal("заголовок страницы не подставлен")
		}
	}

	for _, title := range []string{brandMarker, "<title>Кабинет</title>"} {
		f := &brandMini{ui: WebUIDTO{Design: designMinimal, Name: "Acme"}, title: title}
		w := httptest.NewRecorder()
		brandServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
		body := w.Body.String()
		if !strings.Contains(body, "<title>"+html.EscapeString(title)+"</title>") {
			t.Fatalf("заголовок %q подставлен не текстом", title)
		}
		if strings.Count(body, `"name":"Acme"`) != 1 {
			t.Fatalf("заголовок %q: настройки оформления подставлены не один раз", title)
		}
	}
}

// Свой заголовок классическая страница держит и в разметке, и в строках
// скрипта: знаки, значимые там или там, скрипт не ломают и видны как есть.
func TestCabinetTitleSafeInScript(t *testing.T) {
	const title = "O'Neil & \"Co\" <b> `x` ${y} \\\nвторая\\"
	const want = "O&#39;Neil &amp; &#34;Co&#34; &lt;b&gt; `x` ${y} &#92; вторая&#92;"
	f := &brandMini{ui: WebUIDTO{Design: "classic"}, title: title}
	w := httptest.NewRecorder()
	brandServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
	body := w.Body.String()
	for _, part := range []string{"<title>" + want + "</title>", "<h1>" + want + "</h1>", "h1.innerHTML='" + want + "'", `class="title">` + want + "</div>"} {
		if !strings.Contains(body, part) {
			t.Fatalf("в классической странице нет %s", part)
		}
	}
	// Шапку страница ставит в двух местах, оба — разметкой.
	if n := strings.Count(body, "h1.innerHTML='"+want+"'"); n != 2 {
		t.Fatalf("шапка ставится разметкой в %d местах из 2", n)
	}
	if strings.Contains(body, "textContent='"+want) || strings.Contains(body, "O'Neil") || strings.Contains(body, "вторая\\") {
		t.Fatal("заголовок попал в страницу в виде, ломающем скрипт")
	}

	f.ui.Design = designMinimal
	w = httptest.NewRecorder()
	brandServer(f).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/cabinet/", nil))
	body = w.Body.String()
	js, _ := json.Marshal("O'Neil & \"Co\" <b> `x` ${y} \\ вторая\\")
	if !strings.Contains(body, "<title>"+want+"</title>") || !strings.Contains(body, `"title":`+string(js)) {
		t.Fatal("заголовок нового дизайна подставлен иначе")
	}
}
