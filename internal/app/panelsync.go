package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/remnawave"
)

const (
	panelScanPageSize = 250
	panelScanMaxUsers = 10000
)

func (a *App) panelClient() *remnawave.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.panel
}

func (a *App) syncPanelAccount(ctx context.Context, chatID int64) bool {
	panel := a.panelClient()
	if panel == nil || a.store == nil {
		return false
	}
	if u, _ := a.store.GetUser(ctx, chatID); u != nil && (u.TrialUsedAt != "" || u.SubExpireAt != "") {
		return false
	}
	ui := a.getUI(chatID)
	if ui.panelSyncDone {
		return false
	}
	pu := a.findPanelAccount(ctx, panel, chatID)
	ui.panelSyncDone = true
	if pu == nil {
		return false
	}
	if pu.TelegramID == 0 {
		if err := panel.LinkTelegramID(ctx, pu.Ref, chatID, false); err != nil {
			a.log.Warn("panel sync: link telegramId", "tg_id", chatID, "panel_ref", pu.Ref.Key(), "err", err)
		}
	}
	a.markPanelLinked(ctx, chatID, pu.ExpireAt)
	a.log.Info("panel sync: account linked", "tg_id", chatID, "panel_user", pu.Username, "panel_ref", pu.Ref.Key())
	return true
}

func (a *App) findPanelAccount(ctx context.Context, panel *remnawave.Client, chatID int64) *remnawave.PanelUser {
	if pu, err := panel.FindByTelegramID(ctx, chatID); err == nil && pu != nil {
		return pu
	}
	// tg_<id> — наша схема имён, rs_<id> — схема remnashop: после переезда
	// аккаунты в панели остаются с его именами, и находить их по прямому
	// запросу дешевле, чем сканировать весь список.
	for _, prefix := range []string{"tg_", "rs_"} {
		if pu, err := panel.FindByUsername(ctx, fmt.Sprintf("%s%d", prefix, chatID)); err == nil && pu != nil {
			return pu
		}
	}
	return a.scanPanelByID(ctx, panel, chatID)
}

func (a *App) scanPanelByID(ctx context.Context, panel *remnawave.Client, chatID int64) *remnawave.PanelUser {
	idStr := strconv.FormatInt(chatID, 10)
	var found *remnawave.PanelUser
	matches := 0
	for start := 0; start < panelScanMaxUsers; start += panelScanPageSize {
		users, total, err := panel.ListUsersPage(ctx, start, panelScanPageSize)
		if err != nil {
			a.log.Warn("panel sync: scan", "err", err)
			return nil
		}
		for i := range users {
			if usernameHasID(users[i].Username, idStr) {
				matches++
				if matches == 1 {
					u := users[i]
					found = &u
				}
			}
		}
		if len(users) == 0 || start+len(users) >= total {
			break
		}
	}
	if matches == 1 {
		return found
	}
	if matches > 1 {
		a.log.Warn("panel sync: ambiguous username match, manual link required", "tg_id", chatID, "matches", matches)
	}
	return nil
}

func usernameHasID(username, id string) bool {
	if id == "" {
		return false
	}
	idx := 0
	for {
		i := strings.Index(username[idx:], id)
		if i < 0 {
			return false
		}
		p := idx + i
		after := p + len(id)
		beforeOK := p == 0 || !isASCIIDigit(username[p-1])
		afterOK := after == len(username) || !isASCIIDigit(username[after])
		if beforeOK && afterOK {
			return true
		}
		idx = p + 1
	}
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func (a *App) markPanelLinked(ctx context.Context, chatID int64, expireAt string) {
	if a.store == nil {
		return
	}
	_ = a.store.UpsertUser(ctx, chatID)
	_ = a.store.SetTrialUsed(ctx, chatID, time.Now().UTC().Format(time.RFC3339))
	if expireAt != "" {
		_ = a.store.SetSubExpiry(ctx, chatID, expireAt, "paid")
	}
	a.invalidateSubCache(chatID)
}

func (a *App) adminLinkPanel(ctx context.Context, adminChat, uid int64, input string) {
	lang := a.lang(adminChat)
	panel := a.panelClient()
	if panel == nil || uid == 0 {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", i18n.T(lang, "squads.no_panel")))
		return
	}
	input = strings.TrimSpace(input)
	if input == "" {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_not_found"))
		return
	}
	var pu *remnawave.PanelUser
	var err error
	// Panel 3.0.0 replaced the user uuid with a numeric id, so the admin may
	// paste a username, a uuid or an id. Digits are ambiguous — a username of
	// digits is legal too — so both are resolved and a genuine clash is
	// reported instead of silently linking the wrong account.
	switch {
	case looksLikeUUID(input):
		pu, err = panel.FindByRef(ctx, input)
	case looksLikeUserID(input):
		byID, idErr := panel.FindByRef(ctx, input)
		byName, nameErr := panel.FindByUsername(ctx, input)
		switch {
		case byID != nil && byName != nil && byID.Username != byName.Username:
			a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_ambiguous", byID.Username, byName.Username))
			return
		case byID != nil:
			pu = byID
		case byName != nil:
			pu = byName
		default:
			err = firstErr(idErr, nameErr)
		}
	default:
		pu, err = panel.FindByUsername(ctx, input)
	}
	if err != nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", err.Error()))
		return
	}
	if pu == nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_not_found"))
		return
	}
	if pu.TelegramID != 0 && pu.TelegramID != uid {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_busy", pu.Username, pu.TelegramID))
		return
	}
	if err := panel.LinkTelegramID(ctx, pu.Ref, uid, true); err != nil {
		a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_fail", err.Error()))
		return
	}
	a.markPanelLinked(ctx, uid, pu.ExpireAt)
	a.log.Info("panel sync: manual link", "tg_id", uid, "panel_user", pu.Username, "panel_ref", pu.Ref.Key())
	a.notify(ctx, uid, i18n.T(a.lang(uid), "sync.linked", formatExpire(pu.ExpireAt, a.lang(uid))))
	a.sendHome(ctx, adminChat, i18n.T(lang, "user.link_done", pu.Username, formatExpire(pu.ExpireAt, lang)))
}

// firstErr returns the first non-nil error, so a lookup that failed on both
// paths still reports something to the admin.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// looksLikeUserID matches the numeric user id used by panel 3.0.0+.
func looksLikeUserID(s string) bool {
	if s == "" || len(s) > 19 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
