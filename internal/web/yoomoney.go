package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ЮMoney принимает оплату только POST-формой на своём адресе, а кнопка в
// Telegram и мини-апп умеют открывать лишь ссылку. Поэтому бот отдаёт
// крошечную страницу, которая сама отправляет форму (при выключенных
// скриптах — кнопкой). Тем же способом открывается страница разрешения
// доступа к кошельку.

// relaySubmitJS — единственный скрипт страницы; его хэш зашит в CSP, так что
// ничего другого на странице выполниться не может.
const relaySubmitJS = "document.getElementById('f').submit();"

var relaySubmitHash = func() string {
	sum := sha256.Sum256([]byte(relaySubmitJS))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// writeRelayForm отдаёт автоотправляемую форму на action с полями fields.
func writeRelayForm(w http.ResponseWriter, action string, fields [][2]string, title, button string) {
	u, err := url.Parse(action)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		http.Error(w, "bad action", http.StatusInternalServerError)
		return
	}
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><meta name=\"robots\" content=\"noindex\"><title>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</title><style>body{font-family:system-ui,sans-serif;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center;background:#f6f7f9;color:#1c1c1e}main{text-align:center;padding:24px}button{font:inherit;padding:12px 24px;border:0;border-radius:12px;background:#8b3ffd;color:#fff;cursor:pointer}p{color:#6b6b70}</style></head><body><main><form id=\"f\" method=\"post\" action=\"")
	b.WriteString(html.EscapeString(action))
	b.WriteString("\">")
	for _, f := range fields {
		b.WriteString("<input type=\"hidden\" name=\"")
		b.WriteString(html.EscapeString(f[0]))
		b.WriteString("\" value=\"")
		b.WriteString(html.EscapeString(f[1]))
		b.WriteString("\">")
	}
	b.WriteString("<p>")
	b.WriteString(html.EscapeString(title))
	b.WriteString("</p><button type=\"submit\">")
	b.WriteString(html.EscapeString(button))
	b.WriteString("</button></form><script>")
	b.WriteString(relaySubmitJS)
	b.WriteString("</script></main></body></html>")

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src "+relaySubmitHash+"; form-action "+u.Scheme+"://"+u.Host+"; base-uri 'none'; frame-ancestors 'none'")
	_, _ = w.Write([]byte(b.String()))
}

// writeNotice — простая страница с текстом (итог авторизации, «счёт не найден»).
func writeNotice(w http.ResponseWriter, code int, text string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.WriteHeader(code)
	_, _ = w.Write([]byte("<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><meta name=\"robots\" content=\"noindex\"><title>ЮMoney</title><style>body{font-family:system-ui,sans-serif;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center;background:#f6f7f9;color:#1c1c1e}main{max-width:520px;padding:24px;text-align:center;line-height:1.5}</style></head><body><main>" +
		html.EscapeString(text) + "</main></body></html>"))
}

// handleYooMoney — HTTP-уведомление о переводе. Приём подтверждается только
// кодом 200; на 401 (чужая подпись) и 500 ЮMoney повторит доставку.
func (s *Server) handleYooMoney(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	handled, err := s.handlers.HandleYooMoneyWebhook(ctx, r.PostForm)
	if errors.Is(err, ErrUnauthorized) {
		s.log.Warn("yoomoney webhook", "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if errors.Is(err, ErrRetryLater) {
		s.log.Warn("yoomoney webhook: retry later", "err", err)
		http.Error(w, "retry later", http.StatusInternalServerError)
		return
	}
	if err != nil {
		s.log.Error("yoomoney webhook", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if !handled {
		s.log.Info("yoomoney webhook ignored")
	}
	w.WriteHeader(http.StatusOK)
}

// handleYooMoneyPay — страница оплаты счёта: форма quickpay с суммой и меткой
// из строки счёта. Путь под /webhook/ не лимитируется и не требует HTTPS от
// прокси (см. middleware) — страница сама ставит заголовки безопасности.
func (s *Server) handleYooMoneyPay(w http.ResponseWriter, r *http.Request) {
	label := r.PathValue("label")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	action, fields, ok := s.handlers.YooMoneyPayForm(ctx, label)
	if !ok {
		writeNotice(w, http.StatusNotFound, "Счёт не найден или уже оплачен. Вернитесь в бот и начните оплату заново. / Invoice not found or already paid — go back to the bot and start over.")
		return
	}
	writeRelayForm(w, action, fields, "Переходим к оплате в ЮMoney… / Redirecting to YooMoney…", "Перейти к оплате / Continue")
}

// ymStateCookie — состояние ссылки «Авторизовать»: страница /auth кладёт его
// браузеру, редирект с кодом приносит обратно. ЮMoney своего state не
// передаёт, а без привязки любой GET на /oauth гасил бы ожидание админа.
const ymStateCookie = "ym_s"

// handleYooMoneyAuth — страница запроса разрешения на доступ к кошельку
// (открывается по одноразовой ссылке из админки).
func (s *Server) handleYooMoneyAuth(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("s")
	action, fields, ok := s.handlers.YooMoneyAuthForm(state)
	if !ok {
		writeNotice(w, http.StatusNotFound, "Ссылка устарела. Нажмите «Авторизовать» в боте ещё раз. / The link has expired — press “Authorize” in the bot again.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: ymStateCookie, Value: state, Path: "/webhook/yoomoney/",
		MaxAge: 900, HttpOnly: true, Secure: isSecure(r), SameSite: http.SameSiteLaxMode,
	})
	writeRelayForm(w, action, fields, "Переходим к ЮMoney для разрешения доступа… / Redirecting to YooMoney…", "Продолжить / Continue")
}

// handleYooMoneyOAuth — возврат из ЮMoney с кодом авторизации. Бюджет —
// два похода в ЮMoney (обмен кода и номер кошелька), код одноразовый.
func (s *Server) handleYooMoneyOAuth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := ""
	if c, err := r.Cookie(ymStateCookie); err == nil {
		state = c.Value
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	msg := s.handlers.HandleYooMoneyOAuth(ctx, state, q.Get("code"), q.Get("error"))
	http.SetCookie(w, &http.Cookie{Name: ymStateCookie, Value: "", Path: "/webhook/yoomoney/", MaxAge: -1, HttpOnly: true, Secure: isSecure(r), SameSite: http.SameSiteLaxMode})
	writeNotice(w, http.StatusOK, msg)
}
