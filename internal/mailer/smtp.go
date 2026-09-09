package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// sendSMTP отправляет письмо обычным SMTP.
//
// Соединение устанавливается через net.Dialer с таймаутом и с оглядкой на
// контекст: библиотечный smtp.Dial таймаута не знает вовсе, и повисший сервер
// держал бы обработчик до его собственного дедлайна.
func sendSMTP(ctx context.Context, cfg Config, msg Message) error {
	addr := cfg.hostPort()
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("соединение с почтовым сервером: %w", err)
	}
	// Дедлайн на весь разговор. Без него зависший сервер держит нас до
	// собственного таймаута соединения, а он бывает в минутах.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if cfg.TLS == TLSImplicit {
		tc := tls.Client(conn, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("TLS с почтовым сервером: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("почтовый сервер: %w", err)
	}
	defer func() { _ = c.Close() }()

	if cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("сервер не поддерживает STARTTLS — выберите другой способ шифрования")
		}
		if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}

	if cfg.User != "" {
		if cfg.TLS == TLSNone {
			// Пароль почтового ящика в открытом виде по сети не отправляем.
			// Библиотечный PlainAuth отказался бы и сам, но с невнятной
			// ошибкой про «unencrypted connection».
			return errors.New("вход по паролю без шифрования запрещён — включите STARTTLS или TLS")
		}
		auth, err := pickAuth(c, cfg)
		if err != nil {
			return err
		}
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("вход на почтовый сервер: %w", err)
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("отправитель отклонён: %w", err)
	}
	if err := c.Rcpt(msg.To); err != nil {
		return fmt.Errorf("получатель отклонён: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("передача письма: %w", err)
	}
	if _, err := w.Write(buildMessage(cfg, msg)); err != nil {
		_ = w.Close()
		return fmt.Errorf("передача письма: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("сервер не принял письмо: %w", err)
	}
	return c.Quit()
}

// pickAuth выбирает способ входа по тому, что объявил сервер. PLAIN есть почти
// везде; LOGIN нужен серверам вроде Exchange, где PLAIN не объявлен, и без него
// такая установка просто не может отправить ни одного письма.
func pickAuth(c *smtp.Client, cfg Config) (smtp.Auth, error) {
	ext, params := c.Extension("AUTH")
	if !ext {
		return nil, errors.New("сервер не предлагает вход по паролю")
	}
	mechs := strings.ToUpper(params)
	switch {
	case strings.Contains(mechs, "PLAIN"):
		return smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host), nil
	case strings.Contains(mechs, "LOGIN"):
		return &loginAuth{user: cfg.User, pass: cfg.Password, host: cfg.Host}, nil
	case strings.Contains(mechs, "CRAM-MD5"):
		return smtp.CRAMMD5Auth(cfg.User, cfg.Password), nil
	}
	return nil, fmt.Errorf("сервер не поддерживает ни один известный способ входа (%s)", params)
}

// loginAuth — AUTH LOGIN, которого нет в стандартной библиотеке.
type loginAuth struct {
	user, pass, host string
	done             bool
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	// Тот же запрет, что и у PlainAuth: пароль уходит открытым текстом, и без
	// шифрования отдавать его нельзя.
	if !server.TLS {
		return "", nil, errors.New("вход по паролю требует шифрования")
	}
	if server.Name != a.host {
		return "", nil, errors.New("почтовый сервер представился другим именем")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	// Сервер спрашивает логин, потом пароль. Текст приглашения у разных
	// серверов свой, поэтому идём по порядку, а не по его содержимому.
	if !a.done {
		a.done = true
		return []byte(a.user), nil
	}
	return []byte(a.pass), nil
}
