package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// torrentUserCooldown — минимальная пауза между сообщениями одному
// пользователю (отдельно для предупреждений и для «блокировка снята»).
// Панель шлёт отчёт на каждую блокировку IP, а у пользователя может быть
// несколько устройств/адресов — без паузы его завалит одинаковыми
// сообщениями. Админ получает все отчёты без троттлинга.
const torrentUserCooldown = 10 * time.Minute

// torrentRepeatWindow — окно, за которое считаются повторные нарушения.
const torrentRepeatWindow = 30 * 24 * time.Hour

// torrentUnblockTick — период проверки, не пора ли слать «блокировка снята».
const torrentUnblockTick = 30 * time.Second

// torrentUnblockStale — если срок разблокировки прошёл давнее этого (бот был
// выключен), сообщение уже неактуально: запись помечается без отправки.
const torrentUnblockStale = 24 * time.Hour

// torrentRetention — сколько хранится журнал торрент-блокера.
const torrentRetention = 180 * 24 * time.Hour

// rwTorrentReport — payload события torrent_blocker.report: data содержит не
// объект пользователя (как у user.*), а {node, user, report}.
type rwTorrentReport struct {
	Node struct {
		Name        string `json:"name"`
		Address     string `json:"address"`
		CountryCode string `json:"countryCode"`
	} `json:"node"`
	User   rwUserPayload `json:"user"`
	Report struct {
		ActionReport struct {
			Blocked       bool   `json:"blocked"`
			IP            string `json:"ip"`
			BlockDuration int    `json:"blockDuration"`
			WillUnblockAt string `json:"willUnblockAt"`
			UserID        string `json:"userId"`
			ProcessedAt   string `json:"processedAt"`
		} `json:"actionReport"`
		XrayReport struct {
			Email       string `json:"email"`
			Protocol    string `json:"protocol"`
			Source      string `json:"source"`
			Destination string `json:"destination"`
			InboundTag  string `json:"inboundTag"`
		} `json:"xrayReport"`
	} `json:"report"`
}

func (a *App) torrentCfg() model.TorrentConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.TorrentConfig{NotifyAdmin: true, NotifyUser: true, Init: true}
	}
	a.botCfg.NormalizeTorrent()
	return a.botCfg.Torrent
}

// toggleTorrentNotify переключает уведомление админу (admin=true) или
// пользователю (admin=false).
func (a *App) toggleTorrentNotify(admin bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return
	}
	a.botCfg.NormalizeTorrent()
	if admin {
		a.botCfg.Torrent.NotifyAdmin = !a.botCfg.Torrent.NotifyAdmin
	} else {
		a.botCfg.Torrent.NotifyUser = !a.botCfg.Torrent.NotifyUser
	}
}

func (a *App) pushTorrentReport(ctx context.Context, data json.RawMessage) {
	var r rwTorrentReport
	if err := json.Unmarshal(data, &r); err != nil {
		a.log.Warn("торрент-блокер: не разобран payload", "err", err)
		return
	}
	act := r.Report.ActionReport
	a.log.Info("торрент-блокер: отчёт панели",
		"tg_id", r.User.TelegramID, "username", r.User.Username,
		"ip", act.IP, "block_s", act.BlockDuration, "node", r.Node.Name)

	rep := a.storeTorrentReport(ctx, &r)
	count := a.torrentRepeatCount(ctx, rep)

	tc := a.torrentCfg()
	if tc.NotifyAdmin {
		a.torrentNotifyAdmin(ctx, &r, rep.WillUnblockAt, count)
	}
	if tc.NotifyUser {
		a.torrentNotifyUser(ctx, &r, rep.WillUnblockAt)
	}
}

// torrentNodeLabel — подпись ноды для журнала и отчёта: «имя (страна)».
func torrentNodeLabel(r *rwTorrentReport) string {
	node := strings.TrimSpace(r.Node.Name)
	if cc := strings.TrimSpace(r.Node.CountryCode); cc != "" {
		if node == "" {
			node = cc
		} else {
			node += " (" + cc + ")"
		}
	}
	return node
}

// storeTorrentReport пишет отчёт в журнал и возвращает запись (для счётчика
// повторов). При недоступной БД возвращает запись без ID.
func (a *App) storeTorrentReport(ctx context.Context, r *rwTorrentReport) *model.TorrentReport {
	act := r.Report.ActionReport

	// Момент разблокировки нормализуется к RFC3339 UTC, чтобы сравнение
	// строк в DueTorrentUnblocks совпадало с порядком времён. Если панель
	// его не прислала — выводится из processedAt + blockDuration.
	will := ""
	if t, err := time.Parse(time.RFC3339, act.WillUnblockAt); err == nil {
		will = t.UTC().Format(time.RFC3339)
	} else if act.BlockDuration > 0 {
		base := time.Now()
		if t, err := time.Parse(time.RFC3339, act.ProcessedAt); err == nil {
			base = t
		}
		will = base.UTC().Add(time.Duration(act.BlockDuration) * time.Second).Format(time.RFC3339)
	}

	rep := &model.TorrentReport{
		TelegramID:    r.User.TelegramID,
		Username:      strings.TrimSpace(r.User.Username),
		Node:          torrentNodeLabel(r),
		IP:            strings.TrimSpace(act.IP),
		Protocol:      strings.TrimSpace(r.Report.XrayReport.Protocol),
		Inbound:       strings.TrimSpace(r.Report.XrayReport.InboundTag),
		Source:        strings.TrimSpace(r.Report.XrayReport.Source),
		Destination:   strings.TrimSpace(r.Report.XrayReport.Destination),
		BlockSeconds:  act.BlockDuration,
		WillUnblockAt: will,
		// Уведомлять о разблокировке некого — сразу помечено отправленным.
		UnblockNotified: r.User.TelegramID == 0 || will == "",
	}
	if rep.Username == "" {
		rep.Username = strings.TrimSpace(act.UserID)
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return rep
	}
	if err := st.AddTorrentReport(ctx, rep); err != nil {
		a.log.Warn("торрент-блокер: запись в журнал", "err", err)
	}
	return rep
}

// torrentRepeatCount — номер нарушения за окно (включая текущее).
func (a *App) torrentRepeatCount(ctx context.Context, rep *model.TorrentReport) int {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return 1
	}
	since := time.Now().UTC().Add(-torrentRepeatWindow).Format(time.RFC3339)
	n, err := st.CountTorrentReports(ctx, rep.TelegramID, rep.Username, since)
	if err != nil {
		a.log.Warn("торрент-блокер: счётчик повторов", "err", err)
		return 1
	}
	if n < 1 {
		return 1
	}
	return n
}

func (a *App) torrentNotifyAdmin(ctx context.Context, r *rwTorrentReport, will string, count int) {
	lang := a.lang(a.cfg.AdminID)
	act := r.Report.ActionReport
	xr := r.Report.XrayReport

	who := ""
	if r.User.TelegramID != 0 {
		who = a.userLabelByID(ctx, r.User.TelegramID)
	} else if r.User.Username != "" {
		who = escapeName(r.User.Username)
	} else if act.UserID != "" {
		who = escapeName(act.UserID)
	} else if xr.Email != "" {
		who = escapeName(xr.Email)
	} else {
		who = "—"
	}

	text := i18n.T(lang, "rw.torrent_admin",
		who, i18n.T(lang, "rw.torrent_admin_repeat", count),
		escapeName(orDash(torrentNodeLabel(r))),
		escapeName(orDash(act.IP)), fmtBlockDur(lang, act.BlockDuration), torrentTill(will, lang),
		escapeName(orDash(xr.Protocol)), escapeName(orDash(xr.InboundTag)),
		escapeName(orDash(xr.Source)), escapeName(orDash(xr.Destination)))

	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "rw.torrent_btn_log"), "torj:log")},
	}
	if r.User.TelegramID != 0 {
		rows = append([][]models.InlineKeyboardButton{{
			btn(i18n.T(lang, "rw.torrent_btn_user"), "usr:view:"+strconv.FormatInt(r.User.TelegramID, 10)),
		}}, rows...)
	}
	a.notifyKB(ctx, a.cfg.AdminID, text, rows)
}

func (a *App) torrentNotifyUser(ctx context.Context, r *rwTorrentReport, will string) {
	uid := r.User.TelegramID
	if uid == 0 {
		return
	}
	a.thrMu.Lock()
	if last, ok := a.torSeen[uid]; ok && time.Since(last) < torrentUserCooldown {
		a.thrMu.Unlock()
		return
	}
	if a.torSeen == nil {
		a.torSeen = map[int64]time.Time{}
	}
	a.torSeen[uid] = time.Now()
	a.thrMu.Unlock()

	act := r.Report.ActionReport
	lang := a.lang(uid)
	var rows [][]models.InlineKeyboardButton
	if sup := a.supportURL(); sup != "" {
		rows = append(rows, []models.InlineKeyboardButton{{Text: i18n.T(lang, "btn.support"), URL: sup}})
	}
	a.notifyKB(ctx, uid, i18n.T(lang, "rw.torrent_user",
		fmtBlockDur(lang, act.BlockDuration), torrentTill(will, lang)), rows)
	a.log.Info("торрент-блокер: предупреждение отправлено", "tg_id", uid)
}

// RunTorrentUnblocker — фоновый цикл: шлёт «блокировка снята», когда истекает
// срок из журнала. Переживает рестарт: очередь лежит в БД (unblock_notified).
func (a *App) RunTorrentUnblocker(ctx context.Context) {
	t := time.NewTicker(torrentUnblockTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.torrentUnblockOnce(ctx)
		}
	}
}

func (a *App) torrentUnblockOnce(ctx context.Context) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return
	}
	now := time.Now()
	due, err := st.DueTorrentUnblocks(ctx, now.UTC().Format(time.RFC3339))
	if err != nil {
		a.log.Warn("торрент-блокер: выборка разблокировок", "err", err)
		return
	}
	if len(due) == 0 {
		return
	}
	notifyUser := a.torrentCfg().NotifyUser
	notified := map[int64]bool{}
	for _, r := range due {
		if err := st.MarkTorrentUnblockNotified(ctx, r.ID); err != nil {
			a.log.Warn("торрент-блокер: пометка разблокировки", "err", err)
			continue
		}
		if !notifyUser || notified[r.TelegramID] {
			continue
		}
		// Срок вышел давно (бот стоял) — сообщение уже неактуально.
		if t, err := time.Parse(time.RFC3339, r.WillUnblockAt); err != nil || now.Sub(t) > torrentUnblockStale {
			continue
		}
		// Та же пауза, что у предупреждений: несколько блокировок с разных
		// устройств не должны родить пачку «снята» подряд.
		a.thrMu.Lock()
		if last, ok := a.torUnbSeen[r.TelegramID]; ok && time.Since(last) < torrentUserCooldown {
			a.thrMu.Unlock()
			continue
		}
		if a.torUnbSeen == nil {
			a.torUnbSeen = map[int64]time.Time{}
		}
		a.torUnbSeen[r.TelegramID] = time.Now()
		a.thrMu.Unlock()

		notified[r.TelegramID] = true
		a.sendTorrentUnblock(ctx, r.TelegramID)
	}
}

// sendTorrentUnblock шлёт «блокировка снята»: заданный админом текст с его
// форматированием, иначе — стандартный из i18n.
func (a *App) sendTorrentUnblock(ctx context.Context, chatID int64) {
	tc := a.torrentCfg()
	lang := a.lang(chatID)
	if strings.TrimSpace(tc.UnblockText) != "" {
		var ents []models.MessageEntity
		_ = json.Unmarshal(tc.UnblockEntities, &ents)
		a.msg.SendEnt(ctx, chatID, tc.UnblockText, ents, [][]models.InlineKeyboardButton{backHomeRow(lang)})
	} else {
		a.notifyKB(ctx, chatID, i18n.T(lang, "rw.torrent_unblocked"), nil)
	}
	a.log.Info("торрент-блокер: уведомление о разблокировке", "tg_id", chatID)
}

// torrentTill форматирует момент разблокировки; при пустом/кривом значении — «—».
func torrentTill(raw, lang string) string {
	if raw == "" {
		return "—"
	}
	if _, err := time.Parse(time.RFC3339, raw); err != nil {
		return "—"
	}
	return formatExpire(raw, lang)
}

// fmtBlockDur — компактная длительность блокировки: «1 ч 30 мин», «60 сек».
func fmtBlockDur(lang string, secs int) string {
	if secs <= 0 {
		return "—"
	}
	h, m, s := secs/3600, (secs%3600)/60, secs%60
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+" "+i18n.T(lang, "dur.h"))
	}
	if m > 0 {
		parts = append(parts, strconv.Itoa(m)+" "+i18n.T(lang, "dur.m"))
	}
	if s > 0 && h == 0 {
		parts = append(parts, strconv.Itoa(s)+" "+i18n.T(lang, "dur.s"))
	}
	return strings.Join(parts, " ")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
