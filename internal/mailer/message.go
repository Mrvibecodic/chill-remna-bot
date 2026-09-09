package mailer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"strings"
	"time"
)

// buildMessage собирает письмо по RFC 5322.
//
// Тело всегда base64: письма кириллические, а quoted-printable пришлось бы
// писать руками и следить за длиной строк. Когда есть и текст, и HTML — это
// multipart/alternative: почтовики без HTML показывают текстовую часть, а
// письмо из одной только HTML-части чаще попадает в спам.
func buildMessage(cfg Config, msg Message) []byte {
	var b strings.Builder
	from := cfg.From
	if cfg.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", cfg.FromName) + " <" + cfg.From + ">"
	}
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + messageID(cfg.From) + ">\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	// Письмо служебное: автоответчики «меня нет в офисе» и списки рассылки не
	// должны отвечать на него и уж точно не должны его пересылать.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("X-Auto-Response-Suppress: All\r\n")

	switch {
	case msg.HTML == "":
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(b64Body(msg.Text))
	case msg.Text == "":
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(b64Body(msg.HTML))
	default:
		boundary := randomHex(16)
		b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(b64Body(msg.Text))
		b.WriteString("\r\n--" + boundary + "\r\n")
		b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		b.WriteString(b64Body(msg.HTML))
		b.WriteString("\r\n--" + boundary + "--\r\n")
	}
	return []byte(b.String())
}

// b64Body режет base64 на строки по 76 символов — предел длины строки в RFC.
func b64Body(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var out strings.Builder
	for len(enc) > 76 {
		out.WriteString(enc[:76])
		out.WriteString("\r\n")
		enc = enc[76:]
	}
	out.WriteString(enc)
	return out.String()
}

func messageID(from string) string {
	domain := "localhost"
	if i := strings.LastIndexByte(from, '@'); i >= 0 && i < len(from)-1 {
		domain = from[i+1:]
	}
	return fmt.Sprintf("%s.%s@%s", randomHex(8), randomHex(8), domain)
}

// randomHex — случайные байты в hex. Значение косметическое (граница частей,
// идентификатор письма), поэтому редкая ошибка чтения не должна ронять
// отправку: подставляется время.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
