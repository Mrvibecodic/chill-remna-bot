package app

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

const updateRepoSlug = "Mrvibecodic/chill-remna-bot"

// channelBranch maps an update channel to its git branch.
func channelBranch(ch string) string {
	if ch == "dev" {
		return "dev"
	}
	return "main"
}

// channelTag maps an update channel to its Docker image tag.
func channelTag(ch string) string {
	if ch == "dev" {
		return "dev"
	}
	return "latest"
}

func (a *App) updChannel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg != nil && a.botCfg.UpdateCheck.Channel == "dev" {
		return "dev"
	}
	return "stable"
}

func (a *App) channelChosen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.botCfg != nil && a.botCfg.UpdateCheck.ChannelChosen
}

func (a *App) channelName(lang string) string {
	if a.updChannel() == "dev" {
		return i18n.T(lang, "update.chan_name_dev")
	}
	return i18n.T(lang, "update.chan_name_stable")
}

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

func (a *App) fetchCommits(ctx context.Context, branch string) ([]ghCommit, error) {
	url := "https://api.github.com/repos/" + updateRepoSlug + "/commits?sha=" + branch + "&per_page=30"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "chill-remna-bot")
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github status %d", resp.StatusCode)
	}
	var out []ghCommit
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func matchSHA(a, b string) bool {
	if a == "" || b == "" || b == "dev" {
		return false
	}
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func sliceBetween(commits []ghCommit, current, latest string) []ghCommit {
	idxLatest := 0
	for i, c := range commits {
		if matchSHA(c.SHA, latest) {
			idxLatest = i
			break
		}
	}
	idxCur := len(commits)
	for i, c := range commits {
		if matchSHA(c.SHA, current) {
			idxCur = i
			break
		}
	}
	if idxLatest >= idxCur {
		return nil
	}
	return commits[idxLatest:idxCur]
}

func (a *App) latestBuiltSHA(ctx context.Context, branch string) (string, error) {
	url := "https://api.github.com/repos/" + updateRepoSlug + "/actions/workflows/docker.yml/runs?branch=" + branch + "&status=success&per_page=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "chill-remna-bot")
	cl := &http.Client{Timeout: 15 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github status %d", resp.StatusCode)
	}
	var out struct {
		Runs []struct {
			HeadSHA string `json:"head_sha"`
		} `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Runs) == 0 {
		return "", nil
	}
	return out.Runs[0].HeadSHA, nil
}

func (a *App) RunUpdateChecker(ctx context.Context) {
	msk := time.FixedZone("MSK", 3*3600)
	for {
		a.mu.Lock()
		hour := 12
		if a.botCfg != nil {
			hour = a.botCfg.UpdateCheck.Hour
		}
		a.mu.Unlock()
		if hour < 0 || hour > 23 {
			hour = 12
		}
		now := time.Now().In(msk)
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, msk)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
		}
		a.mu.Lock()
		on := a.botCfg != nil && a.botCfg.UpdateCheck.Enabled
		a.mu.Unlock()
		if on {
			a.checkUpdateOnce(ctx, 0, false)
		}
	}
}

func (a *App) setUpdateSeen(ctx context.Context, sha string) {
	a.mu.Lock()
	changed := a.botCfg != nil && a.botCfg.UpdateCheck.LastSeenAt != sha
	if changed {
		a.botCfg.UpdateCheck.LastSeenAt = sha
	}
	a.mu.Unlock()
	if changed {
		_ = a.saveBotConfig(ctx)
	}
}

func (a *App) checkUpdateOnce(ctx context.Context, adminChat int64, manual bool) {
	lang := a.botLang()
	target := adminChat
	if target == 0 {
		target = a.cfg.AdminID
	}
	branch := channelBranch(a.updChannel())
	latest, err := a.latestBuiltSHA(ctx, branch)
	if err != nil || latest == "" {
		if manual {
			a.sendSysKB(ctx, target, i18n.T(lang, "update.check_fail"), [][]models.InlineKeyboardButton{homeRow(lang)})
		}
		return
	}
	commits, _ := a.fetchCommits(ctx, branch)
	current := a.cfg.Commit
	upToDate := matchSHA(latest, current)

	a.mu.Lock()
	lastSeen := ""
	if a.botCfg != nil {
		lastSeen = a.botCfg.UpdateCheck.LastSeenAt
	}
	a.mu.Unlock()

	if !manual && (upToDate || latest == lastSeen) {
		a.setUpdateSeen(ctx, latest)
		return
	}
	if upToDate {
		a.sendSysKB(ctx, target, i18n.T(lang, "update.uptodate", shortSHA(latest)), [][]models.InlineKeyboardButton{homeRow(lang)})
		a.setUpdateSeen(ctx, latest)
		return
	}

	news := sliceBetween(commits, current, latest)
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "update.available"))
	sb.WriteString("\n\n")
	if len(news) == 0 {
		if len(commits) > 0 {
			sb.WriteString(i18n.T(lang, "update.commit_latest", html.EscapeString(firstLine(commits[0].Commit.Message))))
			sb.WriteString("\n")
		}
	} else {
		limit := len(news)
		if limit > 15 {
			limit = 15
		}
		for _, c := range news[:limit] {
			sb.WriteString("• " + html.EscapeString(firstLine(c.Commit.Message)) + "\n")
		}
		if len(news) > limit {
			sb.WriteString(i18n.T(lang, "update.more", len(news)-limit) + "\n")
		}
	}
	sb.WriteString("\n" + i18n.T(lang, "update.tail", shortSHA(latest)))

	if manual {
		a.sendSysKB(ctx, target, sb.String(), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "update.btn_now"), "upd:now")},
			homeRow(lang),
		})
	} else {
		rows := [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "update.btn_now"), "upd:now")},
			backHomeRow(lang),
		}
		if msgID := a.msg.SendKB(ctx, target, a.applyPremium(sb.String()), rows); msgID != 0 {
			a.setUpdNotice(target, msgID)
			time.AfterFunc(60*time.Second, func() {
				a.msg.Delete(context.Background(), target, msgID)
				a.clearUpdNotice(target, msgID)
			})
		}
	}
	a.setUpdateSeen(ctx, latest)
}

func (a *App) onUpdateCheck(ctx context.Context, chatID int64, val string, isAdmin bool) {
	if !isAdmin {
		return
	}
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "now":
		// Transitional migration: oblige the admin to pick a channel before updating.
		if !a.channelChosen() {
			a.showChannelChooser(ctx, chatID)
			return
		}
		a.handleUpdate(ctx, chatID)
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.UpdateCheck.Enabled = !a.botCfg.UpdateCheck.Enabled
			a.botCfg.UpdateCheck.Init = true
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showSystem(ctx, chatID)
	case "check":
		// Transitional migration: oblige picking a channel before the update check.
		if !a.channelChosen() {
			a.showChannelChooser(ctx, chatID)
			return
		}
		a.checkUpdateOnce(ctx, chatID, true)
	case "chan":
		a.showChannelChooser(ctx, chatID)
	case "setchan":
		a.setChannel(ctx, chatID, arg)
	}
}

func (a *App) showChannelChooser(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendSysKB(ctx, chatID, i18n.T(lang, "update.chan_title", a.channelName(lang)), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "update.chan_stable"), "upd:setchan:stable")},
		{btn(i18n.T(lang, "update.chan_dev"), "upd:setchan:dev")},
		backHomeRow(lang),
	})
}

func (a *App) setChannel(ctx context.Context, chatID int64, ch string) {
	if ch != "dev" {
		ch = "stable"
	}
	a.mu.Lock()
	if a.botCfg != nil {
		a.botCfg.UpdateCheck.Channel = ch
		a.botCfg.UpdateCheck.ChannelChosen = true
		a.botCfg.UpdateCheck.LastSeenAt = ""
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	lang := a.lang(chatID)
	a.sendKB(ctx, chatID, i18n.T(lang, "update.chan_set", a.channelName(lang)), [][]models.InlineKeyboardButton{homeRow(lang)})
}
