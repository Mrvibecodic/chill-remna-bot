package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// errPlanNotList — тариф больше не в режиме «по списку»: выдавать допуск
// некуда, экран устарел.
var errPlanNotList = errors.New("тариф не в режиме «по списку»")

// Экран «Доступность» карточки тарифа: режим + список допущенных.
//
// Режим — оформление тарифа, а не коммерция: правится через editPlan и НЕ
// снимает from_config (пересборка «Базового» из конфига режим сохраняет,
// см. basePlanFrom). Список допущенных живёт в отдельной таблице и правится
// немедленно, по одной записи — без черновика, чтобы выдача из карточки
// пользователя не гонялась с этим экраном за строку тарифа.

// planAccessPageSize — записей списка допущенных на страницу.
const planAccessPageSize = 8

// availModes — порядок режимов на экране.
var availModes = []string{
	model.PlanAvailAll, model.PlanAvailNew, model.PlanAvailExisting,
	model.PlanAvailList, model.PlanAvailLink,
}

// availModeName — короткое имя режима.
func availModeName(lang, mode string) string {
	return i18n.T(lang, "plans.av_"+model.NormalizeAvailability(mode))
}

// showPlanAvail — экран режима доступности.
func (a *App) showPlanAvail(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.planEditFailed(ctx, chatID, code, planGoneOr(err))
		return
	}
	mode := model.NormalizeAvailability(p.Availability)

	var b strings.Builder
	b.WriteString(i18n.T(lang, "plans.avail_title", planTitleHTML(lang, p), availModeName(lang, mode)))
	b.WriteString("\n\n")
	b.WriteString(i18n.T(lang, "plans.avd_"+mode))
	switch mode {
	case model.PlanAvailList:
		n := 0
		if list, lerr := a.planAccessList(ctx, code); lerr == nil {
			n = len(list)
		}
		b.WriteString("\n\n")
		b.WriteString(i18n.T(lang, "plans.avail_count", n))
	case model.PlanAvailLink:
		if link := a.planLink(ctx, code); link != "" {
			b.WriteString("\n\n")
			b.WriteString(i18n.T(lang, "plans.avail_link_line", link))
		}
	}

	var rows [][]models.InlineKeyboardButton
	for _, m := range availModes {
		label := availModeName(lang, m)
		if m == mode {
			label = "✅ " + label
		}
		rows = append(rows, []models.InlineKeyboardButton{btn(label, "pln:avm:"+m+":"+code)})
	}
	if mode == model.PlanAvailList {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(i18n.T(lang, "plans.btn_access_list"), "pln:avls:0:"+code),
			btn(i18n.T(lang, "plans.btn_access_add"), "pln:avla:"+code),
		})
	}
	rows = append(rows, navBack(lang, "pln:open:"+code))
	a.sendPayKB(ctx, chatID, b.String(), rows)
}

// planAccessList — список допущенных тарифа из хранилища.
func (a *App) planAccessList(ctx context.Context, code string) ([]model.PlanAccess, error) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil, errStorageUnavailable
	}
	return st.ListPlanAccess(ctx, code)
}

// onPlanAvailMode — выбор режима: pln:avm:<режим>:<код>.
//
// Уход с режима «по списку» ЧИСТИТ список допущенных — иначе у публичного
// тарифа копятся «мусорные» разрешения, молча оживающие при возврате режима.
// Непустой список чистится только через подтверждение (avmc).
func (a *App) onPlanAvailMode(ctx context.Context, chatID int64, arg string, confirmed bool) {
	lang := a.lang(chatID)
	modeArg, code, _ := strings.Cut(arg, ":")
	mode := model.NormalizeAvailability(modeArg)
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.planEditFailed(ctx, chatID, code, planGoneOr(err))
		return
	}
	cur := model.NormalizeAvailability(p.Availability)
	if mode == cur {
		a.showPlanAvail(ctx, chatID, code)
		return
	}
	if cur == model.PlanAvailList && mode != model.PlanAvailList && !confirmed {
		list, lerr := a.planAccessList(ctx, code)
		if lerr != nil {
			a.planEditFailed(ctx, chatID, code, lerr)
			return
		}
		if len(list) > 0 {
			a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.avail_clear_confirm", len(list), availModeName(lang, mode)),
				[][]models.InlineKeyboardButton{
					{btn(i18n.T(lang, "plans.btn_avail_clear_yes"), "pln:avmc:"+mode+":"+code)},
					navBack(lang, "pln:av:"+code),
				})
			return
		}
	}
	// Смена режима и очистка списка — одна критическая секция под plansMu, и
	// выдача допуска (карточка пользователя, многострочный ввод) идёт под тем
	// же замком с проверкой режима: иначе допуск, выданный между сменой и
	// очисткой, либо молча стирался бы, либо переживал уход из режима «по
	// списку» и молча оживал при возврате.
	err = func() error {
		a.plansMu.Lock()
		defer a.plansMu.Unlock()
		p, err := a.planByCode(ctx, code)
		if err != nil {
			return err
		}
		if p == nil {
			return errPlanGone
		}
		leavingList := model.NormalizeAvailability(p.Availability) == model.PlanAvailList && mode != model.PlanAvailList
		p.Availability = mode
		if err := a.savePlan(ctx, p); err != nil {
			return err
		}
		if leavingList {
			a.mu.Lock()
			st := a.store
			a.mu.Unlock()
			if st != nil {
				if cerr := st.ClearPlanAccess(ctx, code); cerr != nil {
					// Режим уже сменён; осиротевший список — не повод врать про
					// сбой смены режима: его добьёт чистка при старте.
					a.log.Warn("список допущенных не очищен", "err", cerr, "plan", code)
				}
			}
		}
		return nil
	}()
	if err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanAvail(ctx, chatID, code)
}

// grantPlanAccessChecked выдаёт допуск под plansMu, убедившись, что тариф
// существует и всё ещё в режиме «по списку» (экран мог устареть, режим —
// смениться между отрисовкой и нажатием).
func (a *App) grantPlanAccessChecked(ctx context.Context, code string, tgID int64, email string) error {
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
	p, err := a.planByCode(ctx, code)
	if err != nil {
		return err
	}
	if p == nil {
		return errPlanGone
	}
	if model.NormalizeAvailability(p.Availability) != model.PlanAvailList {
		return errPlanNotList
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return errStorageUnavailable
	}
	return st.GrantPlanAccess(ctx, code, tgID, email)
}

// showPlanAccessList — постраничный список допущенных: pln:avls:<стр>:<код>.
// Нажатие на запись удаляет её; в callback уходит fnv-отпечаток записи —
// сама запись (почта до 254 знаков) в 64 байта callback'а не помещается.
func (a *App) showPlanAccessList(ctx context.Context, chatID int64, arg string) {
	lang := a.lang(chatID)
	pageStr, code, _ := strings.Cut(arg, ":")
	page, _ := strconv.Atoi(pageStr)
	list, err := a.planAccessList(ctx, code)
	if err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	if len(list) == 0 {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.access_empty"),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "plans.btn_access_add"), "pln:avla:"+code)},
				navBack(lang, "pln:av:"+code),
			})
		return
	}
	pages := (len(list) + planAccessPageSize - 1) / planAccessPageSize
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	from := page * planAccessPageSize
	to := min(from+planAccessPageSize, len(list))

	var rows [][]models.InlineKeyboardButton
	for _, e := range list[from:to] {
		rows = append(rows, []models.InlineKeyboardButton{
			btn("🗑 "+a.planAccessLabel(ctx, &e), "pln:avlx:"+planEditHash(planAccessKeyOf(&e))+":"+strconv.Itoa(page)+":"+code),
		})
	}
	if pages > 1 {
		var nav []models.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, btn(i18n.T(lang, "btn.prev"), "pln:avls:"+strconv.Itoa(page-1)+":"+code))
		}
		if page < pages-1 {
			nav = append(nav, btn(i18n.T(lang, "btn.next"), "pln:avls:"+strconv.Itoa(page+1)+":"+code))
		}
		rows = append(rows, nav)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "plans.btn_access_add"), "pln:avla:"+code)},
		navBack(lang, "pln:av:"+code),
	)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.access_title", len(list), page+1, pages), rows)
}

// planAccessKeyOf — канонический ключ записи для отпечатка в callback.
func planAccessKeyOf(e *model.PlanAccess) string {
	if e.Email != "" {
		return "em:" + e.Email
	}
	return "tg:" + strconv.FormatInt(e.TelegramID, 10)
}

// planAccessLabel — подпись записи: для Telegram — имя, как в карточках
// пользователей; для почты — сама почта. Подпись кнопки идёт как есть, без
// HTML-экранирования.
func (a *App) planAccessLabel(ctx context.Context, e *model.PlanAccess) string {
	if e.Email != "" {
		return "✉️ " + e.Email
	}
	return "👤 " + a.userLabelByID(ctx, e.TelegramID)
}

// onPlanAccessRemove — удаление записи: pln:avlx:<отпечаток>:<стр>:<код>.
// Страница едет в callback'е, чтобы чистка длинного списка не сбрасывала
// админа на первую страницу после каждого нажатия.
func (a *App) onPlanAccessRemove(ctx context.Context, chatID int64, arg string) {
	hash, rest, _ := strings.Cut(arg, ":")
	pageStr, code, _ := strings.Cut(rest, ":")
	list, err := a.planAccessList(ctx, code)
	if err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	for i := range list {
		if planEditHash(planAccessKeyOf(&list[i])) != hash {
			continue
		}
		if st != nil {
			if rerr := st.RevokePlanAccess(ctx, code, list[i].TelegramID, list[i].Email); rerr != nil {
				a.planEditFailed(ctx, chatID, code, rerr)
				return
			}
		}
		break
	}
	// Запись могла исчезнуть раньше (второй админ, другая страница) — список
	// перерисовывается в любом случае; номер страницы клампится по факту.
	a.showPlanAccessList(ctx, chatID, pageStr+":"+code)
}

// askPlanAccess — вопрос «кого добавить»: pln:avla:<код>.
func (a *App) askPlanAccess(ctx context.Context, chatID int64, code string) {
	a.askPlanTextTo(ctx, chatID, code, "plan_access", "plans.ask_access", "pln:av:"+code)
}

// hasListPlans — есть ли тарифы в режиме «по списку» (для кнопки в карточке
// пользователя).
func (a *App) hasListPlans(ctx context.Context) bool {
	plans, err := a.planList(ctx)
	if err != nil {
		return false
	}
	for i := range plans {
		if model.NormalizeAvailability(plans[i].Availability) == model.PlanAvailList {
			return true
		}
	}
	return false
}

// userAccessEmail — почта e-mail-аккаунта кабинета ("" — обычный Telegram).
func (a *App) userAccessEmail(ctx context.Context, uid int64) string {
	if uid >= 0 {
		return ""
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return ""
	}
	wu, err := st.GetWebUserByTgID(ctx, uid)
	if err != nil || wu == nil {
		return ""
	}
	return model.NormalizeEmail(wu.Email)
}

// showUserPlanAccess — экран «допуски к тарифам» карточки пользователя:
// тарифы в режиме «по списку», нажатие выдаёт или отзывает допуск.
func (a *App) showUserPlanAccess(ctx context.Context, chatID, uid int64) {
	lang := a.lang(chatID)
	plans, err := a.planList(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	email := a.userAccessEmail(ctx, uid)
	id := strconv.FormatInt(uid, 10)
	var rows [][]models.InlineKeyboardButton
	for i := range plans {
		p := &plans[i]
		if model.NormalizeAvailability(p.Availability) != model.PlanAvailList {
			continue
		}
		has, herr := st.HasPlanAccess(ctx, p.Code, uid, email)
		if herr != nil {
			a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
			return
		}
		mark := "➖"
		if has {
			mark = "✅"
		}
		rows = append(rows, []models.InlineKeyboardButton{
			btn(mark+" "+planTitle(lang, p), "usr:pg:"+id+":"+p.Code),
		})
	}
	if len(rows) == 0 {
		a.sendUsrKB(ctx, chatID, i18n.T(lang, "user.plans_none"),
			[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.back"), "usr:view:"+id)}})
		return
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.back"), "usr:view:"+id)})
	a.sendUsrKB(ctx, chatID, i18n.T(lang, "user.plans_title", a.userLabelByID(ctx, uid)), rows)
}

// toggleUserPlanAccess выдаёт или отзывает допуск из карточки пользователя.
// Для e-mail-аккаунта кабинета запись пишется по почте — его синтетический
// отрицательный ID в списках не участвует.
func (a *App) toggleUserPlanAccess(ctx context.Context, chatID, uid int64, code string) {
	lang := a.lang(chatID)
	if uid == 0 || !model.ValidPlanCode(code) {
		a.showUserPlanAccess(ctx, chatID, uid)
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	email := a.userAccessEmail(ctx, uid)
	tg := uid
	if uid < 0 {
		if email == "" {
			// Синтетический аккаунт без почты — некуда писать допуск.
			a.sendUsrKB(ctx, chatID, i18n.T(lang, "user.plans_no_email"),
				[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "btn.back"), "usr:view:"+strconv.FormatInt(uid, 10))}})
			return
		}
		tg = 0
	}
	has, err := st.HasPlanAccess(ctx, code, uid, email)
	if err == nil {
		if has {
			err = st.RevokePlanAccess(ctx, code, tg, email)
			if err == nil && uid < 0 {
				// Синтетический ID мог попасть в список и числом (многострочный
				// ввод) — иначе отзыв по почте оставил бы допуск живым.
				err = st.RevokePlanAccess(ctx, code, uid, "")
			}
		} else {
			err = a.grantPlanAccessChecked(ctx, code, tg, email)
		}
	}
	if errors.Is(err, errPlanGone) || errors.Is(err, errPlanNotList) {
		// Тариф исчез или ушёл из режима «по списку», пока экран висел, —
		// перерисовка всё объяснит сама.
		a.showUserPlanAccess(ctx, chatID, uid)
		return
	}
	if err != nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.showUserPlanAccess(ctx, chatID, uid)
}

// applyPlanAccessInput принимает многострочный ввод списка допущенных:
// Telegram ID и адреса почты, разделённые пробелами, запятыми или переводами
// строк. Непонятные куски пропускаются и честно считаются.
func (a *App) applyPlanAccessInput(ctx context.Context, chatID int64, text string) {
	ui := a.getUI(chatID)
	code := ui.planCode
	ui.adminInput = ""
	ui.planCode = ""
	lang := a.lang(chatID)
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil || code == "" {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}

	raw := strings.NewReplacer(",", " ", "\n", " ", ";", " ").Replace(text)
	added, skipped := 0, 0
	for _, f := range strings.Fields(raw) {
		var err error
		switch {
		case strings.Contains(f, "@"):
			email := model.NormalizeEmail(f)
			// Минимальная проверка адреса: одна @ не с краю. Сложнее не нужно —
			// список сверяется с почтой, которую ввёл сам пользователь кабинета.
			at := strings.IndexByte(email, '@')
			if at <= 0 || at == len(email)-1 || strings.Count(email, "@") != 1 {
				skipped++
				continue
			}
			err = a.grantPlanAccessChecked(ctx, code, 0, email)
		default:
			id, perr := strconv.ParseInt(f, 10, 64)
			if perr != nil || id == 0 {
				skipped++
				continue
			}
			err = a.grantPlanAccessChecked(ctx, code, id, "")
		}
		if errors.Is(err, errPlanGone) || errors.Is(err, errPlanNotList) {
			// Тариф исчез или ушёл из режима «по списку», пока админ печатал, —
			// продолжать по одной записи бессмысленно.
			a.planEditFailed(ctx, chatID, code, errPlanGone)
			return
		}
		if err != nil {
			skipped++
			continue
		}
		added++
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.access_added", added, skipped),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "plans.btn_access_list"), "pln:avls:0:"+code)},
			navBack(lang, "pln:av:"+code),
		})
}
