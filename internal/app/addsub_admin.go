package app

import (
	"context"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func (a *App) showAddSubAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	c := a.botCfg.AddSub
	// Набор сквадов читается ниже без замка, а правится он на месте — копия.
	c.InternalSquads = append([]string(nil), c.InternalSquads...)
	a.mu.Unlock()

	squadNames, _ := a.squadNames(ctx, c.InternalSquads, "")
	if squadNames == "" {
		squadNames = i18n.T(lang, "admin.none")
	}
	state := i18n.T(lang, "addsub.off")
	if c.Enabled {
		state = i18n.T(lang, "addsub.on")
	}
	traffic := i18n.T(lang, "addsub.unlimited")
	if c.TrafficGB > 0 {
		traffic = strconv.Itoa(c.TrafficGB) + " GB"
	}
	toggleLabel := i18n.T(lang, "addsub.btn_enable")
	if c.Enabled {
		toggleLabel = i18n.T(lang, "addsub.btn_disable")
	}
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = i18n.T(lang, "addsub.default_name")
	}
	desc := strings.TrimSpace(c.Description)
	if desc == "" {
		desc = i18n.T(lang, "admin.none")
	}
	title := i18n.T(lang, "addsub.title", state, traffic, squadNames) +
		"\n\n" + i18n.T(lang, "addsub.texts_line", html.EscapeString(name), html.EscapeString(desc))
	rows := [][]models.InlineKeyboardButton{
		{btn(toggleLabel, "addsub:toggle")},
		{btn(i18n.T(lang, "addsub.btn_gb"), "addsub:gb"), btn(i18n.T(lang, "addsub.btn_squads"), "addsub:squads")},
		{btn(i18n.T(lang, "addsub.btn_name"), "addsub:name"), btn(i18n.T(lang, "addsub.btn_desc"), "addsub:desc")},
	}
	if c.Enabled {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "addsub.btn_sync"), "addsub:sync")})
	}
	rows = append(rows, []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home"),
	})
	a.sendPayKB(ctx, chatID, title, rows)
}

func (a *App) onAddSubAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := cut3(val)
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.AddSub.Enabled = !a.botCfg.AddSub.Enabled
			a.botCfg.AddSub.Init = true
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showAddSubAdmin(ctx, chatID)
	case "gb":
		a.getUI(chatID).adminInput = "addsub_gb"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "addsub.ask_gb"), "menu:addsub")
	case "name":
		a.getUI(chatID).adminInput = "addsub_name"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "addsub.ask_name"), "menu:addsub")
	case "desc":
		a.getUI(chatID).adminInput = "addsub_desc"
		a.askInput(ctx, chatID, i18n.T(a.lang(chatID), "addsub.ask_desc"), "menu:addsub")
	case "squads", "refresh", "noop":
		a.showAddSubSquads(ctx, chatID)
	case "int":
		a.toggleAddSubInternal(ctx, chatID, arg)
		a.showAddSubSquads(ctx, chatID)
	case "sync":
		switch arg {
		case "upd":
			a.startAddSubBackfill(ctx, chatID, true)
		case "all", "go":
			// go — кнопка экрана прежних версий: она создавала доп-подписки.
			a.startAddSubBackfill(ctx, chatID, false)
		default:
			a.sendPayKB(ctx, chatID, i18n.T(a.lang(chatID), "addsub.sync_confirm"), [][]models.InlineKeyboardButton{
				{btn(i18n.T(a.lang(chatID), "addsub.btn_sync_upd"), "addsub:sync:upd")},
				{btn(i18n.T(a.lang(chatID), "addsub.btn_sync_all"), "addsub:sync:all")},
				{btn(i18n.T(a.lang(chatID), "btn.back"), "menu:addsub")},
			})
		}
	}
}

// addSubBackfillBudget bounds the whole backfill run, so an unreachable panel
// can't leave the goroutine (and the "syncing" flag) alive forever.
const addSubBackfillBudget = 2 * time.Hour

// addSubBackfillPage is the panel page size for the backfill walk, and
// addSubBackfillPause paces the per-user upserts so the walk never floods the
// panel with requests.
const (
	addSubBackfillPage  = 100
	addSubBackfillPause = 50 * time.Millisecond
)

// startAddSubBackfill mirrors the add-on subscription onto every active
// bot-owned panel user. Without it, enabling the feature only reaches users at
// their next purchase — everyone already paid up would stay without B (and so
// without the merge) until they renew. onlyExisting — режим «обновить тем, у
// кого есть»: условия пишутся в созданные доп-подписки, новые не создаются.
func (a *App) startAddSubBackfill(ctx context.Context, chatID int64, onlyExisting bool) {
	lang := a.lang(chatID)
	panel, enabled, opt := a.addSubOptions(false)
	opt.OnlyExisting = onlyExisting
	// The explicit admin action is the only place allowed to move a legacy-named
	// add-on onto the discoverable name (it deletes the old panel user).
	opt.MigrateLegacyName = true
	if !enabled || panel == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "addsub.sync_off"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.back"), "menu:addsub")},
		})
		return
	}
	if !a.addSubSyncing.CompareAndSwap(false, true) {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "addsub.sync_busy"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "btn.back"), "menu:addsub")},
		})
		return
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "addsub.sync_started"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.back"), "menu:addsub")},
	})
	mode := "addsub.btn_sync_all"
	if onlyExisting {
		mode = "addsub.btn_sync_upd"
	}
	head := "<b>" + i18n.T(lang, mode) + "</b>\n"
	go func() {
		defer a.addSubSyncing.Store(false)
		bg, cancel := context.WithTimeout(a.bgContext(), addSubBackfillBudget)
		st := a.addSubBackfill(bg, panel, opt)
		cancel()
		// The report goes out on its own context: when the run stops because the
		// budget expired, bg is already dead and the admin would hear nothing.
		rep, repCancel := context.WithTimeout(a.bgContext(), 30*time.Second)
		defer repCancel()
		if st.err != nil {
			a.notify(rep, chatID, head+i18n.T(lang, "addsub.sync_err", st.err.Error(), st.ok, st.migrated, st.failed))
			return
		}
		a.notify(rep, chatID, head+i18n.T(lang, "addsub.sync_done", st.ok, st.migrated, st.failed))
	}()
}

// addSubStats is what a backfill run has done so far.
type addSubStats struct {
	ok       int // add-on created or updated
	migrated int // add-on moved off the legacy name
	failed   int
	err      error
}

// addSubTargets keeps only the panel users the bot may mirror an add-on for:
// an account it manages (its own tag, or none at all — panelsync links adopted
// accounts by telegramId without tagging them) that carries a telegram id.
// A foreign tag means the account belongs to another system: never touch it.
func addSubTargets(users []remnawave.PanelUser) []remnawave.PanelUser {
	out := make([]remnawave.PanelUser, 0, len(users))
	for i := range users {
		u := users[i]
		if u.TelegramID <= 0 || u.Tag == remnawave.BotTagAdd {
			continue
		}
		if u.Tag != "" && u.Tag != remnawave.BotTag {
			continue
		}
		out = append(out, u)
	}
	return out
}

// collectAddSubTargets reads the whole user list BEFORE anything is written.
// The backfill creates (and, when migrating, deletes) panel users, which shifts
// an offset-paginated list under its own feet and would skip entries.
func (a *App) collectAddSubTargets(ctx context.Context, panel *remnawave.Client) ([]remnawave.PanelUser, error) {
	var out []remnawave.PanelUser
	for start := 0; ; start += addSubBackfillPage {
		users, total, err := panel.ListUsersPage(ctx, start, addSubBackfillPage)
		if err != nil {
			return out, err
		}
		if len(users) == 0 {
			return out, nil
		}
		out = append(out, addSubTargets(users)...)
		if start+len(users) >= total {
			return out, nil
		}
	}
}

// addSubBackfill mirrors the add-on onto every active bot-owned user. Traffic is
// never reset here: a backfill is not a renewal. Expired accounts and add-on
// users themselves are skipped inside the panel client.
func (a *App) addSubBackfill(ctx context.Context, panel *remnawave.Client, opt remnawave.AddSubOptions) (st addSubStats) {
	targets, err := a.collectAddSubTargets(ctx, panel)
	if err != nil {
		st.err = err
		return st
	}
	for i := range targets {
		if ctx.Err() != nil {
			st.err = ctx.Err()
			return st
		}
		u := targets[i]
		// Трафик и сквады — те, что проданы сделкой пользователя, иначе общие.
		// Сделка без опции получает её, если у тарифа она не выключена.
		snap, trial := a.addSubDeal(ctx, u.TelegramID)
		granted := false
		if !snap.AddSubSold() {
			if opt.OnlyExisting {
				continue
			}
			byPlan, gerr := a.addSubByPlan(ctx, snap)
			if gerr != nil {
				st.failed++
				a.log.Warn("addsub: backfill", "user", u.Username, "err", gerr)
				continue
			}
			if byPlan == nil {
				continue
			}
			snap, granted = byPlan, true
		}
		uopt := opt
		// На триале действуют общие условия, даже если осталась прошлая сделка.
		if !trial && applyAddSubSnap(&uopt, snap, addSubLiveFor(ctx, panel, snap)) {
			a.log.Warn("addsub: все сквады тарифа удалены в панели, взяты общие", "user", u.Username, "plan", snap.Code)
		}
		res, uerr := panel.UpsertAddSubForUser(ctx, u, uopt)
		if res.Done && granted {
			// Опция попадает в снимок только вместе с созданной доп-подпиской:
			// истёкшей подписке она не создаётся, и считать её проданной рано.
			// Созданная при частичном сбое — уже создана, отметка нужна и ей.
			if gerr := a.grantAddSub(ctx, u.TelegramID, snap); gerr != nil && uerr == nil {
				uerr = gerr
			}
		}
		switch {
		case uerr != nil:
			st.failed++
			a.log.Warn("addsub: backfill", "user", u.Username, "err", uerr)
		case res.Done:
			st.ok++
		}
		// Counted only on a clean run, so a half-failed migration never shows
		// up as both renamed and failed.
		if uerr == nil && res.Migrated {
			st.migrated++
			a.log.Info("addsub: доп-подписка переведена на новое имя",
				"user", u.Username, "legacy", res.Legacy)
		}
		select {
		case <-ctx.Done():
			st.err = ctx.Err()
			return st
		case <-time.After(addSubBackfillPause):
		}
	}
	return st
}

// addSubByPlan — сделка записана без доп-подписки (так бывает, когда покупка
// прошла при выключенной доп-подписке): снимок с опцией на сегодняшних
// условиях тарифа, а если тариф удалён — на общих. nil без ошибки — опция
// выключена у самого тарифа.
func (a *App) addSubByPlan(ctx context.Context, snap *model.PlanSnapshot) (*model.PlanSnapshot, error) {
	p, err := a.planByCode(ctx, snapCodeOr(snap, model.PlanCodeBase))
	if err != nil {
		return nil, err
	}
	if !a.planAddSubOn(p) {
		return nil, nil
	}
	out := *snap
	sold := true
	out.AddSub = &sold
	out.AddSubTrafficGB, out.AddSubSquads = planAddSubTerms(p)
	return &out, nil
}

// grantAddSub записывает выданную опцию в снимок пользователя — иначе
// продление бонусом или разблокировка выключили бы только что созданную
// доп-подписку. granted — снимок, по которому она создана.
func (a *App) grantAddSub(ctx context.Context, tgID int64, granted *model.PlanSnapshot) error {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil
	}
	// Снимок пишется целиком: без замка выдачи запись затёрла бы сделку,
	// оплаченную между чтением и записью.
	lk := &a.finalizeUserLk[extLockIndex(strconv.FormatInt(tgID, 10))]
	lk.Lock()
	defer lk.Unlock()
	u, err := st.GetUser(ctx, tgID)
	if err != nil {
		return err
	}
	// Опция уже есть или сделка успела смениться — у новой свой ответ.
	if u == nil || u.Snapshot.AddSubSold() || planCodeOf(u.Snapshot) != planCodeOf(granted) {
		return nil
	}
	snap := *u.Snapshot
	snap.AddSub = granted.AddSub
	snap.AddSubTrafficGB, snap.AddSubSquads = granted.AddSubTrafficGB, granted.AddSubSquads
	return st.SetUserSnapshot(ctx, tgID, &snap)
}

// addSubDeal — снимок последней сделки пользователя и идёт ли у него триал.
func (a *App) addSubDeal(ctx context.Context, tgID int64) (*model.PlanSnapshot, bool) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil, false
	}
	u, err := st.GetUser(ctx, tgID)
	if err != nil || u == nil {
		return nil, false
	}
	return u.Snapshot, u.NotifyKind == "trial"
}

func (a *App) showAddSubSquads(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.mu.Lock()
	panel := a.panel
	activeInt := append([]string(nil), a.botCfg.AddSub.InternalSquads...)
	a.mu.Unlock()

	back := []models.InlineKeyboardButton{
		btn(i18n.T(lang, "btn.back"), "menu:addsub"),
		btn(i18n.T(lang, "btn.home"), "menu:home"),
	}
	if panel == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.no_panel"), [][]models.InlineKeyboardButton{back})
		return
	}
	intSquads, err := panel.ListSquads(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "squads.err", err.Error()), [][]models.InlineKeyboardButton{back})
		return
	}
	live := panel.LiveSquads(ctx)

	isActiveInt := func(uuid string) bool {
		for _, u := range activeInt {
			if u == uuid {
				return true
			}
		}
		return false
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(intSquads)+2)
	for _, sq := range intSquads {
		mark := "⬜"
		if isActiveInt(sq.UUID) {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(mark+" 🏠 "+sq.Name, "addsub:int:"+sq.UUID)})
	}
	gone := goneSquadRows(lang, live, activeInt, "",
		func(u string) string { return "addsub:int:" + u }, nil)
	rows = append(rows, gone...)
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "squads.btn_refresh"), "addsub:refresh")})
	rows = append(rows, back)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "addsub.squads_title", len(intSquads), len(activeInt))+goneSquadsHint(lang, gone), rows)
}

func (a *App) toggleAddSubInternal(ctx context.Context, chatID int64, uuid string) {
	if uuid == "" {
		return
	}
	live := a.liveSquads(ctx)
	a.mu.Lock()
	if a.botCfg != nil {
		cur := a.botCfg.AddSub.InternalSquads
		idx := -1
		for i, u := range cur {
			if u == uuid {
				idx = i
				break
			}
		}
		if idx >= 0 {
			a.botCfg.AddSub.InternalSquads = append(cur[:idx], cur[idx+1:]...)
		} else {
			a.botCfg.AddSub.InternalSquads = append(cur, uuid)
		}
		a.botCfg.AddSub.InternalSquads, _ = pruneGoneSquads(live, a.botCfg.AddSub.InternalSquads, "")
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
}
