package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

// --- админ: лог API-запросов к панели Remnawave ---
//
// Источник — ring buffer внутри remnawave.Client.Logs(). Показываем последние
// записи (новые сверху), таблично, с пагинацией. Кнопка «🔄 Обновить» —
// перечитать буфер (он живёт в памяти процесса, доступен сразу). «🧹 Очистить»
// сбрасывает буфер.

const apiLogPageSize = 20

func (a *App) showAPILog(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:manage"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if panel == nil {
		a.sendKB(ctx, chatID, i18n.T(lang, "apilog.no_panel"),
			[][]models.InlineKeyboardButton{back})
		return
	}
	all := panel.Logs()
	total := len(all)
	if total == 0 {
		a.sendKB(ctx, chatID, i18n.T(lang, "apilog.empty"),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "apilog.btn_refresh"), "alog:refresh")},
				back,
			})
		return
	}

	if page < 0 {
		page = 0
	}
	pages := (total + apiLogPageSize - 1) / apiLogPageSize
	if page >= pages {
		page = pages - 1
	}

	// Новые записи — сверху: реверсируем и берём страницу.
	rev := make([]int, total)
	for i := 0; i < total; i++ {
		rev[i] = total - 1 - i
	}
	from := page * apiLogPageSize
	to := from + apiLogPageSize
	if to > total {
		to = total
	}

	// Колонки: time(HH:MM:SS) · method(6) · status(3) · ms(5) · path(остаток).
	// Под <pre>, моноширинно.
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "apilog.title", total, page+1, pages))
	sb.WriteString("\n<pre>")
	header := padRight("Time", 8) + "  " + padRight("Method", 6) + "  " +
		padRight("Status", 6) + "  " + padRight("Δms", 5) + "  Path"
	sb.WriteString(header)
	sb.WriteString("\n")
	sb.WriteString(strings.Repeat("─", visualWidth(header)))

	for i := from; i < to; i++ {
		ev := all[rev[i]]
		hhmmss := ev.Time.Local().Format("15:04:05")
		status := ""
		if ev.Err != "" {
			status = "ERR"
		} else {
			status = strconv.Itoa(ev.Status)
		}
		ms := strconv.FormatInt(ev.DurationMs, 10)
		path := ev.Path
		if ev.Err != "" {
			path = path + "  ← " + ev.Err
		}
		sb.WriteString("\n")
		sb.WriteString(padRight(hhmmss, 8))
		sb.WriteString("  ")
		sb.WriteString(padRight(ev.Method, 6))
		sb.WriteString("  ")
		sb.WriteString(padRight(status, 6))
		sb.WriteString("  ")
		sb.WriteString(padRight(ms, 5))
		sb.WriteString("  ")
		sb.WriteString(path)
	}
	sb.WriteString("</pre>")

	var rows [][]models.InlineKeyboardButton
	var nav []models.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, btn(i18n.T(lang, "btn.prev"), "alog:page:"+strconv.Itoa(page-1)))
	}
	if page+1 < pages {
		nav = append(nav, btn(i18n.T(lang, "btn.next"), "alog:page:"+strconv.Itoa(page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "apilog.btn_refresh"), "alog:refresh"),
		btn(i18n.T(lang, "apilog.btn_clear"), "alog:clear"),
	})
	rows = append(rows, back)
	a.sendKB(ctx, chatID, sb.String(), rows)
}

// onAPILog — диспетчер callback'ов "alog:*".
func (a *App) onAPILog(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "page":
		page, _ := strconv.Atoi(arg)
		a.showAPILog(ctx, chatID, page)
	case "refresh":
		a.showAPILog(ctx, chatID, 0)
	case "clear":
		a.mu.Lock()
		panel := a.panel
		a.mu.Unlock()
		if panel != nil {
			panel.ClearLogs()
		}
		a.showAPILog(ctx, chatID, 0)
	}
}
