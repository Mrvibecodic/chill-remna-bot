package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

const apiLogPageSize = 20

func (a *App) showAPILog(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	a.mu.Unlock()
	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:system"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if panel == nil {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "apilog.no_panel"),
			[][]models.InlineKeyboardButton{back})
		return
	}
	all := panel.Logs()
	total := len(all)
	if total == 0 {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "apilog.empty"),
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

	rev := make([]int, total)
	for i := 0; i < total; i++ {
		rev[i] = total - 1 - i
	}
	from := page * apiLogPageSize
	to := from + apiLogPageSize
	if to > total {
		to = total
	}

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
	nav := paginationRow("alog:page:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next"))
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "apilog.btn_refresh"), "alog:refresh"),
		btn(i18n.T(lang, "apilog.btn_clear"), "alog:clear"),
	})
	rows = append(rows, back)
	a.sendSysKB(ctx, chatID, sb.String(), rows)
}

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
