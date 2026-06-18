package app

import (
	"context"

	"remnabot/internal/remnawave"
)

// addSubParams snapshots the add-on config (and panel client) under the lock.
func (a *App) addSubParams() (panel *remnawave.Client, enabled bool, suffix string, trafficBytes int64, internal []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	panel = a.panel
	if a.botCfg == nil {
		return panel, false, "_addsub", 0, nil
	}
	c := a.botCfg.AddSub
	suffix = c.UsernameSuffix
	if suffix == "" {
		suffix = "_addsub"
	}
	return panel, c.Enabled, suffix, int64(c.TrafficGB) * 1024 * 1024 * 1024, append([]string(nil), c.InternalSquads...)
}

// syncAddSub upserts the add-on user B for telegramID (best-effort; a failure
// must never break the main purchase). No-op when the feature is disabled.
func (a *App) syncAddSub(ctx context.Context, telegramID int64) {
	panel, enabled, suffix, traffic, internal := a.addSubParams()
	if !enabled || panel == nil {
		return
	}
	if err := panel.UpsertAddSub(ctx, telegramID, suffix, traffic, internal); err != nil {
		a.log.Warn("addsub: upsert", "tg_id", telegramID, "err", err)
	}
}

// removeAddSub deletes user B (runs regardless of the toggle, to clean up).
func (a *App) removeAddSub(ctx context.Context, telegramID int64) {
	panel, _, suffix, _, _ := a.addSubParams()
	if panel == nil {
		return
	}
	if err := panel.DeleteAddSub(ctx, telegramID, suffix); err != nil {
		a.log.Warn("addsub: delete", "tg_id", telegramID, "err", err)
	}
}

// setAddSubEnabledPanel enables/disables user B alongside the main one.
func (a *App) setAddSubEnabledPanel(ctx context.Context, telegramID int64, enable bool) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if !enabled || panel == nil {
		return
	}
	if err := panel.SetAddSubEnabled(ctx, telegramID, suffix, enable); err != nil {
		a.log.Warn("addsub: set-enabled", "tg_id", telegramID, "err", err)
	}
}
