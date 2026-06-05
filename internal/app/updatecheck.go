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

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
}

func (a *App) fetchCommits(ctx context.Context) ([]ghCommit, error) {
	url := "https://api.github.com/repos/" + updateRepoSlug + "/commits?sha=main&per_page=30"
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

func newerThan(commits []ghCommit, current string) []ghCommit {
	if current == "" || current == "dev" {
		return nil
	}
	for i, c := range commits {
		if matchSHA(c.SHA, current) {
			return commits[:i]
		}
	}
	return nil
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
	commits, err := a.fetchCommits(ctx)
	if err != nil || len(commits) == 0 {
		if manual {
			a.sendKB(ctx, target, i18n.T(lang, "update.check_fail"), [][]models.InlineKeyboardButton{homeRow(lang)})
		}
		return
	}
	latest := commits[0].SHA
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
		a.sendKB(ctx, target, i18n.T(lang, "update.uptodate", shortSHA(latest)), [][]models.InlineKeyboardButton{homeRow(lang)})
		a.setUpdateSeen(ctx, latest)
		return
	}

	news := newerThan(commits, current)
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, "update.available"))
	sb.WriteString("\n\n")
	if len(news) == 0 {
		sb.WriteString(i18n.T(lang, "update.commit_latest", html.EscapeString(firstLine(commits[0].Commit.Message))))
		sb.WriteString("\n")
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

	a.notifyKB(ctx, target, sb.String(), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "update.btn_now"), "menu:update")},
	})
	a.setUpdateSeen(ctx, latest)
}

func (a *App) onUpdateCheck(ctx context.Context, chatID int64, val string, isAdmin bool) {
	if !isAdmin {
		return
	}
	switch val {
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
		a.checkUpdateOnce(ctx, chatID, true)
	}
}
