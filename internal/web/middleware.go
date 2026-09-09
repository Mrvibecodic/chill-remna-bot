package web

import (
	"net/http"
	"strings"
	"time"
)

// Общая обёртка вокруг всего маршрутизатора.
//
// Раньше всё «поперечное» — заголовки безопасности, требование HTTPS,
// ограничение частоты — вызывалось руками внутри отдельных обработчиков, и
// покрытие вышло дырявым: заголовки стояли на шести маршрутах из тридцати
// двух, HTTPS на пяти, лимитер на трёх. Причём именно те ручки, которые носят
// ключ доступа, ссылку на подписку и чек об оплате, были не закрыты ничем.
//
// Теперь это одна цепочка на весь маршрутизатор, а исключения перечислены
// явно и в одном месте.

// noHTTPSPaths — куда требование HTTPS не применяется.
//
// Вебхуки платёжек ходят внутрь сети и на 426 уйдут в суточные повторы;
// healthz дёргает docker изнутри контейнера.
func noHTTPSPaths(path string) bool {
	return strings.HasPrefix(path, "/webhook/") || path == "/healthz"
}

// framable — страницы, которые Telegram намеренно показывает во фрейме.
// Кабинет фреймить нельзя, мини-апп — нужно.
func framable(path string) bool {
	return strings.HasPrefix(path, "/miniapp/") || strings.HasPrefix(path, "/api/miniapp/")
}

// rlBucket — какой лимит применить к маршруту. Пустая строка — без лимита.
func rlBucket(r *http.Request) string {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/webhook/"), p == "/healthz", p == "/robots.txt":
		// Вебхуки лимитировать нельзя категорически: на 429 провайдеры уходят
		// в суточные повторы, а healthz дёргает оркестратор.
		return ""
	case strings.HasPrefix(p, "/api/cabinet/auth/"), p == "/api/miniapp/auth",
		strings.HasPrefix(p, "/api/cabinet/password/"),
		strings.HasPrefix(p, "/api/cabinet/email/"),
		strings.HasPrefix(p, "/api/cabinet/tg/"):
		// Вход и обмен подписи: подбор пароля и бесплатный расход процессора
		// на проверке подписи. Здесь же ссылки из писем и смена пароля: перебор
		// значения ссылки, подбор старого пароля и — отдельной статьёй — чужой
		// почтовый сервер, с которого нас попросят, если превратить отправку в
		// бесплатную рассылку.
		return "auth"
	case p == "/api/miniapp/promo",
		p == "/api/miniapp/checkout",
		p == "/api/miniapp/topup",
		p == "/api/miniapp/trial",
		p == "/api/cabinet/p2p/screenshot",
		p == "/api/miniapp/devices/reset":
		// Деньги и дорогие действия: перебор промокодов — это прямой убыток,
		// чек весит до двенадцати мегабайт, а сброс устройств бьёт по панели.
		return "money"
	case strings.HasPrefix(p, "/api/"):
		return "read"
	}
	return ""
}

// wrap навешивает на маршрутизатор заголовки, требование HTTPS и лимитер.
func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secure := isSecure(r)
		path := r.URL.Path

		// Заголовки ставятся всем и до записи тела. Единственная развилка —
		// запрет фрейма: мини-апп обязан открываться внутри Telegram.
		if !strings.HasPrefix(path, "/webhook/") {
			s.setSecurityHeaders(w, !framable(path), secure)
		}

		if !secure && !noHTTPSPaths(path) && !s.allowPlainHTTP {
			if r.Method == http.MethodGet && !strings.HasPrefix(path, "/api/") {
				http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
				return
			}
			writeJSON(w, http.StatusUpgradeRequired, map[string]string{"error": "требуется HTTPS"})
			return
		}

		if b := rlBucket(r); b != "" {
			if lim := s.limiterFor(b); lim != nil && !lim.allow(b+"|"+clientIP(r)) {
				w.Header().Set("Retry-After", "60")
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "слишком часто, попробуйте позже"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// limiterFor выдаёт лимитер по имени корзины.
func (s *Server) limiterFor(bucket string) *rateLimiter {
	switch bucket {
	case "auth":
		return s.authLimiter
	case "money":
		return s.moneyLimiter
	case "read":
		return s.readLimiter
	}
	return nil
}

// Пороги корзин.
//
// read обязан пережить обычную загрузку страницы: мини-апп на старте залпом
// тянет полдюжины ручек, и слишком тесный лимит ловил бы 429 на честной
// перезагрузке.
const (
	authLimitHits   = 15
	authLimitWindow = 5 * time.Minute

	moneyLimitHits   = 10
	moneyLimitWindow = time.Minute

	readLimitHits   = 120
	readLimitWindow = time.Minute
)
