package app

import (
	"context"
	"strconv"
	"time"

	"remnabot/internal/remnawave"
)

// addSubParams snapshots the add-on config (and panel client) under the lock.
func (a *App) addSubParams() (panel *remnawave.Client, enabled bool, suffix string, trafficBytes int64, internal []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	panel = a.panel
	if a.botCfg == nil {
		return panel, false, remnawave.DefaultAddSubSuffix, 0, nil
	}
	c := a.botCfg.AddSub
	suffix = c.UsernameSuffix
	if suffix == "" {
		suffix = remnawave.DefaultAddSubSuffix
	}
	return panel, c.Enabled, suffix, int64(c.TrafficGB) * 1024 * 1024 * 1024, append([]string(nil), c.InternalSquads...)
}

// addSubOptions is addSubParams shaped for the panel client. Migration of a
// legacy-named add-on is never part of it: the automatic paths must not delete
// anything (see remnawave.AddSubOptions.MigrateLegacyName).
func (a *App) addSubOptions(resetTraffic bool) (*remnawave.Client, bool, remnawave.AddSubOptions) {
	panel, enabled, suffix, traffic, internal := a.addSubParams()
	return panel, enabled, remnawave.AddSubOptions{
		Suffix:         suffix,
		TrafficBytes:   traffic,
		InternalSquads: internal,
		ResetTraffic:   resetTraffic,
	}
}

// syncAddSub upserts the add-on user B for telegramID (best-effort; a failure
// must never break the main purchase). No-op when the feature is disabled.
// resetTraffic must be true exactly when the main subscription's traffic was
// reset as well (paid renewal), so B doesn't stay exhausted after payment.
func (a *App) syncAddSub(ctx context.Context, telegramID int64, resetTraffic bool) {
	panel, enabled, opt := a.addSubOptions(resetTraffic)
	if !enabled || panel == nil {
		return
	}
	res, err := panel.UpsertAddSub(ctx, telegramID, opt)
	if err != nil {
		a.log.Warn("addsub: upsert", "tg_id", telegramID, "err", err)
	}
	if res.Legacy != "" {
		a.log.Info("addsub: доп-подписка живёт под старым именем; «Синхронизировать всех» переведёт её на новое",
			"tg_id", telegramID, "legacy", res.Legacy)
	}
}

// removeAddSub deletes user B (runs regardless of the toggle, to clean up).
// Must run BEFORE the main user is deleted: B is resolved from A's username, so
// once A is gone only the legacy name can still be found.
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
// Disabling runs regardless of the toggle (a leftover B must not keep serving a
// blocked user); enabling only makes sense while the feature is on.
func (a *App) setAddSubEnabledPanel(ctx context.Context, telegramID int64, enable bool) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if panel == nil || (enable && !enabled) {
		return
	}
	if err := panel.SetAddSubEnabled(ctx, telegramID, suffix, enable); err != nil {
		a.log.Warn("addsub: set-enabled", "tg_id", telegramID, "err", err)
	}
}

// addSubStatus returns B's snapshot for the user-facing screens; ok=false when
// the feature is off or the user has no add-on subscription.
func (a *App) addSubStatus(ctx context.Context, telegramID int64) (remnawave.AddSubInfo, bool) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if !enabled || panel == nil {
		return remnawave.AddSubInfo{}, false
	}
	return panel.AddSubStatus(ctx, telegramID, suffix)
}

// resetAddSubDevices extends the "reset devices" action onto B. The middleware
// forwards the client's HWID headers to the add-on subscription as well, so B
// accumulates registrations for the same devices; without this they keep
// occupying B's inherited device limit and keep working with the old
// credentials. Best-effort: the main reset has already succeeded by then.
func (a *App) resetAddSubDevices(ctx context.Context, telegramID int64) {
	panel, enabled, suffix, _, _ := a.addSubParams()
	if !enabled || panel == nil {
		return
	}
	res, found, err := panel.ResetAddSubDevices(ctx, telegramID, suffix)
	if err != nil {
		// The user has already been told the reset went through (it did, for
		// the main subscription), so the add-on has to catch up out of band
		// instead of quietly staying on the old credentials.
		a.log.Warn("addsub: reset devices, повторю в фоне", "tg_id", telegramID, "err", err)
		a.retryAddSubResetInBackground(telegramID)
		return
	}
	if !found {
		return
	}
	if res.HwidErr != nil {
		a.log.Warn("addsub: HWID delete-all failed; keys rotated, retrying in background",
			"tg_id", telegramID, "err", res.HwidErr)
		a.clearHwidInBackground(telegramID, res.Ref)
	}
}

// addSubResetRetryEvery is the pause between background attempts to finish an
// add-on device reset that failed while the user was waiting.
const addSubResetRetryEvery = 30 * time.Second

// retryAddSubResetInBackground keeps retrying the add-on device reset until it
// succeeds or the budget runs out. Deduped per user so repeated taps can't
// stack goroutines (shares the map with the HWID retry, under a distinct key).
func (a *App) retryAddSubResetInBackground(telegramID int64) {
	key := "addsub:" + strconv.FormatInt(telegramID, 10)
	a.hwidMu.Lock()
	if a.hwidRetrying == nil {
		a.hwidRetrying = map[string]bool{}
	}
	if a.hwidRetrying[key] {
		a.hwidMu.Unlock()
		return
	}
	a.hwidRetrying[key] = true
	a.hwidMu.Unlock()

	go func() {
		defer func() {
			a.hwidMu.Lock()
			delete(a.hwidRetrying, key)
			a.hwidMu.Unlock()
			if r := recover(); r != nil {
				a.log.Error("addsub reset background retry panicked", "tg_id", telegramID, "err", r)
			}
		}()
		ctx, cancel := context.WithTimeout(a.bgContext(), hwidBackgroundBudget)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				a.log.Warn("addsub: сброс устройств доп-подписки не удался и после фоновых попыток", "tg_id", telegramID)
				return
			case <-time.After(addSubResetRetryEvery):
			}
			panel, enabled, suffix, _, _ := a.addSubParams()
			if !enabled || panel == nil {
				return
			}
			res, found, err := panel.ResetAddSubDevices(ctx, telegramID, suffix)
			if err != nil {
				continue
			}
			if found && res.HwidErr != nil {
				a.clearHwidInBackground(telegramID, res.Ref)
			}
			if found {
				a.log.Info("addsub: сброс устройств доп-подписки добит в фоне", "tg_id", telegramID)
			}
			return
		}
	}()
}
