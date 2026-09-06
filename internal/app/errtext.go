package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"remnabot/internal/i18n"
)

// Безопасный текст ошибки для покупателя.
//
// Раньше в чат уходил err.Error() целиком, например:
//
//	«Не удалось выдать подписку: нет связи с панелью: Get
//	"http://127.0.0.1:44617/api/users/by-telegram-id/9530": context deadline exceeded»
//
// — внутренний адрес и порт панели, идентификатор человека и английский текст
// библиотеки. Хуже того, панель умеет возвращать инструкции, адресованные
// АДМИНУ («задайте ключ в переменной CADDY_AUTH_API_TOKEN», «проверьте
// API-token» плюс кусок ответа панели), и они тем же путём доезжали до
// покупателя.
//
// Подробность нужна, но не ему: она идёт в журнал и админу, а человек получает
// короткую причину и код обращения, по которому админ найдёт запись.

// clientErr — что показать покупателю, и заодно запись подробностей в журнал.
//
// Возвращается короткий текст с кодом обращения. Код — первые знаки отпечатка
// самой ошибки: одинаковые сбои дают одинаковый код, и по нему видно, что
// у десяти обратившихся людей одна и та же причина.
func (a *App) clientErr(ctx context.Context, chatID int64, op string, err error) string {
	if err == nil {
		return ""
	}
	// Часть ошибок — это уже готовый текст для человека («пополнение
	// отключено», «оплата картой не настроена»). Прятать их за «что-то пошло
	// не так» — чистая потеря: скрывать там нечего, а причина понятная.
	var ut userText
	if errors.As(err, &ut) {
		a.log.Info(op, "reason", ut.msg, "user", chatID)
		return ut.msg
	}
	code := errCode(err)
	a.log.Error(op, "err", err, "user", chatID, "code", code)
	lang := a.lang(chatID)
	switch errKind(err) {
	case "panel":
		return i18n.T(lang, "err.panel_busy", code)
	case "gateway":
		return i18n.T(lang, "err.pay_gateway", code)
	case "storage":
		return i18n.T(lang, "err.storage")
	}
	return i18n.T(lang, "err.generic", code)
}

// userText — ошибка, текст которой изначально написан для покупателя и
// переведён: её показываем как есть.
type userText struct{ msg string }

func (e userText) Error() string { return e.msg }

// errUserText оборачивает готовый текст для покупателя.
func errUserText(msg string) error { return userText{msg: msg} }

// errKind — грубая классификация: только чтобы выбрать человеческий текст.
func errKind(err error) string {
	msg := strings.ToLower(err.Error())
	// Панель — ПЕРВОЙ: в её ошибку подставляется кусок ответа, а прокси перед
	// панелью отдаёт страницу «502 Bad Gateway». По слову gateway из чужого
	// тела покупателю говорили «платёжный сервис не отвечает» — и он шёл
	// платить другим способом, хотя лежала панель.
	for _, m := range []string{"нет связи с панелью", "панель не подключена", "панель вернула", "панель отклонила", "api панели"} {
		if strings.Contains(msg, m) {
			return "panel"
		}
	}
	for _, m := range []string{"хранилище", "database", "sql", "no such table"} {
		if strings.Contains(msg, m) {
			return "storage"
		}
	}
	// Шлюзы — по тексту, и только потом по типу ошибки: таймаут бывает у кого
	// угодно, а проверка context.DeadlineExceeded первой зачисляла в «панель»
	// самый частый отказ платёжки.
	for _, m := range []string{"шлюз", "gateway", "yookassa", "юkassa", "cryptobot", "platega", "heleket", "tribute"} {
		if strings.Contains(msg, m) {
			return "gateway"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "panel"
	}
	return ""
}

// errCode — короткий отпечаток ошибки для сопоставления чата с журналом.
//
// Числа из текста выбрасываются: в ошибке лежат id человека, порт и адрес,
// и без этой чистки один и тот же сбой у трёх человек давал три разных кода —
// ровно то, чего код не должен делать.
func errCode(err error) string {
	sum := sha256.Sum256([]byte(errFingerprint(err.Error())))
	return strings.ToUpper(hex.EncodeToString(sum[:3]))
}

// errFingerprint приводит текст ошибки к устойчивому виду.
//
// Все числа выбрасываются: в тексте лежат идентификаторы, порты, суммы и
// метки времени, и без чистки один и тот же сбой у трёх человек давал три
// разных кода. Код ответа HTTP при этом сохраняется отдельно: обычно это
// единственное, что отличает «неверный токен» от «сервис лежит».
//
// Длина ограничена: в ошибку панели подставляется кусок её ответа, и без
// обрезки отпечаток зависел бы от случайного содержимого страницы.
func errFingerprint(msg string) string {
	const keep = 64
	low := strings.ToLower(msg)
	var b strings.Builder
	if st := httpStatusIn(low); st != "" {
		b.WriteString("http" + st + "|")
	}
	n := 0
	for _, r := range low {
		if r >= '0' && r <= '9' {
			continue
		}
		b.WriteRune(r)
		if n++; n >= keep {
			break
		}
	}
	return b.String()
}

// httpStatusIn достаёт трёхзначный код ответа после слова http:
// «панель вернула HTTP 502: …», «HTTP/1.1 404 Not Found».
func httpStatusIn(low string) string {
	for i := 0; i+4 <= len(low); i++ {
		if low[i:i+4] != "http" {
			continue
		}
		j := i + 4
		// Версия протокола, если она есть: /1.1
		if j < len(low) && low[j] == '/' {
			j++
			for j < len(low) && (low[j] == '.' || (low[j] >= '0' && low[j] <= '9')) {
				j++
			}
		}
		for j < len(low) && (low[j] == ' ' || low[j] == ':') {
			j++
		}
		if j+3 <= len(low) && isDigits(low[j:j+3]) &&
			(j+3 == len(low) || low[j+3] < '0' || low[j+3] > '9') {
			return low[j : j+3]
		}
	}
	return ""
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
