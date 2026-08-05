package app

import (
	"context"
	"encoding/json"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

const torrentLogPageSize = 10

// showTorrentLog — журнал торрент-блокера: прошлые отчёты с пагинацией и
// счётчиком повторов «×N за 30 дней» по каждому нарушителю.
func (a *App) showTorrentLog(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	if page < 0 {
		page = 0
	}
	reports, total, err := a.store.TorrentReports(ctx, torrentLogPageSize, page*torrentLogPageSize)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	if total == 0 {
		a.sendSysKB(ctx, chatID, i18n.T(lang, "torj.empty"),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:webhooks")})
		return
	}
	pages := (total + torrentLogPageSize - 1) / torrentLogPageSize
	since := time.Now().UTC().Add(-torrentRepeatWindow).Format(time.RFC3339)

	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "torj.title", total))
	counts := map[string]int{}
	for _, r := range reports {
		key := strconv.FormatInt(r.TelegramID, 10) + "|" + r.Username
		n, ok := counts[key]
		if !ok {
			n, _ = a.store.CountTorrentReports(ctx, r.TelegramID, r.Username, since)
			if n < 1 {
				n = 1
			}
			counts[key] = n
		}
		sb.WriteString("\n\n" + torrentLogLine(ctx, a, &r, n, lang))
	}

	rows := [][]models.InlineKeyboardButton{}
	if nav := paginationRow("torj:page:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, navBack(lang, "menu:webhooks"))
	a.sendSysKB(ctx, chatID, sb.String(), rows)
}

// torrentLogLine — одна строка журнала: дата · кто ×N · IP · нода · срок.
func torrentLogLine(ctx context.Context, a *App, r *model.TorrentReport, count int, lang string) string {
	when := r.CreatedAt
	if t, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
		when = t.UTC().Add(3 * time.Hour).Format("02.01 15:04")
	}
	who := ""
	if r.TelegramID != 0 {
		who = a.userLabelByID(ctx, r.TelegramID)
	} else {
		who = escapeName(orDash(r.Username))
	}
	mark := ""
	if count > 1 {
		mark = " 🔁×" + strconv.Itoa(count)
	}
	return when + " · " + who + mark + " · <code>" + escapeName(orDash(r.IP)) + "</code> · " +
		escapeName(orDash(r.Node)) + " · " + fmtBlockDur(lang, r.BlockSeconds)
}

// showTorrentUnblockAdmin — редактор сообщения о снятии блокировки.
func (a *App) showTorrentUnblockAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	tc := a.torrentCfg()

	current := ""
	custom := strings.TrimSpace(tc.UnblockText) != ""
	if custom {
		// Текст админа набран телеграмным форматированием (entities), а экран
		// уходит с ParseModeHTML — сырой текст экранируется, чтобы < > & из
		// него не ломали разметку экрана.
		current = html.EscapeString(tc.UnblockText)
	} else {
		current = i18n.T(lang, "rw.torrent_unblocked")
	}
	src := i18n.T(lang, "toru.src_default")
	if custom {
		src = i18n.T(lang, "toru.src_custom")
	}

	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "toru.btn_edit"), "torj:edit"), btn(i18n.T(lang, "toru.btn_test"), "torj:test")},
	}
	if custom {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "toru.btn_reset"), "torj:reset")})
	}
	rows = append(rows, navBack(lang, "menu:webhooks"))
	a.sendSysKB(ctx, chatID, i18n.T(lang, "toru.title", src, current), rows)
}

func (a *App) onTorrentAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "log":
		a.showTorrentLog(ctx, chatID, 0)
	case "page":
		page, _ := strconv.Atoi(arg)
		a.showTorrentLog(ctx, chatID, page)
	case "text":
		a.showTorrentUnblockAdmin(ctx, chatID)
	case "edit":
		a.getUI(chatID).torAwait = true
		a.sendKB(ctx, chatID, i18n.T(lang, "toru.ask"),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "torj:cancel")}})
	case "cancel":
		a.getUI(chatID).torAwait = false
		a.showTorrentUnblockAdmin(ctx, chatID)
	case "test":
		a.sendTorrentUnblock(ctx, chatID)
		a.showTorrentUnblockAdmin(ctx, chatID)
	case "reset":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Torrent.UnblockText = ""
			a.botCfg.Torrent.UnblockEntities = nil
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTorrentUnblockAdmin(ctx, chatID)
	}
}

// setTorrentUnblockText сохраняет присланное админом сообщение вместе с
// entities — форматирование у юзера будет 1-в-1 (как у приветствия).
func (a *App) setTorrentUnblockText(ctx context.Context, chatID int64, m *models.Message) {
	a.getUI(chatID).torAwait = false
	ents, _ := json.Marshal(m.Entities)
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeTorrent()
		a.botCfg.Torrent.UnblockText = m.Text
		a.botCfg.Torrent.UnblockEntities = ents
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showTorrentUnblockAdmin(ctx, chatID)
}
