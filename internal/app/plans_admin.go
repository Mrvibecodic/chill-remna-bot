package app

import (
	"context"
	"crypto/rand"
	"errors"
	"html"
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
// ссылку на скрытый тариф, поэтому он должен быть неперебираемым: 12 символов
// алфавита ниже дают около 57 бит — запас на порядки при любом мыслимом числе
// попыток.
const planCodeLen = 12

// planCodeAlphabet — алфавит кода: без гласных и без похожих начертаний, чтобы
// код нельзя было спутать при чтении с экрана и чтобы в нём не сложилось слово.
const planCodeAlphabet = "23456789bcdfghjkmnpqrstvwxz"

// newPlanCode генерирует код тарифа.
//
// Ошибку возвращает, а не глотает: тихий фолбэк на предсказуемый код сделал бы
// перебираемой ссылку на скрытый тариф, а это ровно то, от чего код и защищает.
//
// Остаток от деления брать нельзя: 256 на 27 не делится, и первые тринадцать
// символов алфавита выпадали бы чаще остальных. Лишние значения отбрасываем.
func newPlanCode() (string, error) {
	// Наибольшее кратное длине алфавита; всё, что выше, отбрасывается.
	limit := 256 - 256%len(planCodeAlphabet)
	out := make([]byte, 0, planCodeLen)
	buf := make([]byte, planCodeLen)
	for len(out) < planCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, planCodeAlphabet[int(b)%len(planCodeAlphabet)])
			if len(out) == planCodeLen {
				break
			}
		}
	}
	return string(out), nil
}

// Границы длины полей оформления. Имя уезжает в снимок условий сделки, а он
// лежит в каждой строке платежей, счетов, заявок и автосписаний — длинное имя
// раздувает их все. Описание ограничено тем, что подпись под баннером у
// Telegram кончается на 1024 символах: тариф с описанием на четыре тысячи
// символов сделал бы карточку неотправляемой, то есть неисправимой.
const (
	planNameMaxLen = 64
	planDescMaxLen = 300
	planIconMaxLen = 8
)

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

// savePlan пишет тариф. Вызывать ТОЛЬКО под a.plansMu (см. editPlan): запись
// идёт целой строкой, поэтому «прочитал → изменил → записал» без замка теряет
// чужую правку — например, цену, которую синхронизация только что привезла из
// конфига.
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

// errPlanGone — тарифа нет: его удалили, пока экран висел в переписке.
var errPlanGone = errors.New("тариф не найден")

// editPlan выполняет «прочитать тариф → изменить → записать» под замком
// тарифов. Замок здесь не про две горутины админки (обработчик апдейтов один),
// а про синхронизацию «Базового» от сетки цен: её запускает и фоновая проверка
// обновлений, и заявка обычного покупателя на перевод. Без замка правка,
// начатая до синхронизации, записывала бы поверх неё старые цены.
func (a *App) editPlan(ctx context.Context, code string, apply func(*model.Plan) error) (*model.Plan, error) {
	if code == "" {
		return nil, errPlanGone
	}
	a.plansMu.Lock()
	defer a.plansMu.Unlock()
	p, err := a.planByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errPlanGone
	}
	if err := apply(p); err != nil {
		return nil, err
	}
	if err := a.savePlan(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// planEditFailed сообщает админу, почему правка не применилась. Ошибку
// хранилища в чат не отдаём (в тексте драйвера хост, база и пользователь), но и
// молчать нельзя: молчаливый возврат в карточку читается как «сохранено».
func (a *App) planEditFailed(ctx context.Context, chatID int64, code string, err error) {
	lang := a.lang(chatID)
	if errors.Is(err, errPlanGone) {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.gone"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}
	a.log.Warn("тариф не сохранён", "err", err, "plan", code)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "err.storage"),
		[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
}

func (a *App) showPlansAdmin(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	plans, err := a.planList(ctx)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "err.storage"),
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
	a.getUI(chatID).plansPage = page
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

// planTitle — имя тарифа со значком, как есть. Годится для подписи кнопки:
// подписи Telegram принимает обычным текстом. Для текста сообщения нужен
// planTitleHTML — сообщения уходят с разметкой.
//
// Имя может быть пустым (тариф только что создан) — тогда вместо пустоты
// показываем код: пустая кнопка нажимается, но не читается.
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

// planTitleHTML — то же имя, но экранированное.
//
// Имя и значок вводит человек, а экраны бота уходят с parse_mode=HTML: имя вида
// «Тариф <VIP>» Telegram считает неизвестным тегом и отвечает ошибкой, то есть
// карточка не отправляется вообще. Выбраться из этого нельзя — кнопка «Имя»
// живёт только в этой карточке.
func planTitleHTML(lang string, p *model.Plan) string {
	return html.EscapeString(planTitle(lang, p))
}

func (a *App) showPlanCard(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p, err := a.planByCode(ctx, code)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "err.storage"),
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
	// Экранируется всё, что человек когда-либо вводил руками: не только имя и
	// описание, но и валюта со стратегией внутри строк лимитов и сроков.
	// Незакрытый тег где угодно в тексте — это отказ Telegram отправить
	// сообщение, то есть карточка, из которой уже не выбраться.
	body := i18n.T(lang, "plans.card",
		planTitleHTML(lang, p), p.Code, state, p.Order, html.EscapeString(desc),
		html.EscapeString(a.planLimitsLine(lang, p)), html.EscapeString(planDurationsLine(lang, p)))
	// Продаёт бот пока по старой сетке цен: витрина, счета и финализация читают
	// конфиг. Тариф, заведённый рядом, ни на что не влияет — говорим об этом
	// прямо, иначе выключённый «Базовый» выглядел бы как остановка продаж.
	if p.Code == model.PlanCodeBase {
		body += i18n.T(lang, "plans.note_base")
	} else {
		body += i18n.T(lang, "plans.note_idle")
	}

	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "plans.btn_pricing"), "pln:pr:"+p.Code)},
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
	// Любое нажатие, кроме вопроса о поле, означает, что предыдущий вопрос
	// брошен. Иначе взведённое ожидание ввода живёт вечно: админ нажал «Имя»,
	// ушёл с экрана — и любой текст, присланный боту через час, молча
	// переименовывает тариф.
	switch action {
	case "name", "desc", "icon":
	default:
		a.forgetPlanInput(chatID)
	}
	switch action {
	case "", "list":
		a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
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
			a.planEditFailed(ctx, chatID, arg, planGoneOr(err))
			return
		}
		a.createPlan(ctx, chatID, src)
	case "del":
		p, err := a.planByCode(ctx, arg)
		if err != nil || p == nil {
			a.planEditFailed(ctx, chatID, arg, planGoneOr(err))
			return
		}
		// Про действующих подписчиков говорим главное: удаление тарифа никого не
		// понижает, потому что проданные условия зафиксированы снимком сделки и
		// от справочника не зависят. Числа подписчиков здесь нет намеренно —
		// пока продажи идут по старой сетке, у любого удаляемого тарифа оно
		// ноль, а ноль в предупреждении читался бы как разрешение.
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.del_confirm", planTitleHTML(lang, p)),
			[][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "plans.btn_del_yes"), "pln:delyes:"+p.Code)},
				navBack(lang, "pln:open:"+p.Code),
			})
	case "delyes":
		a.deletePlan(ctx, chatID, arg)
	case "pr":
		a.showPlanPricing(ctx, chatID, arg)
	case "prm":
		moStr, code, _ := strings.Cut(arg, ":")
		mo, _ := strconv.Atoi(moStr)
		a.showPlanMonth(ctx, chatID, code, mo)
	case "in":
		kind, rest, _ := strings.Cut(arg, ":")
		moStr, code, _ := strings.Cut(rest, ":")
		mo, _ := strconv.Atoi(moStr)
		a.askPlanPriceInput(ctx, chatID, kind, mo, code)
	case "cur":
		a.askPlanValue(ctx, chatID, arg, "currency", "plans.ask_currency")
	case "dvl":
		a.askPlanValue(ctx, chatID, arg, "device_limit", "plans.ask_devlimit")
	case "str":
		a.showPlanStrategy(ctx, chatID, arg)
	case "sts":
		strat, code, _ := strings.Cut(arg, ":")
		if err := a.setPlanStrategy(ctx, code, strat); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.showPlanPricing(ctx, chatID, code)
	case "sqm":
		moStr, code, _ := strings.Cut(arg, ":")
		mo, _ := strconv.Atoi(moStr)
		a.showPlanSquadEditor(ctx, chatID, code, mo)
	case "sqc":
		moStr, code, _ := strings.Cut(arg, ":")
		mo, _ := strconv.Atoi(moStr)
		if err := a.clearPlanSquadOverride(ctx, code, mo); err != nil {
			a.planInputFailed(ctx, chatID, err)
			return
		}
		a.showPlanSquadEditor(ctx, chatID, code, mo)
	default:
		// Неизвестное действие домена — это кнопка, добавленная без маршрута.
		// Показать список честнее, чем не ответить ничем.
		a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
	}
}

// planGoneOr отличает «тариф удалили» от «база недоступна»: сообщения разные, и
// путать их нельзя — в первом случае админу нечего ждать, во втором есть.
func planGoneOr(err error) error {
	if err != nil {
		return err
	}
	return errPlanGone
}

// plansPage — страница списка, на которой админ был последний раз. Номер
// страницы не в callback'ах намеренно: он нужен и после правки, и после
// удаления, и на кнопке «назад» из карточки — протаскивать его через каждую
// кнопку значило бы удлинять callback-данные ради того же результата.
func (a *App) plansPage(chatID int64) int {
	return a.getUI(chatID).plansPage
}

// forgetPlanInput снимает ожидание ввода поля тарифа — и оформления, и
// коммерческого. Коммерческие ключи общие со старыми экранами, и снимать их
// при навигации по тарифам обязательно: иначе вопрос «введите цену», брошенный
// в карточке тарифа, дождался бы любого текста и записал его не туда.
func (a *App) forgetPlanInput(chatID int64) {
	ui := a.getUI(chatID)
	switch ui.adminInput {
	case "plan_name", "plan_desc", "plan_icon",
		"baseprice", "price", "ykprice", "starprice",
		"traffic_gb", "device_per", "device_limit", "currency":
		ui.adminInput = ""
		ui.inputBack = ""
		ui.priceMonths = 0
	}
	ui.planCode = ""
}

// askPlanText спрашивает текстовое поле тарифа. Код тарифа запоминается в
// состоянии экрана: в callback-данные ответа его не положить, а держать «тот
// тариф, что открыт» было бы неверно — админ может открыть другой, пока ждём
// ввод.
func (a *App) askPlanText(ctx context.Context, chatID int64, code, input, key string) {
	if code == "" {
		a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
		return
	}
	ui := a.getUI(chatID)
	// Незакрытые ожидания текста с ДРУГИХ экранов перехватывают сообщение раньше
	// (handleMessage смотрит их до adminInput), и ответ на этот вопрос уходил бы
	// в приветственный баннер, в эмодзи или в причину отказа по заявке. Админ,
	// нажавший «Имя», спрашивается именно об имени — прежние вопросы сняты.
	ui.welcomeAwait = ""
	ui.awaitEmojiFor = ""
	ui.awaitTopUp = false
	ui.rejectReq = 0
	ui.adminInput = input
	ui.planCode = code
	a.askInput(ctx, chatID, i18n.T(a.lang(chatID), key), "pln:open:"+code)
}

// planFieldLimit — граница длины поля и ключ сообщения о её нарушении.
func planFieldLimit(field string) int {
	switch field {
	case "plan_name":
		return planNameMaxLen
	case "plan_desc":
		return planDescMaxLen
	default:
		return planIconMaxLen
	}
}

// applyPlanText принимает введённое значение поля тарифа.
//
// Слишком длинное значение НЕ обрезается: обрезка молча меняет то, что человек
// ввёл, а имя тарифа он увидит только в списке, где оно и так укорочено. Лучше
// сказать про границу и оставить прежнее значение.
func (a *App) applyPlanText(ctx context.Context, chatID int64, field, text string) {
	ui := a.getUI(chatID)
	code := ui.planCode
	ui.adminInput = ""
	ui.planCode = ""
	lang := a.lang(chatID)

	v := strings.TrimSpace(text)
	// Имя и значок идут в подписи кнопок, где перевод строки ломает вид, поэтому
	// от них берём одну строку (firstLine сам обрезает пробелы). Описание
	// многострочным быть может.
	if field != "plan_desc" {
		v = firstLine(v)
	}
	if limit := planFieldLimit(field); len([]rune(v)) > limit {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.too_long", limit),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:open:"+code)})
		return
	}
	// Имя стереть нельзя ни пустой строкой, ни прочерком: безымянный тариф в
	// витрине читается как сбой бота. Молча возвращать карточку тоже нельзя —
	// это выглядело бы как «сохранено».
	if field == "plan_name" && (v == "" || v == "-") {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.name_required"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:open:"+code)})
		return
	}

	_, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		// Прочерк — общий для этой админки способ «стереть значение», и стирает
		// ровно он: пустой ввод (в том числе из одних пробелов) значения не
		// трогает, иначе случайно отправленный пробел стирал бы описание.
		switch field {
		case "plan_name":
			p.Name = v
		case "plan_desc":
			switch v {
			case "-":
				p.Description = ""
			case "":
			default:
				p.Description = v
			}
		case "plan_icon":
			switch v {
			case "-":
				p.Icon = ""
			case "":
			default:
				p.Icon = v
			}
		}
		return nil
	})
	if err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanCard(ctx, chatID, code)
}

func (a *App) togglePlan(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	// Тариф без длительностей включать нельзя: в витрине он либо пустой, либо
	// продаётся по пустой цене. Ровно поэтому новый тариф и создаётся
	// выключенным — здесь тот же запрет с другой стороны.
	_, err := a.editPlan(ctx, code, func(p *model.Plan) error {
		if !p.Enabled && len(p.Durations) == 0 {
			// Отказ возвращается ошибкой, а не флагом: тогда строка тарифа не
			// перезаписывается вообще — незачем трогать базу, чтобы ничего не
			// изменить.
			return errPlanNoDurations
		}
		p.Enabled = !p.Enabled
		return nil
	})
	if errors.Is(err, errPlanNoDurations) {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.enable_empty"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:open:"+code)})
		return
	}
	if err != nil {
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	a.showPlanCard(ctx, chatID, code)
}

// movePlan переставляет тариф в порядке витрины.
//
// Номера переписываются подряд по всему списку, а не переставляются у двух
// соседей. Причина в том, что номера не обязаны быть различными: миграция и
// импорт оставляют нули, а при равных номерах порядок доопределяется кодом
// тарифа — и «поменять местами номера» с таким списком либо не делает ничего,
// либо перебрасывает тариф через несколько позиций сразу. Подряд идущие номера
// заодно чинят список, в котором номера уже разъехались.
func (a *App) movePlan(ctx context.Context, chatID int64, code string, delta int) {
	a.plansMu.Lock()
	plans, err := a.planList(ctx)
	if err != nil {
		a.plansMu.Unlock()
		a.planEditFailed(ctx, chatID, code, err)
		return
	}
	idx := -1
	for i := range plans {
		if plans[i].Code == code {
			idx = i
			break
		}
	}
	other := idx + delta
	if idx < 0 || other < 0 || other >= len(plans) {
		a.plansMu.Unlock()
		if idx < 0 {
			a.planEditFailed(ctx, chatID, code, errPlanGone)
			return
		}
		// Крайний тариф: в базу не пишем ничего, но и молчать нельзя — экран
		// остался бы прежним, и кнопка выглядела бы сломанной.
		lang := a.lang(chatID)
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.move_edge"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:open:"+code)})
		return
	}
	plans[idx], plans[other] = plans[other], plans[idx]

	var failed error
	for i := range plans {
		if plans[i].Order == i {
			continue
		}
		plans[i].Order = i
		if err := a.savePlan(ctx, &plans[i]); err != nil {
			failed = err
			break
		}
	}
	a.plansMu.Unlock()
	if failed != nil {
		a.planEditFailed(ctx, chatID, code, failed)
		return
	}
	a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
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
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.code_failed"))
		return
	}
	p := &model.Plan{Code: code, Availability: model.PlanAvailAll}
	if src != nil {
		cp := *src
		p = &cp
		p.Code = code
		p.CreatedAt = ""
		p.UpdatedAt = ""
		// Имя копии складывается из имени источника, и подрезать надо именно
		// имя источника, а не готовую строку: иначе у длинного имени пометка
		// «копия» отрезалась бы, и копия становилась неотличима от источника в
		// списке — различить их можно было бы только по коду в карточке.
		room := planNameMaxLen - len([]rune(i18n.T(lang, "plans.copy_name", "")))
		if room < 1 {
			room = 1
		}
		p.Name = clampRunes(i18n.T(lang, "plans.copy_name", clampRunes(strings.TrimSpace(src.Name), room)), planNameMaxLen)
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

	// Проверка занятости кода, номер в конце списка и запись — под общим замком
	// тарифов: между ними нельзя пускать ни синхронизацию «Базового», ни
	// соседнее создание.
	err = func() error {
		a.plansMu.Lock()
		defer a.plansMu.Unlock()
		// Совпадение кода на таком алфавите практически невозможно, но занятый
		// код перезаписал бы чужой тариф — поэтому проверяем.
		exists, err := a.planByCode(ctx, code)
		if err != nil {
			return err
		}
		if exists != nil {
			return errPlanCodeTaken
		}
		// В конец списка: новый тариф не должен вытеснять действующий с первой
		// строки витрины. Не прочитали список — не создаём: тариф, встающий
		// первым, увели бы покупателей с действующего.
		order, err := a.nextPlanOrder(ctx)
		if err != nil {
			return err
		}
		p.Order = order
		return a.savePlan(ctx, p)
	}()
	switch {
	case errors.Is(err, errPlanCodeTaken):
		a.sendHome(ctx, chatID, i18n.T(lang, "plans.code_failed"))
		return
	case err != nil:
		a.log.Warn("тариф не создан", "err", err)
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.showPlanCard(ctx, chatID, code)
}

// errPlanCodeTaken — сгенерированный код уже занят.
var errPlanCodeTaken = errors.New("код тарифа занят")

// errPlanNoDurations — у тарифа не заданы сроки, включать нечего.
var errPlanNoDurations = errors.New("у тарифа нет длительностей")

// clampRunes подрезает строку по числу символов, а не байтов: в имени тарифа
// кириллица и эмодзи — норма.
func clampRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return strings.TrimSpace(string(r[:limit]))
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
func (a *App) nextPlanOrder(ctx context.Context) (int, error) {
	plans, err := a.planList(ctx)
	if err != nil {
		return 0, err
	}
	next := 0
	for i := range plans {
		if plans[i].Order >= next {
			next = plans[i].Order + 1
		}
	}
	return next, nil
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
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	if err := st.DeletePlan(ctx, code); err != nil {
		a.log.Warn("тариф не удалён", "err", err, "plan", code)
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}
	a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
}
