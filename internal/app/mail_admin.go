package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/assets"
	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Экран «Почта» в системных настройках: чем и от кого кабинет шлёт письма.

func (a *App) showMailAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	m := a.mailCfg()

	state := i18n.T(lang, "cabinet.off")
	toggle := i18n.T(lang, "mail.btn_on")
	if m.Enabled {
		state = i18n.T(lang, "cabinet.on")
		toggle = i18n.T(lang, "mail.btn_off")
	}
	mode := i18n.T(lang, "mail.mode_smtp")
	if m.Mode == model.MailModeAPI {
		mode = i18n.T(lang, "mail.mode_api")
	}
	dash := func(s string) string {
		if s == "" {
			return "—"
		}
		return escapeName(s)
	}
	set := func(s string) string {
		if s == "" {
			return i18n.T(lang, "user.no")
		}
		return i18n.T(lang, "user.yes")
	}
	text := i18n.T(lang, "mail.title", state, mode)
	text += "\n" + i18n.T(lang, "mail.from", dash(m.From), dash(m.FromName))
	if m.Mode == model.MailModeAPI {
		text += "\n" + i18n.T(lang, "mail.api", dash(firstNonEmpty(m.APIURL, model.DefaultMailAPIURL)), set(m.APIKey))
	} else {
		text += "\n" + i18n.T(lang, "mail.smtp", dash(m.Host), strconv.Itoa(m.Port), dash(m.User), set(m.Password), m.TLS)
	}
	if !m.MailReady() {
		text += "\n\n" + i18n.T(lang, "mail.incomplete")
	}
	if a.cabinetURL() == "" {
		// Без публичного адреса ссылку в письмо не на что повесить, и письмо
		// становится бессмысленным ещё до отправки.
		text += "\n\n" + i18n.T(lang, "mail.no_public_url")
	}
	text += "\n\n" + i18n.T(lang, "mail.hint")

	rows := [][]models.InlineKeyboardButton{
		{btn(toggle, "menu:mailtoggle"), btn(i18n.T(lang, "mail.btn_mode"), "menu:mailmode")},
		{btn(i18n.T(lang, "mail.btn_from"), "menu:mailfrom"), btn(i18n.T(lang, "mail.btn_fromname"), "menu:mailfromname")},
	}
	if m.Mode == model.MailModeAPI {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "mail.btn_apiurl"), "menu:mailapiurl"),
			btn(i18n.T(lang, "mail.btn_apikey"), "menu:mailapikey"),
		})
	} else {
		rows = append(rows,
			[]models.InlineKeyboardButton{
				btn(i18n.T(lang, "mail.btn_host"), "menu:mailhost"),
				btn(i18n.T(lang, "mail.btn_tls")+": "+m.TLS, "menu:mailtls"),
			},
			[]models.InlineKeyboardButton{
				btn(i18n.T(lang, "mail.btn_user"), "menu:mailuser"),
				btn(i18n.T(lang, "mail.btn_pass"), "menu:mailpass"),
			})
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "mail.btn_test"), "menu:mailtest")},
		[]models.InlineKeyboardButton{
			btn(i18n.T(lang, "btn.back"), "menu:system"),
			btn(i18n.T(lang, "btn.home"), "menu:home"),
		})
	a.sendKBSection(ctx, chatID, assets.SectionAdminStats, text, rows)
}

func (a *App) toggleMail(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.Mail.Enabled = !a.botCfg.Mail.Enabled
		a.botCfg.NormalizeMail()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showMailAdmin(ctx, chatID)
}

// cycleMailMode переключает способ доставки, cycleMailTLS — шифрование.
// Порт при смене шифрования пересчитывается сам (NormalizeMail): держать в
// голове «465 или 587» админ не обязан.
func (a *App) cycleMailMode(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		if a.botCfg.Mail.Mode == model.MailModeAPI {
			a.botCfg.Mail.Mode = model.MailModeSMTP
		} else {
			a.botCfg.Mail.Mode = model.MailModeAPI
		}
		a.botCfg.NormalizeMail()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showMailAdmin(ctx, chatID)
}

func (a *App) cycleMailTLS(ctx context.Context, chatID int64) {
	a.mu.Lock()
	if a.botCfg != nil {
		switch a.botCfg.Mail.TLS {
		case model.MailTLSStartTLS:
			a.botCfg.Mail.TLS = model.MailTLSImplicit
		case model.MailTLSImplicit:
			a.botCfg.Mail.TLS = model.MailTLSNone
		default:
			a.botCfg.Mail.TLS = model.MailTLSStartTLS
		}
		// Порт пересчитывается только если он был «типовым» для прежнего
		// шифрования: нестандартный порт админ вводил руками, и затирать его
		// сменой тумблера нельзя.
		if a.botCfg.Mail.Port == 465 || a.botCfg.Mail.Port == 587 {
			a.botCfg.Mail.Port = 0
		}
		a.botCfg.NormalizeMail()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showMailAdmin(ctx, chatID)
}

// setMailField записывает одно поле настроек почты.
//
// Хост принимается в виде «сервер» или «сервер:порт» — вводить порт отдельной
// кнопкой значит спрашивать два ответа там, где почтовые сервисы всегда пишут
// их одной строкой.
func (a *App) setMailField(ctx context.Context, chatID int64, field, val string) {
	val = strings.TrimSpace(val)
	if val == "-" {
		val = ""
	}
	a.mu.Lock()
	if a.botCfg != nil {
		m := &a.botCfg.Mail
		switch field {
		case "from":
			m.From = val
		case "fromname":
			m.FromName = val
		case "host":
			host := val
			if i := strings.LastIndexByte(val, ':'); i > 0 {
				if p, err := strconv.Atoi(val[i+1:]); err == nil && p > 0 && p <= 65535 {
					host, m.Port = val[:i], p
				}
			}
			m.Host = host
		case "user":
			m.User = val
		case "pass":
			m.Password = val
		case "apiurl":
			m.APIURL = val
		case "apikey":
			m.APIKey = val
		}
		a.botCfg.NormalizeMail()
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showMailAdmin(ctx, chatID)
}

// sendTestMail отправляет пробное письмо на адрес отправителя.
//
// Именно на него: другого адреса, про который бот точно знает, что он админский,
// у него нет, а спрашивать ещё один — лишний шаг. Ошибка показывается целиком:
// в ней вся диагностика (не тот порт, отказ по паролю, домен не подтверждён).
func (a *App) sendTestMail(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	m := a.mailCfg()
	if !m.MailReady() {
		a.sendHome(ctx, chatID, i18n.T(lang, "mail.incomplete"))
		return
	}
	brand := a.cabinetBrand(lang)
	err := a.sendMail(ctx, m.From, i18n.T(lang, "mail.test_subject", brand),
		i18n.T(lang, "mail.test_text", brand), mailHTML(brand, i18n.T(lang, "mail.test_text", brand), "", "", ""))
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "mail.test_fail", escapeName(err.Error())))
		return
	}
	a.sendHome(ctx, chatID, i18n.T(lang, "mail.test_ok", escapeName(m.From)))
}
