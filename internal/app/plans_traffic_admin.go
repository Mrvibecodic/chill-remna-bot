package app

import (
	"context"
	"errors"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

var errPlanKind = errors.New("действие не подходит виду тарифа")

var errPackGone = errors.New("пакет не найден")

func trafficPlanSells(p *model.Plan) bool {
	return p.IsTraffic() && p.Traffic != nil && p.TrafficGB > 0 && hasPrice(p.Traffic.Price, p.Traffic.Stars)
}

func packSells(pk *model.PlanPack) bool {
	return pk != nil && pk.GB > 0 && hasPrice(pk.Price, pk.Stars)
}

func hasPrice(price string, stars int) bool {
	k, ok := rubToKopecks(price)
	return (ok && k > 0) || stars > 0
}

func (a *App) showPlanKindChoice(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.kind_title"), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "plans.btn_kind_term"), "pln:newk:t"), btn(i18n.T(lang, "plans.btn_kind_traffic"), "pln:newk:r")},
		navBack(lang, "pln:list"),
	})
}

func priceShown(lang, price, currency string) string {
	price = strings.TrimSpace(price)
	if price == "" {
		return i18n.T(lang, "admin.none")
	}
	return price + curSuffix(curSymbol(currency))
}

func starsShown(lang string, stars int) string {
	if stars <= 0 {
		return i18n.T(lang, "admin.none")
	}
	return strconv.Itoa(stars) + " ⭐"
}

func trafficTerms(p *model.Plan) model.PlanTraffic {
	if p == nil || p.Traffic == nil {
		return model.PlanTraffic{}
	}
	return *p.Traffic
}

var trafficInputs = map[string]struct{ input, key, back string }{
	"p": {"plan_t_price", "plans.ask_t_price", "pln:pr:"},
	"s": {"plan_t_stars", "plans.ask_t_stars", "pln:pr:"},
	"g": {"plan_t_gb", "plans.ask_t_gb", "pln:pr:"},
	"r": {"plan_t_refgb", "plans.ask_t_refgb", "pln:rf:"},
}

func (a *App) askTrafficInput(ctx context.Context, chatID int64, field, code string) {
	spec, ok := trafficInputs[field]
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !ok || !p.IsTraffic() {
		a.showPlanCard(ctx, chatID, code)
		return
	}
	a.askPlanTextTo(ctx, chatID, code, spec.input, spec.key, spec.back+code)
}

func parseGB(text string) (int, bool) {
	n, ok := parseGBOrZero(text)
	return n, ok && n > 0
}

func parseGBOrZero(text string) (int, bool) {
	n, err := strconv.Atoi(normPriceStr(strings.TrimSpace(text)))
	return n, err == nil && n >= 0 && n <= model.PlanTrafficMaxGB
}

func parsePlanPrice(text string) (string, error) {
	v := normPriceStr(strings.TrimSpace(text))
	if v == "" || v == "-" {
		return "", nil
	}
	if k, ok := rubToKopecks(v); !ok || k <= 0 {
		return "", errPlanPriceInvalid
	}
	return v, nil
}

func parseStars(text string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return n, err == nil && n >= 0
}

func (a *App) applyTrafficInput(ctx context.Context, chatID int64, field, text string) {
	ui := a.getUI(chatID)
	code := ui.planCode
	ui.adminInput = ""
	ui.planCode = ""
	lang := a.lang(chatID)
	done, backTo := a.showPlanPricing, "pln:pr:"+code
	if field == "plan_t_refgb" {
		done, backTo = a.showPlanRef, "pln:rf:"+code
	}
	back := [][]models.InlineKeyboardButton{navBack(lang, backTo)}
	var apply func(p *model.Plan) error
	switch field {
	case "plan_t_price":
		v, err := parsePlanPrice(text)
		if err != nil {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.price_invalid"), back)
			return
		}
		apply = func(p *model.Plan) error {
			if p.Traffic == nil {
				p.Traffic = &model.PlanTraffic{}
			}
			p.Traffic.Price = v
			return nil
		}
	case "plan_t_stars":
		n, ok := parseStars(text)
		if !ok {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.t_stars_invalid"), back)
			return
		}
		apply = func(p *model.Plan) error {
			if p.Traffic == nil {
				p.Traffic = &model.PlanTraffic{}
			}
			p.Traffic.Stars = n
			return nil
		}
	case "plan_t_gb":
		n, ok := parseGB(text)
		if !ok {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.t_gb_invalid"), back)
			return
		}
		apply = func(p *model.Plan) error {
			p.TrafficGB = n
			return nil
		}
	case "plan_t_refgb":
		n, ok := parseGBOrZero(text)
		if !ok {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.ref_gb_invalid"), back)
			return
		}
		apply = func(p *model.Plan) error {
			if p.Traffic == nil {
				p.Traffic = &model.PlanTraffic{}
			}
			p.Traffic.RefGB = n
			return nil
		}
	default:
		a.showPlanCard(ctx, chatID, code)
		return
	}
	if _, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		if !p.IsTraffic() {
			return errPlanKind
		}
		return apply(p)
	}); err != nil {
		a.trafficEditFailed(ctx, chatID, code, err)
		return
	}
	done(ctx, chatID, code)
}

func (a *App) trafficEditFailed(ctx context.Context, chatID int64, code string, err error) {
	lang := a.lang(chatID)
	switch {
	case errors.Is(err, errPlanKind):
		a.showPlanCard(ctx, chatID, code)
	case errors.Is(err, errPackGone):
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.pack_gone"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:pk:"+code)})
	default:
		a.planEditFailed(ctx, chatID, code, err)
	}
}

func (a *App) showPlanPacks(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !p.IsTraffic() {
		a.showPlanCard(ctx, chatID, code)
		return
	}
	t := trafficTerms(p)
	list := i18n.T(lang, "plans.packs_none")
	var rows [][]models.InlineKeyboardButton
	if len(t.Packs) > 0 {
		lines := make([]string, 0, len(t.Packs))
		for i, pk := range t.Packs {
			lines = append(lines, i18n.T(lang, "plans.pack_line", i+1, pk.GB,
				html.EscapeString(priceShown(lang, pk.Price, p.Currency)), starsShown(lang, pk.Stars)))
			rows = append(rows, []models.InlineKeyboardButton{
				btn(i18n.T(lang, "plans.pack_btn", pk.GB, priceShown(lang, pk.Price, p.Currency)), "pln:pke:"+pk.ID+":"+p.Code),
			})
		}
		list = strings.Join(lines, "\n")
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "plans.btn_pack_add"), "pln:pka:"+p.Code)},
		navBack(lang, "pln:open:"+p.Code))
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.packs_title", planTitleHTML(lang, p), list), rows)
}

func (a *App) showPlanPack(ctx context.Context, chatID int64, code, id string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	pk := p.Pack(id)
	if pk == nil {
		a.trafficEditFailed(ctx, chatID, code, errPackGone)
		return
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.pack_title", pk.GB, planTitleHTML(lang, p),
		html.EscapeString(priceShown(lang, pk.Price, p.Currency)), starsShown(lang, pk.Stars)),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "plans.btn_pack_gb"), "pln:pki:g:"+id+":"+code), btn(i18n.T(lang, "plans.btn_pack_price"), "pln:pki:p:"+id+":"+code)},
			{btn(i18n.T(lang, "plans.btn_pack_stars"), "pln:pki:s:"+id+":"+code), btn(i18n.T(lang, "plans.btn_pack_del"), "pln:pkx:"+id+":"+code)},
			navBack(lang, "pln:pk:"+code),
		})
}

var packInputs = map[string]struct{ input, key string }{
	"g": {"plan_pk_gb", "plans.ask_pack_gb"},
	"p": {"plan_pk_price", "plans.ask_pack_price"},
	"s": {"plan_pk_stars", "plans.ask_pack_stars"},
}

func (a *App) askPackInput(ctx context.Context, chatID int64, field, id, code string) {
	spec, ok := packInputs[field]
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !ok || p.Pack(id) == nil {
		a.trafficEditFailed(ctx, chatID, code, errPackGone)
		return
	}
	a.askPlanTextTo(ctx, chatID, code, spec.input, spec.key, "pln:pke:"+id+":"+code)
	a.getUI(chatID).planPack = id
}

func (a *App) askPackAdd(ctx context.Context, chatID int64, code string) {
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !p.IsTraffic() {
		a.showPlanCard(ctx, chatID, code)
		return
	}
	if len(trafficTerms(p).Packs) >= model.PlanPacksMax {
		lang := a.lang(chatID)
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.packs_full", model.PlanPacksMax),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:pk:"+code)})
		return
	}
	a.askPlanTextTo(ctx, chatID, code, "plan_pk_add", "plans.ask_pack_add", "pln:pk:"+code)
}

func parsePackLine(text string) (model.PlanPack, bool) {
	text = strings.ReplaceAll(text, "\u00a0", " ")
	var f []string
	if strings.Contains(text, "/") {
		for _, part := range strings.Split(text, "/") {
			f = append(f, strings.TrimSpace(part))
		}
	} else {
		f = strings.Fields(text)
		if len(f) != 2 {
			return model.PlanPack{}, false
		}
	}
	if len(f) < 2 || len(f) > 3 {
		return model.PlanPack{}, false
	}
	gb, ok := parseGB(f[0])
	if !ok {
		return model.PlanPack{}, false
	}
	price, err := parsePlanPrice(f[1])
	if err != nil {
		return model.PlanPack{}, false
	}
	pk := model.PlanPack{GB: gb, Price: price}
	if len(f) == 3 {
		n, ok := parseStars(f[2])
		if !ok {
			return model.PlanPack{}, false
		}
		pk.Stars = n
	}
	return pk, packSells(&pk)
}

func (a *App) applyPackInput(ctx context.Context, chatID int64, field, text string) {
	ui := a.getUI(chatID)
	code, id := ui.planCode, ui.planPack
	ui.adminInput = ""
	ui.planCode = ""
	ui.planPack = ""
	lang := a.lang(chatID)
	backTo := "pln:pke:" + id + ":" + code
	if field == "plan_pk_add" {
		backTo = "pln:pk:" + code
	}
	back := [][]models.InlineKeyboardButton{navBack(lang, backTo)}

	var apply func(p *model.Plan) error
	switch field {
	case "plan_pk_add":
		pk, ok := parsePackLine(text)
		if !ok {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.pack_add_invalid"), back)
			return
		}
		newID, err := newCode(model.PlanPackIDLen)
		if err != nil {
			a.log.Warn("ID пакета не сгенерирован", "err", err)
			a.sendPayKB(ctx, chatID, i18n.T(lang, "err.storage"), back)
			return
		}
		pk.ID = newID
		apply = func(p *model.Plan) error {
			if p.Traffic == nil {
				p.Traffic = &model.PlanTraffic{}
			}
			if p.Pack(newID) != nil {
				return errPlanCodeTaken
			}
			if len(p.Traffic.Packs) >= model.PlanPacksMax {
				return errPacksFull
			}
			p.Traffic.Packs = append(p.Traffic.Packs, pk)
			return nil
		}
	case "plan_pk_gb":
		n, ok := parseGB(text)
		if !ok {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.t_gb_invalid"), back)
			return
		}
		apply = editPack(id, func(pk *model.PlanPack) { pk.GB = n })
	case "plan_pk_price":
		v, err := parsePlanPrice(text)
		if err != nil {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.price_invalid"), back)
			return
		}
		apply = editPack(id, func(pk *model.PlanPack) { pk.Price = v })
	case "plan_pk_stars":
		n, ok := parseStars(text)
		if !ok {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.t_stars_invalid"), back)
			return
		}
		apply = editPack(id, func(pk *model.PlanPack) { pk.Stars = n })
	default:
		a.showPlanCard(ctx, chatID, code)
		return
	}
	_, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		if !p.IsTraffic() {
			return errPlanKind
		}
		return apply(p)
	})
	switch {
	case errors.Is(err, errPackNoPrice):
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.pack_no_price"), back)
	case errors.Is(err, errPacksFull):
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.packs_full", model.PlanPacksMax), back)
	case errors.Is(err, errPlanCodeTaken):
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.code_failed"), back)
	case err != nil:
		a.trafficEditFailed(ctx, chatID, code, err)
	case field == "plan_pk_add":
		a.showPlanPacks(ctx, chatID, code)
	default:
		a.showPlanPack(ctx, chatID, code, id)
	}
}

var (
	errPackNoPrice = errors.New("у пакета не осталось цены")
	errPacksFull   = errors.New("пакетов уже максимум")
)

func editPack(id string, change func(*model.PlanPack)) func(*model.Plan) error {
	return func(p *model.Plan) error {
		pk := p.Pack(id)
		if pk == nil {
			return errPackGone
		}
		next := *pk
		change(&next)
		if !packSells(&next) {
			return errPackNoPrice
		}
		*pk = next
		return nil
	}
}

func (a *App) askPackDelete(ctx context.Context, chatID int64, code, id string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	pk := p.Pack(id)
	if pk == nil {
		a.trafficEditFailed(ctx, chatID, code, errPackGone)
		return
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.pack_del_confirm", pk.GB), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "plans.btn_pack_del_yes"), "pln:pkxy:"+id+":"+code)},
		navBack(lang, "pln:pke:"+id+":"+code),
	})
}

func (a *App) deletePack(ctx context.Context, chatID int64, code, id string) {
	_, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		if p.Pack(id) == nil {
			return errPackGone
		}
		out := p.Traffic.Packs[:0:0]
		for _, pk := range p.Traffic.Packs {
			if pk.ID != id {
				out = append(out, pk)
			}
		}
		p.Traffic.Packs = out
		return nil
	})
	if err != nil && !errors.Is(err, errPackGone) {
		a.trafficEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanPacks(ctx, chatID, code)
}

func addSubResetName(lang, strategy string) string {
	if strategy == "NO_RESET" {
		return i18n.T(lang, "plans.reset_none")
	}
	return resetWhen(lang, strategy)
}

func (a *App) showAddSubReset(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !p.IsTraffic() {
		a.showPlanAddSub(ctx, chatID, code)
		return
	}
	cur := p.AddSubStrategyOrDefault()
	var rows [][]models.InlineKeyboardButton
	for _, s := range []string{"DAY", "WEEK", "MONTH_ROLLING", "MONTH", "NO_RESET"} {
		label := addSubResetName(lang, s)
		if s == cur {
			label = "✅ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "pln:asrs:"+s+":"+code)})
	}
	rows = append(rows, navBack(lang, "pln:as:"+code))
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.as_reset_title", planTitleHTML(lang, p)), rows)
}

func (a *App) setAddSubReset(ctx context.Context, chatID int64, code, strategy string) {
	if !model.ValidStrategy(strategy) {
		a.showAddSubReset(ctx, chatID, code)
		return
	}
	if _, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		if !p.IsTraffic() {
			return errPlanKind
		}
		if p.Traffic == nil {
			p.Traffic = &model.PlanTraffic{}
		}
		p.Traffic.AddSubStrategy = strategy
		return nil
	}); err != nil {
		a.trafficEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanAddSub(ctx, chatID, code)
}

var refToCodes = map[string]string{"r": model.RefToReferrer, "i": model.RefToInvitee, "b": model.RefToBoth}

var refToKeys = map[string]struct{ name, btn, screen string }{
	model.RefToReferrer: {"plans.ref_to_referrer", "plans.btn_ref_to_referrer", "ref.traffic_gb_referrer"},
	model.RefToInvitee:  {"plans.ref_to_invitee", "plans.btn_ref_to_invitee", "ref.traffic_gb_invitee"},
	model.RefToBoth:     {"plans.ref_to_both", "plans.btn_ref_to_both", "ref.traffic_gb_both"},
}

func refToName(lang, to string) string {
	return i18n.T(lang, refToKeys[to].name)
}

func refLine(lang string, t model.PlanTraffic) string {
	if t.RefGB <= 0 {
		return i18n.T(lang, "plans.ref_off")
	}
	return i18n.T(lang, "plans.ref_on", t.RefGB, refToName(lang, t.RefTo))
}

func (a *App) showPlanRef(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p := a.planForEdit(ctx, chatID, code)
	if p == nil {
		return
	}
	if !p.IsTraffic() {
		a.showPlanCard(ctx, chatID, code)
		return
	}
	t := trafficTerms(p)
	text := i18n.T(lang, "plans.ref_title", planTitleHTML(lang, p), refLine(lang, t))
	if !a.referralCfg().Enabled {
		text += i18n.T(lang, "plans.ref_program_off")
	}
	rows := [][]models.InlineKeyboardButton{{btn(i18n.T(lang, "plans.btn_ref_gb"), "pln:tin:r:"+code)}}
	for _, k := range []string{"r", "i", "b"} {
		label := i18n.T(lang, refToKeys[refToCodes[k]].btn)
		if refToCodes[k] == t.RefTo {
			label = "✅ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "pln:rft:"+k+":"+code)})
	}
	rows = append(rows, navBack(lang, "pln:open:"+code))
	a.sendPayKB(ctx, chatID, text, rows)
}

func (a *App) setPlanRefTo(ctx context.Context, chatID int64, code, k string) {
	to, ok := refToCodes[k]
	if !ok {
		a.showPlanRef(ctx, chatID, code)
		return
	}
	if _, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		if !p.IsTraffic() {
			return errPlanKind
		}
		if p.Traffic == nil {
			p.Traffic = &model.PlanTraffic{}
		}
		p.Traffic.RefTo = to
		return nil
	}); err != nil {
		a.trafficEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanRef(ctx, chatID, code)
}
