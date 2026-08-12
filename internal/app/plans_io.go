package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Экспорт и импорт тарифа JSON-файлом: бэкап, клонирование между ботами и
// массовое заполнение списка допущенных.
//
// Формат намеренно самоописан (format + version): файл живёт дольше кода, и
// файл из более новой версии бота должен отклоняться внятным сообщением, а не
// молча терять поля.

const (
	planFileFormat  = "remnabot-plan"
	planFileVersion = 1
	// planImportMaxBytes — предел размера файла импорта. Тариф с большим
	// списком допущенных занимает десятки килобайт; мегабайты — это не тариф.
	planImportMaxBytes = 256 << 10
	// planImportMaxAccess — предел записей списка в файле: применение идёт под
	// замком тарифов, и безразмерный список тормозил бы продажи на время
	// импорта.
	planImportMaxAccess = 5000
)

type planFile struct {
	Format  string           `json:"format"`
	Version int              `json:"version"`
	Plan    planFilePlan     `json:"plan"`
	Access  []planFileAccess `json:"access,omitempty"`
}

type planFilePlan struct {
	Code         string               `json:"code"`
	Name         string               `json:"name"`
	Description  string               `json:"description,omitempty"`
	Icon         string               `json:"icon,omitempty"`
	Availability string               `json:"availability,omitempty"`
	AddSub       string               `json:"addsub,omitempty"`
	AddSubName   string               `json:"addsub_name,omitempty"`
	AddSubDesc   string               `json:"addsub_desc,omitempty"`
	TrafficGB    int                  `json:"traffic_gb,omitempty"`
	DeviceLimit  int                  `json:"device_limit,omitempty"`
	Strategy     string               `json:"strategy,omitempty"`
	IntSquads    []string             `json:"int_squads,omitempty"`
	ExtSquad     string               `json:"ext_squad,omitempty"`
	Currency     string               `json:"currency,omitempty"`
	Durations    []model.PlanDuration `json:"durations,omitempty"`
}

type planFileAccess struct {
	TelegramID int64  `json:"telegram_id,omitempty"`
	Email      string `json:"email,omitempty"`
}

// exportPlan отправляет тариф файлом: pln:exp:<код>.
func (a *App) exportPlan(ctx context.Context, chatID int64, code string) {
	lang := a.lang(chatID)
	p, err := a.planByCode(ctx, code)
	if err != nil || p == nil {
		a.planEditFailed(ctx, chatID, code, planGoneOr(err))
		return
	}
	access, aerr := a.planAccessList(ctx, code)
	if aerr != nil {
		a.planEditFailed(ctx, chatID, code, aerr)
		return
	}
	f := planFile{
		Format:  planFileFormat,
		Version: planFileVersion,
		Plan: planFilePlan{
			Code: p.Code, Name: p.Name, Description: p.Description, Icon: p.Icon,
			Availability: p.Availability,
			AddSub:       p.AddSub, AddSubName: p.AddSubName, AddSubDesc: p.AddSubDesc,
			TrafficGB: p.TrafficGB, DeviceLimit: p.DeviceLimit,
			Strategy: p.Strategy, IntSquads: p.IntSquads, ExtSquad: p.ExtSquad,
			Currency: p.Currency, Durations: p.Durations,
		},
	}
	for _, e := range access {
		f.Access = append(f.Access, planFileAccess{TelegramID: e.TelegramID, Email: e.Email})
	}
	data, jerr := json.MarshalIndent(&f, "", "  ")
	if jerr != nil {
		a.planEditFailed(ctx, chatID, code, jerr)
		return
	}
	a.msg.SendDocument(ctx, chatID, "plan_"+p.Code+".json", data, i18n.T(lang, "plans.export_caption", planTitle(lang, p)))
}

// askPlanImport — экран «пришлите файл»: pln:imp.
func (a *App) askPlanImport(ctx context.Context, chatID int64) {
	ui := a.getUI(chatID)
	ui.awaitPlanImport = true
	// Взаимоисключение с ожиданием дампа remnashop: два взведённых ожидания
	// файла — это чей-то файл не в том разборе.
	ui.awaitRSDump = false
	lang := a.lang(chatID)
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.import_ask"),
		[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
}

// errPlanFile — файл не является тарифом этого формата.
var errPlanFile = errors.New("файл не разобран")

// parsePlanFile разбирает и проверяет файл тарифа. Возвращает готовый к записи
// тариф (без Order/Enabled/FromConfig — их решает применение) и список
// допущенных.
func parsePlanFile(data []byte) (*model.Plan, []model.PlanAccess, error) {
	var f planFile
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errPlanFile, err)
	}
	if f.Format != planFileFormat {
		return nil, nil, fmt.Errorf("%w: это не файл тарифа", errPlanFile)
	}
	if f.Version > planFileVersion {
		return nil, nil, fmt.Errorf("%w: файл из более новой версии бота (version=%d)", errPlanFile, f.Version)
	}
	p := &model.Plan{
		Code: strings.TrimSpace(f.Plan.Code), Name: strings.TrimSpace(f.Plan.Name),
		Description: f.Plan.Description, Icon: strings.TrimSpace(f.Plan.Icon),
		Availability: f.Plan.Availability,
		AddSub:       f.Plan.AddSub, AddSubName: strings.TrimSpace(f.Plan.AddSubName), AddSubDesc: f.Plan.AddSubDesc,
		TrafficGB:   f.Plan.TrafficGB,
		DeviceLimit: f.Plan.DeviceLimit, Strategy: f.Plan.Strategy,
		IntSquads: f.Plan.IntSquads, ExtSquad: f.Plan.ExtSquad,
		Currency: f.Plan.Currency, Durations: f.Plan.Durations,
	}
	if !model.ValidPlanCode(p.Code) {
		return nil, nil, fmt.Errorf("%w: недопустимый код тарифа %q", errPlanFile, p.Code)
	}
	if n := len([]rune(p.AddSubName)); n > planNameMaxLen {
		return nil, nil, fmt.Errorf("%w: название опции длиннее %d символов", errPlanFile, planNameMaxLen)
	}
	if n := len([]rune(p.AddSubDesc)); n > planDescMaxLen {
		return nil, nil, fmt.Errorf("%w: описание опции длиннее %d символов", errPlanFile, planDescMaxLen)
	}
	if p.Name == "" {
		return nil, nil, fmt.Errorf("%w: у тарифа нет имени", errPlanFile)
	}
	if n := len([]rune(p.Name)); n > planNameMaxLen {
		return nil, nil, fmt.Errorf("%w: имя длиннее %d символов", errPlanFile, planNameMaxLen)
	}
	if n := len([]rune(p.Description)); n > planDescMaxLen {
		return nil, nil, fmt.Errorf("%w: описание длиннее %d символов", errPlanFile, planDescMaxLen)
	}
	if n := len([]rune(p.Icon)); n > planIconMaxLen {
		return nil, nil, fmt.Errorf("%w: значок длиннее %d символов", errPlanFile, planIconMaxLen)
	}
	seen := map[int]bool{}
	for i := range p.Durations {
		d := &p.Durations[i]
		if d.Months <= 0 && d.Days <= 0 {
			return nil, nil, fmt.Errorf("%w: длительность без срока", errPlanFile)
		}
		if d.Months > 0 {
			if seen[d.Months] {
				return nil, nil, fmt.Errorf("%w: длительность %d мес повторяется", errPlanFile, d.Months)
			}
			seen[d.Months] = true
		}
		if d.Stars < 0 {
			return nil, nil, fmt.Errorf("%w: отрицательная цена в звёздах", errPlanFile)
		}
		// Цены — числа, как и при вводе с экранов (setPlanPrice): импорт не
		// должен быть обходом валидации — тариф с ценой «9 900» или «abc»
		// висел бы в витрине, но не продавался.
		for _, pv := range []*string{&d.Base, &d.P2P, &d.YooKassa} {
			*pv = normPriceStr(strings.TrimSpace(*pv))
			if *pv == "" {
				continue
			}
			if k, ok := rubToKopecks(*pv); !ok || k <= 0 {
				return nil, nil, fmt.Errorf("%w: цена %q не разобрана как число", errPlanFile, *pv)
			}
		}
	}
	p.Normalize()

	if len(f.Access) > planImportMaxAccess {
		return nil, nil, fmt.Errorf("%w: список допущенных длиннее %d записей", errPlanFile, planImportMaxAccess)
	}
	var access []model.PlanAccess
	for _, e := range f.Access {
		email := model.NormalizeEmail(e.Email)
		if (e.TelegramID == 0) == (email == "") {
			// Запись без адресата или с двумя сразу — файл правили руками.
			return nil, nil, fmt.Errorf("%w: запись списка допущенных должна содержать либо telegram_id, либо email", errPlanFile)
		}
		access = append(access, model.PlanAccess{PlanCode: p.Code, TelegramID: e.TelegramID, Email: email})
	}
	return p, access, nil
}

// handlePlanImportDoc принимает файл тарифа, показывает предпросмотр и ждёт
// подтверждения.
func (a *App) handlePlanImportDoc(ctx context.Context, m *models.Message) {
	chatID := m.Chat.ID
	lang := a.lang(chatID)
	if m.Document.FileSize > planImportMaxBytes {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.import_too_big"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}
	data, err := a.msg.Download(ctx, m.Document.FileID)
	if err != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.import_bad", escapeName(err.Error())),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}
	p, access, perr := parsePlanFile(data)
	if perr != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.import_bad", escapeName(perr.Error())),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}
	existing, eerr := a.planByCode(ctx, p.Code)
	if eerr != nil {
		a.sendPayKB(ctx, chatID, i18n.T(lang, "err.storage"),
			[][]models.InlineKeyboardButton{navBack(lang, "pln:list")})
		return
	}
	ui := a.getUI(chatID)
	ui.planImport = p
	ui.planImportAccess = access

	kind := i18n.T(lang, "plans.import_new")
	if existing != nil {
		kind = i18n.T(lang, "plans.import_overwrite", planTitleHTML(lang, existing))
	}
	if p.Code == model.PlanCodeBase {
		// Импорт «Базового» — это ещё и разворот синхронизации: файл станет
		// истиной, зеркало перепишет сетку цен в конфиге.
		kind += "\n\n" + i18n.T(lang, "plans.import_base_warn")
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.import_preview",
		planTitleHTML(lang, p), p.Code, availModeName(lang, p.Availability),
		len(p.Durations), len(access), kind),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "plans.btn_import_yes"), "pln:impok")},
			navBack(lang, "pln:list"),
		})
}

// applyPlanImport — подтверждение импорта: pln:impok.
//
// Новый тариф создаётся ВЫКЛЮЧЕННЫМ и встаёт в конец списка (как createPlan) —
// безопасный дефолт. При перезаписи существующего сохраняются его включённость,
// порядок и время создания: файл описывает условия, а не место в витрине.
// Список допущенных заменяется списком из файла целиком.
func (a *App) applyPlanImport(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	p, access := ui.planImport, ui.planImportAccess
	ui.planImport = nil
	ui.planImportAccess = nil
	if p == nil {
		a.showPlansAdmin(ctx, chatID, a.plansPage(chatID))
		return
	}
	a.mu.Lock()
	st := a.store
	a.mu.Unlock()
	if st == nil {
		a.sendHome(ctx, chatID, i18n.T(lang, "err.storage"))
		return
	}

	err := func() error {
		a.plansMu.Lock()
		defer a.plansMu.Unlock()
		existing, err := a.planByCode(ctx, p.Code)
		if err != nil {
			return err
		}
		if existing != nil {
			p.Enabled = existing.Enabled
			p.Order = existing.Order
			p.CreatedAt = existing.CreatedAt
		} else {
			p.Enabled = false
			order, oerr := a.nextPlanOrder(ctx)
			if oerr != nil {
				return oerr
			}
			p.Order = order
		}
		// Импортированный тариф ведёт себя как правленный редактором. Для
		// «Базового» это означает разворот синхронизации: файл — истина,
		// зеркало перепишет сетку в конфиге (syncPlansConfig ниже).
		p.FromConfig = false
		if err := a.savePlan(ctx, p); err != nil {
			return err
		}
		if err := st.ClearPlanAccess(ctx, p.Code); err != nil {
			return err
		}
		for i := range access {
			if err := st.GrantPlanAccess(ctx, p.Code, access[i].TelegramID, access[i].Email); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		a.planEditFailed(ctx, chatID, p.Code, err)
		return
	}
	if p.Code == model.PlanCodeBase {
		// Сетка в конфиге — зеркало «Базового»: после импорта её надо догнать,
		// иначе продажи до ближайшего сохранения конфига идут по старым ценам.
		if _, serr := a.syncPlansConfig(ctx); serr != nil {
			a.log.Warn("зеркало сетки после импорта не записано", "err", serr)
		}
	}
	a.sendPayKB(ctx, chatID, i18n.T(lang, "plans.import_done", planTitleHTML(lang, p)),
		[][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "plans.btn_open_imported"), "pln:open:"+p.Code)},
			navBack(lang, "pln:list"),
		})
}
