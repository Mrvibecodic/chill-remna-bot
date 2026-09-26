package app

import (
	"context"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/tribute"
)

func (a *App) showTributeAdmin(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	cfg := a.tributeCfg()
	status := i18n.T(lang, "admin.off")
	if cfg.Enabled {
		status = i18n.T(lang, "admin.on")
	}
	key := i18n.T(lang, "admin.no")
	if cfg.APIKey != "" {
		key = i18n.T(lang, "admin.yes")
	}
	url := i18n.T(lang, "admin.none")
	if cfg.PayURL != "" {
		url = html.EscapeString(cfg.PayURL)
	}
	bound := 0
	if plans, err := a.planList(ctx); err == nil {
		for i := range plans {
			if plans[i].Code != model.PlanCodeBase && a.tributePlanReady(plans[i].Code) {
				bound++
			}
		}
	}
	text := i18n.T(lang, "trb.title", status, key, url, bound)
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{toggleBtn(lang, cfg.Enabled, "trb:toggle")},
		{btn(i18n.T(lang, "trb.btn_key"), "trb:key"), btn(i18n.T(lang, "trb.btn_url"), "trb:url")},
		{btn(i18n.T(lang, "trb.btn_plans"), "trb:plans")},
		{btn(i18n.T(lang, "btn.back"), "menu:pay"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

func (a *App) onTributeAdmin(ctx context.Context, chatID int64, val string) {
	lang := a.lang(chatID)
	action, arg, _ := strings.Cut(val, ":")
	switch action {
	case "toggle":
		a.mu.Lock()
		if a.botCfg != nil {
			a.botCfg.Tribute.Enabled = !a.botCfg.Tribute.Enabled
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTributeAdmin(ctx, chatID)
	case "key":
		a.getUI(chatID).adminInput = "trb_key"
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_key"), "menu:tribute")
	case "url":
		a.getUI(chatID).adminInput = "trb_url"
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_url"), "menu:tribute")
	case "plans":
		a.showTributePlans(ctx, chatID)
	case "pl":
		a.showTributePlan(ctx, chatID, arg)
	case "ls":
		a.showTributeSubs(ctx, chatID, arg)
	case "ps":
		code, idStr, _ := strings.Cut(arg, ":")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || id <= 0 {
			a.showTributePlan(ctx, chatID, code)
			return
		}
		a.pickTributeSub(ctx, chatID, code, id)
	case "id":
		p := a.tributePlanRow(ctx, arg)
		if p == nil {
			a.showTributePlans(ctx, chatID)
			return
		}
		ui := a.getUI(chatID)
		ui.adminInput = "trb_plid"
		ui.planCode = p.Code
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_id"), "trb:pl:"+p.Code)
	case "u":
		p := a.tributePlanRow(ctx, arg)
		if p == nil {
			a.showTributePlans(ctx, chatID)
			return
		}
		ui := a.getUI(chatID)
		ui.adminInput = "trb_plurl"
		ui.planCode = p.Code
		a.askInput(ctx, chatID, i18n.T(lang, "trb.ask_plurl", planTitleHTML(lang, p)), "trb:pl:"+p.Code)
	case "rm":
		a.unbindTribute(ctx, arg)
		a.showTributePlan(ctx, chatID, arg)
	case "fg", "fgy":
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil || id <= 0 {
			a.showTributePlans(ctx, chatID)
			return
		}
		if action == "fgy" {
			a.forgetTribute(ctx, id)
			a.showTributePlans(ctx, chatID)
			return
		}
		a.askForgetTribute(ctx, chatID, id)
	default:
		a.showTributeAdmin(ctx, chatID)
	}
}

func (a *App) setTributeField(ctx context.Context, chatID int64, field, text string) {
	text = strings.TrimSpace(text)
	// Ссылка оплаты уходит в кнопку: битый адрес Telegram отвергает вместе со
	// ВСЕМ сообщением, и человек, нажавший «оплатить через Tribute», не
	// получает ничего.
	if field == "trb_url" && text != "" && !validButtonURL(text) {
		a.sendHome(ctx, chatID, i18n.T(a.lang(chatID), "trb.url_bad"))
		return
	}
	a.mu.Lock()
	if a.botCfg != nil {
		switch field {
		case "trb_key":
			a.botCfg.Tribute.APIKey = text
		case "trb_url":
			a.botCfg.Tribute.PayURL = text
		}
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showTributeAdmin(ctx, chatID)
}

// tributePlanRow — тариф для экранов привязки. «Базовый» без строки (сбой
// стартовой синхронизации) собирается из сетки, как в витрине.
func (a *App) tributePlanRow(ctx context.Context, code string) *model.Plan {
	if !model.ValidPlanCode(code) {
		return nil
	}
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.log.Warn("tribute: тариф не прочитан", "err", err, "plan", code)
		return nil
	}
	if p == nil && code == model.PlanCodeBase {
		a.mu.Lock()
		p = basePlanFrom(a.botCfg, nil)
		a.mu.Unlock()
	}
	return p
}

// tributePlanReady — показывается ли у тарифа кнопка Tribute.
func (a *App) tributePlanReady(code string) bool {
	cfg := a.tributeCfg()
	if code == model.PlanCodeBase {
		return validButtonURL(cfg.PayURL)
	}
	l := cfg.LinkFor(code)
	return l != nil && l.SubID != 0 && validButtonURL(l.URL)
}

func (a *App) showTributePlans(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	plans, err := a.planList(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "err.storage"), [][]models.InlineKeyboardButton{navBack(lang, "menu:tribute")})
		return
	}
	hasBase := false
	for i := range plans {
		if plans[i].Code == model.PlanCodeBase {
			hasBase = true
			break
		}
	}
	if !hasBase {
		if p := a.tributePlanRow(ctx, model.PlanCodeBase); p != nil {
			plans = append([]model.Plan{*p}, plans...)
		}
	}
	cfg := a.tributeCfg()
	strict := i18n.T(lang, "trb.strict_off")
	if cfg.StrictSubs() {
		strict = i18n.T(lang, "trb.strict_on")
	}
	var rows [][]models.InlineKeyboardButton
	for i := range plans {
		// Лимит Telegram — 100 кнопок на сообщение; тарифов столько не бывает,
		// но сообщение целиком не должно пропадать из-за лишнего.
		if len(rows) >= 90 {
			break
		}
		p := &plans[i]
		mark := "➖"
		if a.tributePlanReady(p.Code) {
			mark = "✅"
		} else if l := cfg.LinkFor(p.Code); l != nil && (l.SubID != 0 || l.URL != "") {
			mark = "⚠️"
		}
		label := mark + " " + planTitle(lang, p)
		if l := cfg.LinkFor(p.Code); l != nil && l.SubID != 0 {
			label += " · #" + strconv.FormatInt(l.SubID, 10)
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "trb:pl:"+p.Code)})
	}
	// Подписки, которые выдают тариф, но не продаются кнопкой: прежние
	// подписки тарифов и привязки удалённых тарифов. Их можно только забыть.
	known := map[string]*model.Plan{}
	for i := range plans {
		known[plans[i].Code] = &plans[i]
	}
	for _, l := range cfg.Links {
		if len(rows) >= 95 {
			break
		}
		if l.SubID == 0 || (!l.Old && known[l.Plan] != nil) {
			continue
		}
		target := l.Plan
		if p := known[l.Plan]; p != nil {
			target = planTitle(lang, p)
		}
		label := "♻️ #" + strconv.FormatInt(l.SubID, 10)
		if l.Name != "" {
			label += " «" + l.Name + "»"
		}
		label += " → " + target
		if known[l.Plan] == nil {
			label += " ❌"
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(truncRunes(label, 60), "trb:fg:"+strconv.FormatInt(l.SubID, 10))})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "menu:tribute"), btn(i18n.T(lang, "btn.home"), "menu:home")})
	a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.plans_title", strict), rows)
}

// askForgetTribute — подтверждение «забыть подписку»: что она выдаёт сейчас и
// что будет после.
func (a *App) askForgetTribute(ctx context.Context, chatID int64, subID int64) {
	lang := a.lang(chatID)
	cfg := a.tributeCfg()
	l := cfg.LinkBySub(subID)
	if l == nil {
		a.showTributePlans(ctx, chatID)
		return
	}
	now := i18n.T(lang, "trb.fg_now_gone", html.EscapeString(l.Plan))
	if p := a.tributePlanRow(ctx, l.Plan); p != nil {
		now = i18n.T(lang, "trb.fg_now", planTitleHTML(lang, p))
	}
	after := i18n.T(lang, "trb.fg_after_base")
	if cfg.StrictSubs() && !(l.Plan == model.PlanCodeBase && !l.Old) {
		after = i18n.T(lang, "trb.fg_after_strict")
	}
	id := strconv.FormatInt(subID, 10)
	text := i18n.T(lang, "trb.fg_ask", id, now, after)
	if l.Plan == model.PlanCodeBase {
		text += "\n\n" + i18n.T(lang, "trb.fg_base_link")
	}
	a.sendPayKB(ctx, chatID, text, [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "trb.btn_fg_yes"), "trb:fgy:"+id)},
		{btn(i18n.T(lang, "btn.back"), "trb:plans"), btn(i18n.T(lang, "btn.home"), "menu:home")},
	})
}

// tributeCovers — продаёт ли тариф срок, который выдаётся по периоду Tribute.
// Та же проверка, что в вебхуке.
func (a *App) tributeCovers(p *model.Plan, months int) bool {
	if p == nil || p.Code == model.PlanCodeBase {
		return a.periodOnSale(months)
	}
	d := p.Duration(months)
	return d != nil && d.Base != ""
}

// tributeCoverage — что бот выдаст по каждому периоду подписки.
func (a *App) tributeCoverage(lang string, p *model.Plan, periods []string) string {
	lines := []string{i18n.T(lang, "trb.cov_head")}
	for _, per := range periods {
		name := html.EscapeString(per)
		days, months, ok := tributePeriod(per)
		if !ok {
			lines = append(lines, i18n.T(lang, "trb.cov_bad", name))
			continue
		}
		term := strconv.Itoa(months) + i18n.T(lang, "plans.mo")
		sale := months
		if days > 0 {
			term = strconv.Itoa(days) + i18n.T(lang, "plans.d")
			sale = 1
		}
		if a.tributeCovers(p, sale) {
			lines = append(lines, i18n.T(lang, "trb.cov_ok", name, term))
		} else {
			lines = append(lines, i18n.T(lang, "trb.cov_miss", name, term))
		}
	}
	return strings.Join(lines, "\n")
}

func (a *App) showTributePlan(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.tributePlanRow(ctx, code)
	if p == nil {
		a.showTributePlans(ctx, chatID)
		return
	}
	cfg := a.tributeCfg()
	l := cfg.LinkFor(p.Code)
	sub := i18n.T(lang, "trb.sub_none")
	if l != nil && l.SubID != 0 {
		sub = "#" + strconv.FormatInt(l.SubID, 10)
		if l.Name != "" {
			sub += " «" + html.EscapeString(l.Name) + "»"
		}
	}
	url := ""
	if p.Code == model.PlanCodeBase {
		url = cfg.PayURL
	} else if l != nil {
		url = l.URL
	}
	urlText := i18n.T(lang, "admin.none")
	if url != "" {
		urlText = html.EscapeString(url)
	}
	coverage := ""
	switch {
	case l != nil && len(l.Periods) > 0:
		coverage = a.tributeCoverage(lang, p, l.Periods)
	case l != nil && l.SubID != 0:
		coverage = i18n.T(lang, "trb.cov_unknown")
	}
	var olds []string
	for _, x := range cfg.Links {
		if x.Plan == p.Code && x.Old && x.SubID != 0 {
			o := "#" + strconv.FormatInt(x.SubID, 10)
			if x.Name != "" {
				o += " «" + html.EscapeString(x.Name) + "»"
			}
			olds = append(olds, o)
		}
	}
	if len(olds) > 0 {
		if coverage != "" {
			coverage += "\n\n"
		}
		coverage += i18n.T(lang, "trb.old_subs", strings.Join(olds, ", "))
	}
	status := i18n.T(lang, "trb.status_off")
	switch {
	case !cfg.Enabled:
		status = i18n.T(lang, "trb.status_disabled")
	case a.tributePlanReady(p.Code):
		status = i18n.T(lang, "trb.status_ok")
	case p.Code == model.PlanCodeBase:
		status = i18n.T(lang, "trb.status_off_base")
	}
	if p.Code == model.PlanCodeBase && l != nil && l.SubID != 0 {
		status += "\n\n" + i18n.T(lang, "trb.base_strict_note")
	}
	text := i18n.T(lang, "trb.plan_title", planTitleHTML(lang, p), sub, urlText, coverage, status)
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "trb.btn_pick"), "trb:ls:"+p.Code), btn(i18n.T(lang, "trb.btn_id"), "trb:id:"+p.Code)},
		{btn(i18n.T(lang, "trb.btn_plurl"), "trb:u:"+p.Code)},
	}
	if l != nil {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "trb.btn_unbind"), "trb:rm:"+p.Code)})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "trb:plans"), btn(i18n.T(lang, "btn.home"), "menu:home")})
	a.sendPayKB(ctx, chatID, text, rows)
}

// tributeSubs — подписки автора из API Tribute.
func (a *App) tributeSubs(ctx context.Context) ([]tribute.Subscription, error) {
	return tribute.ListSubscriptions(ctx, a.tributeCfg().APIKey)
}

func (a *App) showTributeSubs(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.tributePlanRow(ctx, code)
	if p == nil {
		a.showTributePlans(ctx, chatID)
		return
	}
	back := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "trb.btn_id"), "trb:id:"+p.Code)},
		{btn(i18n.T(lang, "btn.back"), "trb:pl:"+p.Code), btn(i18n.T(lang, "btn.home"), "menu:home")},
	}
	subs, err := a.tributeSubs(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.list_fail", shortErr(err)), back)
		return
	}
	if len(subs) == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.list_empty"), back)
		return
	}
	cfg := a.tributeCfg()
	var rows [][]models.InlineKeyboardButton
	for _, s := range subs {
		if len(rows) >= 90 {
			break
		}
		label := s.Name
		if label == "" {
			label = "#" + strconv.FormatInt(s.ID, 10)
		}
		var pers []string
		for _, per := range s.Periods {
			if per.Period != "" {
				pers = append(pers, per.Period)
			}
		}
		if len(pers) > 0 {
			label += " · " + strings.Join(pers, ", ")
		}
		if other := cfg.LinkBySub(s.ID); other != nil && other.Plan != p.Code {
			label = "🔒 " + label
		} else if other != nil {
			label = "✅ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(truncRunes(label, 60), "trb:ps:"+p.Code+":"+strconv.FormatInt(s.ID, 10))})
	}
	rows = append(rows, back...)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.list_title", planTitleHTML(lang, p)), rows)
}

// pickTributeSub — выбор подписки из списка. Название и периоды берутся из
// того же API: callback несёт только ID.
func (a *App) pickTributeSub(ctx context.Context, chatID int64, code string, id int64) {
	lang := a.lang(chatID)
	subs, err := a.tributeSubs(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.list_fail", shortErr(err)),
			[][]models.InlineKeyboardButton{navBack(lang, "trb:pl:"+code)})
		return
	}
	for _, s := range subs {
		if s.ID == id {
			a.bindTribute(ctx, chatID, code, s)
			return
		}
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.sub_gone"), [][]models.InlineKeyboardButton{navBack(lang, "trb:ls:"+code)})
}

// bindTribute делает подписку текущей для тарифа. Одна подписка — один тариф:
// иначе вебхук не знал бы, что выдавать. Прежняя подписка тарифа остаётся за
// ним (Old): её подписчики продолжают платить и получать этот тариф.
func (a *App) bindTribute(ctx context.Context, chatID int64, code string, s tribute.Subscription) {
	lang := a.lang(chatID)
	p := a.tributePlanRow(ctx, code)
	if p == nil {
		a.showTributePlans(ctx, chatID)
		return
	}
	var pers []string
	for _, per := range s.Periods {
		if per.Period != "" {
			pers = append(pers, per.Period)
		}
	}
	a.mu.Lock()
	if a.botCfg == nil {
		a.mu.Unlock()
		return
	}
	cur := a.botCfg.Tribute
	if other := cur.LinkBySub(s.ID); other != nil && other.Plan != p.Code {
		taken, old := other.Plan, other.Old
		a.mu.Unlock()
		name := html.EscapeString(taken)
		if op := a.tributePlanRow(ctx, taken); op != nil {
			name = planTitleHTML(lang, op)
		}
		key := "trb.sub_taken"
		if old {
			key = "trb.sub_taken_old"
		}
		a.sendPayKB(ctx, chatID, i18n.T(lang, key, name), [][]models.InlineKeyboardButton{navBack(lang, "trb:pl:"+p.Code)})
		return
	}
	next := model.TributeLink{Plan: p.Code, SubID: s.ID, Name: s.Name, Periods: pers}
	// Подписка, на которую вела ссылка тарифа: текущая, а после «Отвязать» —
	// последняя прежняя.
	prevSub := int64(0)
	if l := cur.LinkFor(p.Code); l != nil {
		prevSub = l.SubID
	} else {
		for _, l := range cur.Links {
			if l.Plan == p.Code && l.Old {
				prevSub = l.SubID
			}
		}
	}
	// Срез привязок заменяется целиком: копии конфига, снятые раньше, делят
	// с ним память и читаются без замка.
	links := make([]model.TributeLink, 0, len(cur.Links)+2)
	for _, l := range cur.Links {
		if l.SubID == s.ID && l.SubID != 0 {
			// Та же подписка (текущая или прежняя) — её заменит next. Без
			// ответа API имя и периоды остаются прежними.
			if next.Name == "" && len(next.Periods) == 0 {
				next.Name, next.Periods = l.Name, l.Periods
			}
			if !l.Old {
				next.URL = l.URL
			}
			continue
		}
		if l.Plan == p.Code && !l.Old {
			if l.SubID == 0 {
				// Была только ссылка, подписки не было — ссылка остаётся.
				next.URL = l.URL
				continue
			}
			// Другая подписка уходит в прежние; её ссылка — ссылка на ту
			// подписку, у новой она была бы чужой.
			l.Old, l.URL = true, ""
		}
		links = append(links, l)
	}
	links = append(links, next)
	a.botCfg.Tribute.Links = links
	if p.Code == model.PlanCodeBase && prevSub != 0 && prevSub != s.ID {
		// Ссылка «Базового» вела на прежнюю подписку — продавать новую она не
		// может, как и ссылка любого тарифа после перепривязки.
		a.botCfg.Tribute.PayURL = ""
	}
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
	a.showTributePlan(ctx, chatID, p.Code)
}

// unbindTribute снимает текущую подписку тарифа: кнопки Tribute у тарифа
// больше нет. Подписка остаётся за тарифом прежней — её подписчики платят за
// него и продолжают его получать; совсем забыть подписку можно отдельной
// кнопкой. У «Базового» ссылка оплаты живёт отдельно (PayURL) и остаётся.
func (a *App) unbindTribute(ctx context.Context, code string) {
	a.mu.Lock()
	if a.botCfg == nil || a.botCfg.Tribute.LinkFor(code) == nil {
		a.mu.Unlock()
		return
	}
	links := make([]model.TributeLink, 0, len(a.botCfg.Tribute.Links))
	for _, l := range a.botCfg.Tribute.Links {
		if l.Plan == code && !l.Old {
			if l.SubID == 0 {
				continue
			}
			l.Old, l.URL = true, ""
		}
		links = append(links, l)
	}
	a.botCfg.Tribute.Links = links
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
}

// forgetTribute удаляет подписку из привязок совсем: дальше она ведёт себя
// как непривязанная.
func (a *App) forgetTribute(ctx context.Context, subID int64) {
	a.mu.Lock()
	if a.botCfg == nil || a.botCfg.Tribute.LinkBySub(subID) == nil {
		a.mu.Unlock()
		return
	}
	links := make([]model.TributeLink, 0, len(a.botCfg.Tribute.Links))
	for _, l := range a.botCfg.Tribute.Links {
		if l.SubID != subID {
			links = append(links, l)
		}
	}
	a.botCfg.Tribute.Links = links
	a.mu.Unlock()
	_ = a.saveBotConfig(ctx)
}

// setTributeLinkField — ввод ID подписки или ссылки оплаты для тарифа.
func (a *App) setTributeLinkField(ctx context.Context, chatID int64, code, field, text string) {
	lang := a.lang(chatID)
	text = strings.TrimSpace(text)
	p := a.tributePlanRow(ctx, code)
	if p == nil {
		a.showTributePlans(ctx, chatID)
		return
	}
	switch field {
	case "trb_plid":
		id, err := strconv.ParseInt(strings.TrimPrefix(text, "#"), 10, 64)
		if err != nil || id <= 0 {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.id_bad"), [][]models.InlineKeyboardButton{navBack(lang, "trb:pl:"+p.Code)})
			return
		}
		// Название и периоды — по возможности из API; без них привязка всё
		// равно работает, экран лишь не покажет периоды.
		s := tribute.Subscription{ID: id}
		if subs, err := a.tributeSubs(ctx); err == nil {
			for _, x := range subs {
				if x.ID == id {
					s = x
					break
				}
			}
		}
		a.bindTribute(ctx, chatID, p.Code, s)
	case "trb_plurl":
		// Ссылка уходит в кнопку: битый адрес Telegram отвергает вместе со
		// всем сообщением.
		if !validButtonURL(text) {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "trb.url_bad"), [][]models.InlineKeyboardButton{navBack(lang, "trb:pl:"+p.Code)})
			return
		}
		a.mu.Lock()
		if a.botCfg == nil {
			a.mu.Unlock()
			return
		}
		if p.Code == model.PlanCodeBase {
			a.botCfg.Tribute.PayURL = text
		} else {
			cur := a.botCfg.Tribute.Links
			links := make([]model.TributeLink, 0, len(cur)+1)
			found := false
			for _, l := range cur {
				// Только текущая привязка: прежние подписки кнопкой не продаются.
				if l.Plan == p.Code && !l.Old {
					l.URL = text
					found = true
				}
				links = append(links, l)
			}
			if !found {
				links = append(links, model.TributeLink{Plan: p.Code, URL: text})
			}
			a.botCfg.Tribute.Links = links
		}
		a.mu.Unlock()
		_ = a.saveBotConfig(ctx)
		a.showTributePlan(ctx, chatID, p.Code)
	}
}
