package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// WebUIDTO — выбранный дизайн мини-аппа и кабинета и брендирование страниц.
// Отдаётся прямо в HTML (см. injectBrand): шапка с логотипом и фирменный цвет
// нужны до первого запроса к API, иначе страница моргает чужим оформлением.
type WebUIDTO struct {
	Design string `json:"design"`
	Theme  string `json:"theme"`
	Name   string `json:"name,omitempty"`
	Accent string `json:"accent,omitempty"`
	// Logo/LogoDark — адрес картинки: загруженная в бота отдаётся с
	// /brand/logo, заданная ссылкой — как есть.
	Logo     string `json:"logo,omitempty"`
	LogoDark string `json:"logo_dark,omitempty"`
	// Lang — язык бота: экран входа кабинета рисуется до того, как известен
	// язык человека.
	Lang string `json:"lang,omitempty"`
	// Title — заголовок кабинета из его настроек (только для кабинета).
	Title string `json:"title,omitempty"`
	// Surface — "miniapp" или "cabinet".
	Surface string `json:"surface,omitempty"`
}

// Дизайны веб-интерфейса (зеркало model.WebDesign*: web не зависит от model).
const (
	designMinimal = "minimal"
)

// brandMarker — место в странице, куда сервер подставляет WebUIDTO.
const brandMarker = "/*BRAND*/{}"

// injectBrand подставляет настройки оформления в страницу. Страница без маркера
// (классический дизайн, свой дизайн оператора) возвращается как есть.
// json.Marshal экранирует <, > и & — значение безопасно внутри <script>.
func injectBrand(page []byte, ui WebUIDTO) []byte {
	if !bytes.Contains(page, []byte(brandMarker)) {
		return page
	}
	js, err := json.Marshal(ui)
	if err != nil {
		return page
	}
	return bytes.Replace(page, []byte(brandMarker), js, 1)
}

// hasBrandMarker — страница нового образца: оформление она берёт из
// подставленных настроек, а не из замен текста в разметке.
func hasBrandMarker(page []byte) bool { return bytes.Contains(page, []byte(brandMarker)) }

// SniffImage определяет тип картинки логотипа по содержимому. Допускаются
// только PNG, JPEG, GIF, WebP и SVG: всё прочее браузер мог бы истолковать
// как страницу.
func SniffImage(data []byte) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct, true
	}
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	h := strings.ToLower(strings.TrimSpace(string(bytes.TrimPrefix(head, []byte("\xef\xbb\xbf")))))
	if strings.HasPrefix(h, "<svg") || (strings.HasPrefix(h, "<?xml") || strings.HasPrefix(h, "<!--")) && strings.Contains(h, "<svg") {
		return "image/svg+xml", true
	}
	return "", false
}

// handleBrandLogo отдаёт логотип, загруженный в бота. Картинка публичная — это
// лицо сервиса на его же страницах, — но отдаётся с песочницей CSP: SVG,
// открытый по прямой ссылке, иначе исполнял бы свои скрипты на домене
// кабинета, где лежит пропуск входа.
func (s *Server) handleBrandLogo(w http.ResponseWriter, r *http.Request) {
	if s.mini == nil || (!s.mini.MiniEnabled() && !s.mini.CabinetEnabled()) {
		http.NotFound(w, r)
		return
	}
	dark := r.URL.Path == "/brand/logo-dark"
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	data, ok := s.mini.BrandLogo(ctx, dark)
	if !ok {
		http.NotFound(w, r)
		return
	}
	ct, ok := SniffImage(data)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "public, max-age=300")
	// #nosec G705 -- отдаётся только картинка, прошедшая SniffImage, с явным типом, nosniff и CSP-песочницей
	_, _ = w.Write(data)
}
