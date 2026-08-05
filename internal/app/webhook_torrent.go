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

// torrentUserCooldown — минимальная пауза между предупреждениями одному
// пользователю. Панель шлёт отчёт на каждую блокировку IP, а у пользователя
// может быть несколько устройств/адресов — без паузы его завалит одинаковыми
// сообщениями. Админ получает все отчёты без троттлинга.
const torrentUserCooldown = 10 * time.Minute

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

	tc := a.torrentCfg()
	if tc.NotifyAdmin {
		a.torrentNotifyAdmin(ctx, &r)
	}
	if tc.NotifyUser {
		a.torrentNotifyUser(ctx, &r)
	}
}

func (a *App) torrentNotifyAdmin(ctx context.Context, r *rwTorrentReport) {
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

	node := escapeName(r.Node.Name)
	if node == "" {
		node = "—"
	}
	if cc := strings.TrimSpace(r.Node.CountryCode); cc != "" {
		node += " (" + escapeName(cc) + ")"
	}

	text := i18n.T(lang, "rw.torrent_admin",
		who, node,
		escapeName(orDash(act.IP)), fmtBlockDur(lang, act.BlockDuration), torrentTill(act.WillUnblockAt, lang),
		escapeName(orDash(xr.Protocol)), escapeName(orDash(xr.InboundTag)),
		escapeName(orDash(xr.Source)), escapeName(orDash(xr.Destination)))

	var rows [][]models.InlineKeyboardButton
	if r.User.TelegramID != 0 {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "rw.torrent_btn_user"), "usr:view:"+strconv.FormatInt(r.User.TelegramID, 10)),
		})
	}
	a.notifyKB(ctx, a.cfg.AdminID, text, rows)
}

func (a *App) torrentNotifyUser(ctx context.Context, r *rwTorrentReport) {
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
	a.notifyKB(ctx, uid, i18n.T(lang, "rw.torrent_user",
		fmtBlockDur(lang, act.BlockDuration), torrentTill(act.WillUnblockAt, lang)), nil)
	a.log.Info("торрент-блокер: предупреждение отправлено", "tg_id", uid)
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
