package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html"
	"net/url"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/mailer"
	"remnabot/internal/model"
)

// Письма кабинета: подтверждение адреса, сброс пароля, уведомление о смене
// пароля. Ссылки одноразовые, живут ограниченное время и хранятся отпечатком.

const (
	// verifyTTL — сутки: письмо могут открыть не сразу, а с телефона вечером.
	verifyTTL = 24 * time.Hour
	// resetTTL — час: ссылка на смену пароля опаснее, чем на подтверждение.
	resetTTL = time.Hour
	// emailTokenRetention — сколько держим погашенные и просроченные строки.
	// Не меньше суток порога отправки, иначе счётчик писем обнулялся бы уборкой.
	emailTokenRetention = 7 * 24 * time.Hour
)

// Порог отправки писем на один аккаунт. Считается по выданным ссылкам, а не по
// удачным отправкам: иначе провайдер, который принимает письмо и молча его
// выбрасывает, позволял бы слать бесконечно.
const (
	mailResendGap = time.Minute
	mailDailyCap  = 5
)

var (
	errMailOff       = errors.New("отправка писем не настроена")
	errMailTooOften  = errors.New("письмо уже отправлено — подождите минуту")
	errMailTooMany   = errors.New("слишком много писем за сутки, попробуйте завтра")
	errMailNoAddress = errors.New("публичный адрес кабинета не задан")
)

// waitMail дожидается фоновых отправок. Нужен тестам: без него проверка идёт
// наперегонки с письмом, которое ушло в фон при регистрации.
func (a *App) waitMail() { a.mailWG.Wait() }

func (a *App) mailCfg() model.MailConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.MailConfig{}
	}
	return a.botCfg.Mail
}

// MailReady — можно ли вообще отправить письмо. По нему кабинет решает,
// показывать ли «подтвердить почту» и «забыли пароль»: предлагать действие,
// которое заведомо не сработает, хуже, чем не предлагать вовсе.
func (a *App) MailReady() bool { return a.mailCfg().MailReady() }

func mailerConfig(m model.MailConfig) mailer.Config {
	return mailer.Config{
		Mode:     m.Mode,
		From:     m.From,
		FromName: m.FromName,
		Host:     m.Host,
		Port:     m.Port,
		User:     m.User,
		Password: m.Password,
		TLS:      m.TLS,
		APIKey:   m.APIKey,
		APIURL:   m.APIURL,
	}
}

func (a *App) sendMail(ctx context.Context, to, subject, text, htmlBody string) error {
	m := a.mailCfg()
	if !m.MailReady() {
		return errMailOff
	}
	return mailer.Send(ctx, mailerConfig(m), mailer.Message{To: to, Subject: subject, Text: text, HTML: htmlBody})
}

// cabinetBrand — как кабинет называет себя в письме. Заголовок кабинета задаёт
// админ; пустой заменяется нейтральным, чтобы письмо не приходило безымянным.
func (a *App) cabinetBrand(lang string) string {
	if t := strings.TrimSpace(a.cabinetCfg().Title); t != "" {
		return t
	}
	return i18n.T(lang, "mail.brand")
}

// newEmailToken выдаёт одноразовую ссылку и гасит прежние того же назначения.
// Возвращает само значение — в базе остаётся только его отпечаток.
func (a *App) newEmailToken(ctx context.Context, tgID int64, email, purpose string, ttl time.Duration) (string, error) {
	if a.store == nil {
		return "", errors.New("хранилище недоступно")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// Здесь подстановка предсказуемого значения недопустима: это ключ от
		// чужого кабинета.
		return "", errors.New("не удалось создать ссылку")
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	_ = a.store.RevokeEmailTokens(ctx, tgID, purpose)
	now := time.Now().UTC()
	if err := a.store.PutEmailToken(ctx, &model.EmailToken{
		Hash:      emailTokenHash(tok),
		TgID:      tgID,
		Purpose:   purpose,
		Email:     email,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(ttl).Format(time.RFC3339),
	}); err != nil {
		return "", err
	}
	return tok, nil
}

func emailTokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// mailQuota проверяет порог отправки для аккаунта.
func (a *App) mailQuota(ctx context.Context, tgID int64, purpose string) error {
	if a.store == nil {
		return errors.New("хранилище недоступно")
	}
	now := time.Now().UTC()
	recent, err := a.store.CountEmailTokensSince(ctx, tgID, purpose, now.Add(-mailResendGap).Format(time.RFC3339))
	if err != nil {
		return err
	}
	if recent > 0 {
		return errMailTooOften
	}
	day, err := a.store.CountEmailTokensSince(ctx, tgID, purpose, now.Add(-24*time.Hour).Format(time.RFC3339))
	if err != nil {
		return err
	}
	if day >= mailDailyCap {
		return errMailTooMany
	}
	return nil
}

// cabinetLink собирает ссылку из письма: адрес кабинета плюс параметр с
// одноразовым значением. Разбирает его сам кабинет и отправляет обычным
// запросом — GET-ссылку на изменение состояния делать нельзя, её открывают
// почтовые антивирусы и «проверялки ссылок», гася ссылку до человека.
func (a *App) cabinetLink(param, tok string) string {
	base := a.cabinetURL()
	if base == "" {
		return ""
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + param + "=" + url.QueryEscape(tok)
}

// sendVerifyMail отправляет письмо с подтверждением адреса.
func (a *App) sendVerifyMail(ctx context.Context, tgID int64, email string) error {
	if !a.MailReady() {
		return errMailOff
	}
	// Публичный адрес проверяется ПЕРВЫМ: без него ссылку строить не из чего, а
	// сгоревшая на этом попытка ещё и съедала бы порог отправки — человек потом
	// минуту не мог бы попробовать снова.
	if a.cabinetURL() == "" {
		return errMailNoAddress
	}
	if err := a.mailQuota(ctx, tgID, model.EmailPurposeVerify); err != nil {
		return err
	}
	tok, err := a.newEmailToken(ctx, tgID, email, model.EmailPurposeVerify, verifyTTL)
	if err != nil {
		return err
	}
	link := a.cabinetLink("verify", tok)
	if link == "" {
		return errMailNoAddress
	}
	lang := a.lang(a.cfg.AdminID)
	brand := a.cabinetBrand(lang)
	subject := i18n.T(lang, "mail.verify_subject", brand)
	text := i18n.T(lang, "mail.verify_text", brand, link, int(verifyTTL.Hours()))
	return a.sendMail(ctx, email, subject, text, mailHTML(brand, i18n.T(lang, "mail.verify_html"), link, i18n.T(lang, "mail.verify_btn"), i18n.T(lang, "mail.ignore")))
}

// sendResetMail отправляет письмо со ссылкой на смену забытого пароля.
func (a *App) sendResetMail(ctx context.Context, tgID int64, email string) error {
	if !a.MailReady() {
		return errMailOff
	}
	// Публичный адрес проверяется ПЕРВЫМ: без него ссылку строить не из чего, а
	// сгоревшая на этом попытка ещё и съедала бы порог отправки — человек потом
	// минуту не мог бы попробовать снова.
	if a.cabinetURL() == "" {
		return errMailNoAddress
	}
	if err := a.mailQuota(ctx, tgID, model.EmailPurposeReset); err != nil {
		return err
	}
	tok, err := a.newEmailToken(ctx, tgID, email, model.EmailPurposeReset, resetTTL)
	if err != nil {
		return err
	}
	link := a.cabinetLink("reset", tok)
	if link == "" {
		return errMailNoAddress
	}
	lang := a.lang(a.cfg.AdminID)
	brand := a.cabinetBrand(lang)
	subject := i18n.T(lang, "mail.reset_subject", brand)
	text := i18n.T(lang, "mail.reset_text", brand, link, int(resetTTL.Minutes()))
	return a.sendMail(ctx, email, subject, text, mailHTML(brand, i18n.T(lang, "mail.reset_html"), link, i18n.T(lang, "mail.reset_btn"), i18n.T(lang, "mail.ignore")))
}

// notifyPasswordChanged — письмо «пароль изменён». Отправляется без порога и
// без ссылки: это уведомление о событии безопасности, и именно оно даёт
// человеку узнать о чужом входе. Ошибка не роняет саму смену пароля.
func (a *App) notifyPasswordChanged(ctx context.Context, email string) {
	if !a.MailReady() || email == "" {
		return
	}
	lang := a.lang(a.cfg.AdminID)
	brand := a.cabinetBrand(lang)
	if err := a.sendMail(ctx, email,
		i18n.T(lang, "mail.changed_subject", brand),
		i18n.T(lang, "mail.changed_text", brand),
		mailHTML(brand, i18n.T(lang, "mail.changed_text", brand), "", "", "")); err != nil {
		a.log.Warn("кабинет: уведомление о смене пароля не отправлено", "err", err)
	}
}

// mailHTML — общая обёртка письма. Ни одной внешней картинки и ни одного
// внешнего стиля: письмо не должно тянуть ресурсы с сервера бота и выдавать
// почтовому провайдеру факт прочтения.
func mailHTML(brand, body, link, btn, foot string) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.55;color:#111;max-width:520px;margin:0 auto;padding:24px">`)
	b.WriteString(`<div style="font-weight:600;font-size:18px;margin-bottom:14px">` + html.EscapeString(brand) + `</div>`)
	b.WriteString(`<div>` + html.EscapeString(body) + `</div>`)
	if link != "" {
		b.WriteString(`<div style="margin:22px 0"><a href="` + html.EscapeString(link) +
			`" style="display:inline-block;background:#2563eb;color:#fff;text-decoration:none;padding:11px 20px;border-radius:10px;font-weight:600">` +
			html.EscapeString(btn) + `</a></div>`)
		b.WriteString(`<div style="font-size:12px;color:#666;word-break:break-all">` + html.EscapeString(link) + `</div>`)
	}
	if foot != "" {
		b.WriteString(`<div style="margin-top:18px;font-size:12px;color:#666">` + html.EscapeString(foot) + `</div>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}
