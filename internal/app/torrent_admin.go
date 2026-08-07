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

// showTorrentAdmin — раздел «Торренты» внутри «Пользователей»: это управление
// нарушителями, а не транспортом, поэтому в «Вебхуках» ему делать нечего —
// там остались только адрес, порт, домен и секрет.
func (a *App) showTorrentAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	tc := a.torrentCfg()

	total, last30 := 0, 0
	if a.store != nil {
		total, _ = a.store.CountTorrentReportsAll(ctx, "")
		last30, _ = a.store.CountTorrentReportsAll(ctx,
			time.Now().UTC().Add(-torrentRepeatWindow).Format(time.RFC3339))
	}
	mark := func(b bool) string {
		if b {
			return "✅"
		}
		return "⬜"
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(mark(tc.NotifyAdmin)+" "+i18n.T(lang, "wh.btn_tor_admin"), "torj:tadm"),
			btn(mark(tc.NotifyUser)+" "+i18n.T(lang, "wh.btn_tor_user"), "torj:tusr")},
		{btn(i18n.T(lang, "wh.btn_tor_strike", strikeLabel(lang, tc.StrikeLimit)), "torj:strike")},
		{btn(i18n.T(lang, "wh.btn_tor_log"), "torj:log"), btn(i18n.T(lang, "wh.btn_tor_text"), "torj:text")},
		{btn(i18n.T(lang, "btn.back"), "menu:users"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	text := i18n.T(lang, "torj.home", total, last30)
	// Без секрета отчёты панели отбрасываются (см. pushTorrentReport): иначе
	// админ видел бы замерший журнал и молчащие тумблеры без единого намёка.
	if a.rwSecret() == "" {
		text += "\n\n" + i18n.T(lang, "torj.no_secret")
	}
	a.sendUsrKB(ctx, chatID, text, rows)
}

// showUserTorrents — нарушения одного человека: из карточки пользователя.
func (a *App) showUserTorrents(ctx context.Context, chatID, uid int64, page int) {
	lang := a.lang(chatID)
	if a.store == nil {
		return
	}
	if page < 0 {
		page = 0
	}
	id := strconv.FormatInt(uid, 10)
	reports, total, err := a.store.UserTorrentReports(ctx, uid, "", torrentLogPageSize, page*torrentLogPageSize)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+err.Error())
		return
	}
	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "usr:view:"+id), btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if total == 0 {
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "torj.user_empty"),
			[][]models.InlineKeyboardButton{back})
		return
	}
	pages := (total + torrentLogPageSize - 1) / torrentLogPageSize

	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "torj.user_title", a.userLabelByID(ctx, uid), total))
	for _, r := range reports {
		sb.WriteString("\n\n" + torrentLogLine(ctx, a, &r, 0, lang))
	}
	var rows [][]models.InlineKeyboardButton
	if nav := paginationRow("torj:up:"+id+":", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, back)
	a.sendUsrKB(ctx, chatID, sb.String(), rows)
}

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
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "torj.empty"),
			[][]models.InlineKeyboardButton{navBack(lang, "torj:home")})
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
	rows = append(rows, navBack(lang, "torj:home"))
	a.sendUsrKB(ctx, chatID, sb.String(), rows)
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
	rows = append(rows, navBack(lang, "torj:home"))
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "toru.title", src, current), rows)
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
	case "unb":
		a.torrentUnblockIP(ctx, chatID, arg)
	case "ign":
		a.torrentIgnoreUser(ctx, chatID, arg)
	case "home":
		a.showTorrentAdmin(ctx, chatID)
	case "tadm":
		a.toggleTorrentNotify(true)
		_ = a.saveBotConfig(ctx)
		a.showTorrentAdmin(ctx, chatID)
	case "tusr":
		a.toggleTorrentNotify(false)
		_ = a.saveBotConfig(ctx)
		a.showTorrentAdmin(ctx, chatID)
	case "u":
		uid, _ := strconv.ParseInt(arg, 10, 64)
		a.showUserTorrents(ctx, chatID, uid, 0)
	case "up":
		idStr, pageStr, _ := strings.Cut(arg, ":")
		uid, _ := strconv.ParseInt(idStr, 10, 64)
		page, _ := strconv.Atoi(pageStr)
		a.showUserTorrents(ctx, chatID, uid, page)
	case "strike":
		a.getUI(chatID).adminInput = "torrent_strike"
		a.askInput(ctx, chatID, i18n.T(lang, "toru.ask_strike"), "torj:home")
	case "text":
		a.showTorrentUnblockAdmin(ctx, chatID)
	case "edit":
		ui := a.getUI(chatID)
		// Симметрично askInput: два ожидания ввода не должны накладываться.
		ui.adminInput = ""
		ui.torAwait = true
		a.sendKB(ctx, chatID, i18n.T(lang, "toru.ask"),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.cancel"), "torj:cancel")}})
	case "cancel":
		ui := a.getUI(chatID)
		ui.torAwait = false
		ui.adminInput = ""
		a.showTorrentAdmin(ctx, chatID)
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
	ui := a.getUI(chatID)
	ui.torAwait = false
	ui.adminInput = ""
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

// setTorrentStrikeLimit сохраняет порог автоблокировки: 0 (и любой мусор) —
// политика выключена.
func (a *App) setTorrentStrikeLimit(ctx context.Context, chatID int64, text string) {
	// Молчаливый Atoi превращал любую опечатку в 0, то есть ВЫКЛЮЧАЛ уже
	// настроенную политику без единого слова.
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || n < 0 {
		a.sendKB(ctx, chatID, i18n.T(a.lang(chatID), "toru.strike_bad"),
			[][]models.InlineKeyboardButton{{btn(i18n.T(a.lang(chatID), "btn.cancel"), "torj:cancel")}})
		return
	}
	a.getUI(chatID).adminInput = ""
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.NormalizeTorrent()
		a.botCfg.Torrent.StrikeLimit = n
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showTorrentAdmin(ctx, chatID)
}
