package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
func (f *brandMini) CabinetDescription() string                    { return "" }
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
// настройками, классический — прежнюю страницу байт в байт.
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
	if w.Body.String() != string(classic) {
		t.Fatal("классический дизайн обязан отдаваться без изменений")
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
