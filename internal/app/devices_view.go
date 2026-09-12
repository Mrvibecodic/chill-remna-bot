package app

import (
	"html"
	"strings"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Поля платформы, ОС и модели панель записывает из заголовков запроса, то есть
// это текст с клиента, а не наш: без обрезки одно устройство способно занять
// весь экран подписки, а при разметке HTML — ещё и сломать сообщение.
const deviceFieldMaxLen = 32

// deviceListMax — потолок числа строк в чате. Лимит устройств у тарифов
// обычно однозначный; потолок здесь на случай панели, где лимита нет вовсе.
const deviceListMax = 10

// deviceCaptionBudget — сколько знаков экрана подписки можно занять, оставаясь
// подписью под баннером: свыше 1000 знаков sendKBSection уходит на обычное
// сообщение и картинка раздела пропадает.
const deviceCaptionBudget = 950

// devicesConfig — набор полей, разрешённых владельцем бота к показу.
func (a *App) devicesConfig() model.DevicesConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.DevicesConfig{}
	}
	return a.botCfg.Devices
}

// cut подрезает чужую строку до разумной длины.
func cutField(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > deviceFieldMaxLen {
		return string(r[:deviceFieldMaxLen]) + "…"
	}
	return s
}

// shortHWID — отпечаток в виде «a1b2…9f3c». Целиком он бесполезен на экране:
// это несколько десятков знаков, по которым всё равно ничего не прочесть, а
// опознать своё устройство хватает и краёв.
func shortHWID(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= 12 {
		return s
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// deviceParts раскладывает устройство на заголовок и приписку по набору полей,
// разрешённому админом. Возвращает сырой текст: чат его экранирует, мини-апп
// экранирует у себя.
func deviceParts(lang string, d remnawave.Device, cfg model.DevicesConfig) (head, meta string) {
	// В заголовке строки — что за устройство: модель и приложение клиента.
	// Модель присылают не все клиенты, приложение — почти все, поэтому вместе
	// они и дают узнаваемое имя. Остальное (платформа, отпечаток, дата) идёт
	// второй строкой.
	var name, tail []string
	if cfg.Model {
		if m := cutField(d.Model); m != "" {
			name = append(name, m)
		}
	}
	if cfg.UA {
		if ua := cutField(d.UserAgent); ua != "" {
			name = append(name, ua)
		}
	}
	platform := strings.TrimSpace(strings.TrimSpace(cutField(d.Platform)) + " " + strings.TrimSpace(cutField(d.OSVersion)))
	if cfg.Platform && platform != "" {
		if len(name) == 0 {
			name = append(name, platform)
		} else {
			tail = append(tail, platform)
		}
	}
	if cfg.HWID && d.HWID != "" {
		short := shortHWID(d.HWID)
		if len(name) == 0 {
			name = append(name, short)
		} else {
			tail = append(tail, short)
		}
	}
	// Показываем только дату подключения. Дата последней активности у панели
	// есть, но на экране от неё больше путаницы, чем пользы: она обновляется
	// на каждом обращении клиента и человеку ничего не говорит. Сортировать
	// список по ней это не мешает.
	if cfg.Dates && !d.FirstSeen.IsZero() {
		tail = append(tail, i18n.T(lang, "dev.since", d.FirstSeen.In(displayTZ).Format("02.01.2006")))
	}
	if len(name) == 0 {
		// Клиент не прислал ни модели, ни приложения, ни платформы, а
		// отпечаток скрыт владельцем: строка всё равно нужна, иначе список
		// окажется короче счётчика.
		name = append(name, i18n.T(lang, "dev.unknown"))
	}
	return strings.Join(name, " · "), strings.Join(tail, " · ")
}

// deviceLine — строка списка для чата (разметка HTML).
func deviceLine(lang string, d remnawave.Device, cfg model.DevicesConfig) string {
	head, meta := deviceParts(lang, d, cfg)
	line := "• <b>" + html.EscapeString(head) + "</b>"
	if meta != "" {
		line += "\n   <i>" + html.EscapeString(meta) + "</i>"
	}
	return line
}
