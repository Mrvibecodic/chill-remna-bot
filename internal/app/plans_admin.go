package app

import (
	"context"
	"crypto/rand"
	"errors"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Админка тарифов: список, карточка, оформление (имя, описание, значок,
// порядок, включённость), дублирование, создание и удаление.
//
// ПОЧЕМУ ЗДЕСЬ НЕТ ЦЕН. «Базовый» пока ведомый от сетки цен в конфиге, и
// признак plans.from_config в этих экранах не снимается намеренно: цены всё ещё
// правит старый экран цен, а со снятым признаком его правка перестала бы
// доезжать до тарифа — админ поменял бы цену, а тариф остался бы с прежней,
// молча. Разворот синхронизации имеет смысл только вместе с переездом правки
// цен внутрь карточки: тогда у сетки в конфиге остаётся ровно один автор.
//
// Оформление правится уже сейчас: синхронизация его не перезаписывает
// (basePlanFrom сохраняет имя, описание, значок, порядок, режим доступности и
// включённость существующего тарифа), поэтому откат на предыдущий образ и его
// пересборка «Базового» из конфига эти поля не теряют.

// errStorageUnavailable — база недоступна. Отдельная ошибка, а не пустой
// результат: пустой список тарифов предложил бы админу создать заново то, что
// уже существует.
var errStorageUnavailable = errors.New("хранилище недоступно")

// plansPageSize — тарифов на страницу списка. Тариф — сущность, которую админ
// создаёт руками, десятками они не заводятся; страница нужна только чтобы
// клавиатура не выросла за пределы допустимого.
const plansPageSize = 8

// planCodeLen — длина кода, который бот генерирует новому тарифу. Код уходит в
// ссылку на скрытый тариф, поэтому он должен быть не перебираемым; 12 символов
// из 32-символьного алфавита дают запас на порядки при любом мыслимом числе
// попыток.
const planCodeLen = 12

// planCodeAlphabet — алфавит кода: без гласных и похожих символов, чтобы код
// нельзя было спутать при чтении с экрана и чтобы в нём не сложилось слово.
const planCodeAlphabet = "23456789bcdfghjkmnpqrstvwxz"

// newPlanCode генерирует код тарифа. Ошибку не возвращает: crypto/rand на
// поддерживаемых системах не отказывает, а тихий фолбэк на предсказуемый код
// был бы хуже любой ошибки — он бы сделал перебираемой ссылку на скрытый тариф.
func newPlanCode() (string, error) {
	buf := make([]byte, planCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, planCodeLen)
	for i, b := range buf {
		out[i] = planCodeAlphabet[int(b)%len(planCodeAlphabet)]
	}
	return string(out), nil
}

// planList читает тарифы для экрана. Ошибка отдаётся вызывающему: показать
// пустой список вместо недоступного хранилища значило бы предложить админу
// создать тариф заново поверх существующих.
func (a *App) planList(ctx context.Context) ([]model.Plan, error) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil, errStorageUnavailable
	}
	return st.ListPlans(ctx)
}

func (a *App) planByCode(ctx context.Context, code string) (*model.Plan, error) {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return nil, errStorageUnavailable
	}
	return st.GetPlan(ctx, code)
}

func (a *App) savePlan(ctx context.Context, p *model.Plan) error {
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		return errStorageUnavailable
	}
	if err := st.SavePlan(ctx, p); err != nil {
		return err
	}
	// «Базовый» держится в памяти процесса: снимок условий сделки снимается под
	// замком конфига, и ходить оттуда в базу нельзя. Без этого переименованный
	// тариф попадал бы в снимок под старым именем до перезапуска.
	if p.Code == model.PlanCodeBase {
		a.rememberBasePlan(p)
	}
	return nil
}

func (a *App) showPlansAdmin(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	plans, err := a.planList(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"),
			[][]models.InlineKeyboardButton{navBack(lang, "menu:pay")})
		return
	}
	if page < 0 {
		page = 0
	}
	pages := (len(plans) + plansPageSize - 1) / plansPageSize
	if pages == 0 {
		pages = 1
	}
	if page >= pages {
		page = pages - 1
	}
	from := page * plansPageSize
	to := min(from+plansPageSize, len(plans))

	var rows [][]models.InlineKeyboardButton
	for _, p := range plans[from:to] {
		rows = append(rows, []models.InlineKeyboardButton{
			btn(planListLabel(lang, &p), "pln:open:"+p.Code),
		})
	}
	if nav := paginationRow("pln:page:", page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "plans.btn_new"), "pln:new")},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "plans.btn_prices"), "menu:pricing")},
		navBack(lang, "menu:pay"),
	)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.title", len(plans), page+1, pages), rows)
}

// planListLabel — подпись тарифа в списке: состояние, значок, имя и сколько
// длительностей продаётся.
func planListLabel(lang string, p *model.Plan) string {
	mark := "❌"
	if p.Enabled {
		mark = "✅"
	}
	name := planTitle(lang, p)
	return mark + " " + name + " · " + i18n.T(lang, "plans.durations_short", len(p.Durations))
}

// planTitle — имя тарифа со значком. Имя может быть пустым (тариф только что
// создан) — тогда вместо пустоты показываем код: пустая кнопка нажимается, но
// не читается.
func planTitle(lang string, p *model.Plan) string {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = i18n.T(lang, "plans.unnamed", p.Code)
	}
	if icon := strings.TrimSpace(p.Icon); icon != "" {
		return icon + " " + name
	}
	return name
}

func (a *App) showPlanCard(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.sendPayKB(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}
	if p == nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.gone"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}

	state := i18n.T(lang, "plans.off")
	toggleLabel := i18n.T(lang, "plans.btn_enable")
	if p.Enabled {
		state = i18n.T(lang, "plans.on")
		toggleLabel = i18n.T(lang, "plans.btn_disable")
	}
	desc := strings.TrimSpace(p.Description)
	if desc == "" {
		desc = i18n.T(lang, "admin.none")
	}
	body := i18n.T(lang, "plans.card",
		planTitle(lang, p), p.Code, state, p.Order, desc,
		a.planLimitsLine(lang, p), planDurationsLine(lang, p))
	// Продаёт бот пока по старой сетке цен: витрина, счета и финализация читают
	// конфиг. Тариф, заведённый рядом, ни на что не влияет — говорим об этом
	// прямо, иначе выключённый «Базовый» выглядел бы как остановка продаж.
	if p.Code == model.PlanCodeBase {
		body += i18n.T(lang, "plans.note_base")
	} else {
		body += i18n.T(lang, "plans.note_idle")
	}

	rows := [][]models.InlineKeyboardButton{
		{btn(toggleLabel, "pln:toggle:"+p.Code)},
		{btn(i18n.T(lang, "plans.btn_name"), "pln:name:"+p.Code), btn(i18n.T(lang, "plans.btn_desc"), "pln:desc:"+p.Code)},
		{btn(i18n.T(lang, "plans.btn_icon"), "pln:icon:"+p.Code), btn(i18n.T(lang, "plans.btn_dup"), "pln:dup:"+p.Code)},
		{btn(i18n.T(lang, "plans.btn_up"), "pln:up:"+p.Code), btn(i18n.T(lang, "plans.btn_down"), "pln:down:"+p.Code)},
	}
	// «Базовый» удалить нельзя: он мост к старой сетке цен, по которой продаёт
	// предыдущий образ бота после откатa, и единственный тариф, который бот
	// сейчас действительно продаёт.
	if p.Code != model.PlanCodeBase {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "plans.btn_del"), "pln:del:"+p.Code)})
	}
	rows = append(rows, navBack(lang, "pln:list"))
	a.sendPayKB(ctx, chatID, body, rows)
}

// planLimitsLine — лимиты тарифа одной строкой.
func (a *App) planLimitsLine(lang string, p *model.Plan) string {
	traffic := i18n.T(lang, "trial.unlimited")
	if p.TrafficGB > 0 {
		traffic = strconv.Itoa(p.TrafficGB) + " GB"
	}
	devices := i18n.T(lang, "pricing.hwid_default")
	if p.DeviceLimit > 0 {
		devices = strconv.Itoa(p.DeviceLimit)
	}
	squads := strconv.Itoa(len(p.IntSquads))
	if p.ExtSquad != "" {
		squads += " + 1"
	}
	return i18n.T(lang, "plans.limits", traffic, devices, p.Strategy, squads)
}

// planDurationsLine — что и по какой цене продаёт тариф.
func planDurationsLine(lang string, p *model.Plan) string {
	if len(p.Durations) == 0 {
		return i18n.T(lang, "plans.no_durations")
	}
	var parts []string
	for i := range p.Durations {
		d := &p.Durations[i]
		term := strconv.Itoa(d.Months) + i18n.T(lang, "plans.mo")
		if d.Months == 0 && d.Days > 0 {
			term = strconv.Itoa(d.Days) + i18n.T(lang, "plans.d")
		}
		price := strings.TrimSpace(d.Base)
		if price == "" {
			price = "—"
		} else {
			price += curSuffix(p.Currency)
		}
		parts = append(parts, term+" — "+price)
	}
	return strings.Join(parts, " · ")
}

func (a *App) onPlansAdmin(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	switch action {
	case "", "list":
		a.showPlansAdmin(ctx, chatID, 0)
	case "page":
		page, _ := strconv.Atoi(arg)
		a.showPlansAdmin(ctx, chatID, page)
	case "open":
		a.showPlanCard(ctx, chatID, arg)
	case "toggle":
		a.togglePlan(ctx, chatID, arg)
	case "up":
		a.movePlan(ctx, chatID, arg, -1)
	case "down":
		a.movePlan(ctx, chatID, arg, 1)
	case "name":
		a.askPlanText(ctx, chatID, arg, "plan_name", "plans.ask_name")
	case "desc":
		a.askPlanText(ctx, chatID, arg, "plan_desc", "plans.ask_desc")
	case "icon":
		a.askPlanText(ctx, chatID, arg, "plan_icon", "plans.ask_icon")
	case "new":
		a.createPlan(ctx, chatID, nil)
	case "dup":
		src, err := a.planByCode(ctx, arg)
		if err != nil || src == nil {
			a.showPlansAdmin(ctx, chatID, 0)
			return
		}
		a.createPlan(ctx, chatID, src)
	case "del":
		p, err := a.planByCode(ctx, arg)
		if err != nil || p == nil {
			a.showPlansAdmin(ctx, chatID, 0)
			return
		}
		// Про действующих подписчиков говорим главное: удаление тарифа никого не
		// понижает, потому что проданные условия зафиксированы снимком сделки и
		// от справочника не зависят. Числа подписчиков здесь нет намеренно —
		// пока продажи идут по старой сетке, у любого удаляемого тарифа оно
		// ноль, а ноль в предупреждении читался бы как разрешение.
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.del_confirm", planTitle(lang, p)),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "plans.btn_del_yes"), "pln:delyes:"+p.Code)},
				navBack(lang, "pln:open:"+p.Code),
			})
	case "delyes":
		a.deletePlan(ctx, chatID, arg)
	}
}

// askPlanText спрашивает текстовое поле тарифа. Код тарифа запоминается в
// состоянии экрана: в callback-данные ответа его не положить, а держать «тот
// тариф, что открыт» было бы неверно — админ может открыть другой, пока ждём
// ввод.
func (a *App) askPlanText(ctx context.Context, chatID int64, code, input, key string) {
	if code == "" {
		a.showPlansAdmin(ctx, chatID, 0)
		return
	}
	ui := a.getUI(chatID)
	ui.adminInput = input
	ui.planCode = code
	a.askInput(ctx, chatID, i18n.T(a.lang(chatID), key), "pln:open:"+code)
}

// applyPlanText принимает введённое значение поля тарифа.
func (a *App) applyPlanText(ctx context.Context, chatID int64, field, text string) {
	ui := a.getUI(chatID)
	code := ui.planCode
	ui.adminInput = ""
	ui.planCode = ""
	lang := a.lang(chatID)
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.gone"))
		return
	}
	v := strings.TrimSpace(text)
	// Прочерк — общий для админки способ «стереть значение»; у имени его нет:
	// безымянный тариф в витрине выглядел бы как сбой.
	switch field {
	case "plan_name":
		if v == "" {
			a.showPlanCard(ctx, chatID, code)
			return
		}
		p.Name = v
	case "plan_desc":
		if v == "-" {
			v = ""
		}
		p.Description = v
	case "plan_icon":
		if v == "-" {
			v = ""
		}
		p.Icon = v
	}
	if err := a.savePlan(ctx, p); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"))
		return
	}
	a.showPlanCard(ctx, chatID, code)
}

func (a *App) togglePlan(ctx context.Context, chatID int64, code string) {
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.showPlansAdmin(ctx, chatID, 0)
		return
	}
	p.Enabled = !p.Enabled
	if err := a.savePlan(ctx, p); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(a.lang(chatID), "err.storage"))
		return
	}
	a.showPlanCard(ctx, chatID, code)
}

// movePlan переставляет тариф в порядке витрины. Порядок хранится числом, и
// меняются местами именно два соседа: так одно нажатие не переписывает весь
// список, а одинаковые номера (их мог оставить импорт) не превращают
// перестановку в бесконечное «ничего не происходит» — при равенстве соседу
// номера разводятся принудительно.
func (a *App) movePlan(ctx context.Context, chatID int64, code string, delta int) {
	plans, err := a.planList(ctx)
	if err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(a.lang(chatID), "err.storage"))
		return
	}
	idx := -1
	for i := range plans {
		if plans[i].Code == code {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.showPlansAdmin(ctx, chatID, 0)
		return
	}
	other := idx + delta
	if other < 0 || other >= len(plans) {
		a.showPlanCard(ctx, chatID, code)
		return
	}
	cur, nb := &plans[idx], &plans[other]
	curOrder, nbOrder := cur.Order, nb.Order
	if curOrder == nbOrder {
		// Список отсортирован по (order, code), поэтому соседа с тем же номером
		// достаточно развести на единицу в нужную сторону.
		nbOrder = curOrder + delta
	}
	cur.Order, nb.Order = nbOrder, curOrder
	if err := a.savePlan(ctx, cur); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(a.lang(chatID), "err.storage"))
		return
	}
	if err := a.savePlan(ctx, nb); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(a.lang(chatID), "err.storage"))
		return
	}
	a.showPlansAdmin(ctx, chatID, 0)
}

// createPlan заводит новый тариф. src != nil — это дублирование: копия
// повторяет условия источника, но получает свой код и своё имя.
//
// Новый тариф всегда выключён: включённый тариф с незаполненными ценами — это
// либо пустая витрина, либо продажа по пустой цене.
func (a *App) createPlan(ctx context.Context, chatID int64, src *model.Plan) {
	lang := a.lang(chatID)
	code, err := newPlanCode()
	if err != nil {
		a.log.Warn("код тарифа не сгенерирован", "err", err)
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "plans.code_failed"))
		return
	}
	// Совпадение кода на таком алфавите практически невозможно, но занятый код
	// перезаписал бы чужой тариф — поэтому проверяем.
	if exists, err := a.planByCode(ctx, code); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"))
		return
	} else if exists != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "plans.code_failed"))
		return
	}

	p := &model.Plan{Code: code, Availability: model.PlanAvailAll}
	if src != nil {
		cp := *src
		p = &cp
		p.Code = code
		p.CreatedAt = ""
		p.UpdatedAt = ""
		p.Name = i18n.T(lang, "plans.copy_name", strings.TrimSpace(src.Name))
		p.IntSquads = append([]string(nil), src.IntSquads...)
		p.Durations = clonePlanDurations(src.Durations)
	} else {
		p.Name = i18n.T(lang, "plans.new_name")
		p.Currency = a.pricing().Currency
	}
	p.Enabled = false
	// Копия «Базового» ведомой не наследуется: пересобирается из конфига ровно
	// один тариф, и это тариф с кодом base.
	p.FromConfig = false
	// В конец списка: новый тариф не должен вытеснять действующий с первой
	// строки витрины.
	p.Order = a.nextPlanOrder(ctx)
	if err := a.savePlan(ctx, p); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"))
		return
	}
	a.showPlanCard(ctx, chatID, code)
}

// clonePlanDurations копирует длительности вместе с тем, на что смотрят
// указатели переопределений: иначе правка копии меняла бы лимиты источника.
func clonePlanDurations(src []model.PlanDuration) []model.PlanDuration {
	if len(src) == 0 {
		return nil
	}
	out := make([]model.PlanDuration, len(src))
	for i, d := range src {
		out[i] = d
		if d.TrafficGB != nil {
			v := *d.TrafficGB
			out[i].TrafficGB = &v
		}
		if d.DeviceLimit != nil {
			v := *d.DeviceLimit
			out[i].DeviceLimit = &v
		}
		if d.IntSquads != nil {
			v := append([]string(nil), *d.IntSquads...)
			out[i].IntSquads = &v
		}
		if d.ExtSquad != nil {
			v := *d.ExtSquad
			out[i].ExtSquad = &v
		}
	}
	return out
}

// nextPlanOrder — номер в конце списка.
func (a *App) nextPlanOrder(ctx context.Context) int {
	plans, err := a.planList(ctx)
	if err != nil {
		return 0
	}
	next := 0
	for i := range plans {
		if plans[i].Order >= next {
			next = plans[i].Order + 1
		}
	}
	return next
}

func (a *App) deletePlan(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	// «Базовый» не удаляется ни кнопкой, ни прямым callback'ом из старого
	// сообщения: без него бот теряет мост к сетке цен в конфиге.
	if code == model.PlanCodeBase {
		a.showPlanCard(ctx, chatID, code)
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"))
		return
	}
	if err := st.DeletePlan(ctx, code); err != nil {
		a.sendHome(ctx, chatID, "❌ "+i18n.T(lang, "err.storage"))
		return
	}
	a.showPlansAdmin(ctx, chatID, 0)
}
