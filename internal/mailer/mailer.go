// Package mailer отправляет письма кабинета: подтверждение адреса и сброс
// пароля.
//
// Два способа доставки намеренно. SMTP закрывает и собственный сервер, и любой
// коммерческий релей — у всех он есть, а значит одного этого канала хватает
// почти везде. «Внешний API» нужен там, где хостер закрыл исходящие 25/465/587
// наглухо и остаётся только HTTPS.
//
// Пакет ничего не знает ни о кабинете, ни о конфиге бота: на вход — настройки
// и письмо, на выход — ошибка. Это позволяет проверять его без сети.
package mailer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Способы доставки.
const (
	ModeSMTP = "smtp"
	ModeAPI  = "api"
)

// Шифрование соединения с почтовым сервером.
const (
	TLSStartTLS = "starttls"
	TLSImplicit = "tls"
	TLSNone     = "none"
)

// Config — настройки доставки. Заполняется из конфига бота.
type Config struct {
	Mode     string
	From     string
	FromName string

	Host     string
	Port     int
	User     string
	Password string
	TLS      string

	APIKey string
	APIURL string
}

// Message — письмо. HTML необязателен: без него уходит обычный текст.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// dialTimeout — предел на установку соединения, sendTimeout — на весь разговор
// с сервером. Оба нужны жёсткими: отправка идёт из HTTP-обработчика, у
// которого свой дедлайн записи, и зависший почтовый сервер не должен утаскивать
// за собой ответ человеку.
const (
	dialTimeout = 10 * time.Second
	sendTimeout = 25 * time.Second
)

// ErrNotConfigured — настроек не хватает даже на попытку отправки.
var ErrNotConfigured = errors.New("почта не настроена")

// Send отправляет письмо выбранным способом.
func Send(ctx context.Context, cfg Config, msg Message) error {
	if err := validate(cfg, msg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if cfg.Mode == ModeAPI {
		return sendAPI(ctx, cfg, msg)
	}
	return sendSMTP(ctx, cfg, msg)
}

func validate(cfg Config, msg Message) error {
	if cfg.From == "" {
		return ErrNotConfigured
	}
	if cfg.Mode == ModeAPI {
		if cfg.APIKey == "" {
			return ErrNotConfigured
		}
	} else if cfg.Host == "" {
		return ErrNotConfigured
	}
	// Подстановка заголовков: адрес и тема приходят из пользовательского ввода
	// (адрес — прямо из формы регистрации). Перевод строки внутри значения
	// превращает одно поле в несколько и позволяет дописать чужие получателей
	// и своё тело письма.
	for _, v := range []string{cfg.From, cfg.FromName, msg.To, msg.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("перевод строки в заголовке письма")
		}
	}
	if !validAddress(msg.To) {
		return fmt.Errorf("неверный адрес получателя")
	}
	if !validAddress(cfg.From) {
		return fmt.Errorf("неверный адрес отправителя")
	}
	if msg.Text == "" && msg.HTML == "" {
		return errors.New("пустое письмо")
	}
	return nil
}

// validAddress — минимальная проверка адреса: ровно одна собака, непустые
// части, без пробелов и управляющих символов. Полный разбор RFC 5322 здесь не
// нужен и вреден: он принимает экзотику, которую почтовые серверы всё равно
// отвергают.
func validAddress(a string) bool {
	if len(a) < 3 || len(a) > 254 {
		return false
	}
	at := strings.IndexByte(a, '@')
	if at <= 0 || at != strings.LastIndexByte(a, '@') || at == len(a)-1 {
		return false
	}
	domain := a[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	for i := 0; i < len(a); i++ {
		c := a[i]
		if c <= ' ' || c == 127 || c == '"' || c == ',' || c == ';' || c == '<' || c == '>' {
			return false
		}
	}
	return true
}

// ValidAddress — та же проверка для вызывающих: адрес, который отправить не
// удастся, незачем принимать при регистрации.
func ValidAddress(a string) bool { return validAddress(a) }

// hostPort собирает адрес сервера.
func (c Config) hostPort() string {
	port := c.Port
	if port <= 0 {
		if c.TLS == TLSImplicit {
			port = 465
		} else {
			port = 587
		}
	}
	return fmt.Sprintf("%s:%d", c.Host, port)
}
