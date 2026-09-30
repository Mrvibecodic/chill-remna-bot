package app

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
)

// Раздел «Интерфейс → Тексты бота»: свои тексты вместо стандартных.

const (
	cbTexts      = "tx"
	txtPageSize  = 8
	txtLabelMax  = 34
	txtButtonMax = i18n.ButtonMax
	// txtMessageMax — предел видимой длины сообщения с примерами значений.
	// У Telegram 4096, но экран часто собран из нескольких текстов сразу, а
	// значения бывают длиннее примеров.
	txtMessageMax = 1500
)

func (a *App) onTexts(ctx context.Context, chatID int64, val string) {
	action, arg, _ := strings.Cut(val, ":")
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	// Уход с экрана правки внутри редактора (другой текст, список, поиск)
	// снимает ожидание: иначе присланный потом текст сохранился бы не в тот
	// ключ, на который админ смотрит.
	switch action {
	case "ok", "x", "noop", "t", "d":
	default:
		if ui.txtKey != "" {
			a.clearTextInput(ctx, chatID)
		}
	}
	switch action {
	case "home":
		a.showTextsHome(ctx, chatID)
	case "s":
		sec, p, _ := strings.Cut(arg, ":")
		page, _ := strconv.Atoi(p)
		a.showTextsSection(ctx, chatID, sec, page)
	case "q":
		a.clearTextInput(ctx, chatID)
		resetPendingInputs(ui)
		ui.adminInput = "tx_search"
		a.askInput(ctx, chatID, i18n.T(lang, "tx.search_ask"), "tx:home")
	case "f":
		page, _ := strconv.Atoi(arg)
		a.showTextsSearch(ctx, chatID, ui.txtQuery, page)
	case "ch":
		page, _ := strconv.Atoi(arg)
		a.showTextsChanged(ctx, chatID, page)
	case "att":
		page, _ := strconv.Atoi(arg)
		a.showTextsAttention(ctx, chatID, page)
	case "k":
		a.showTextCard(ctx, chatID, arg, "")
	case "e":
		a.startTextEdit(ctx, chatID, arg)
	case "ok":
		a.saveTextDraft(ctx, chatID)
	case "x":
		key := ui.txtKey
		a.clearTextInput(ctx, chatID)
		if key == "" {
			a.showTextsHome(ctx, chatID)
			return
		}
		a.showTextCard(ctx, chatID, key, "")
	case "t":
		a.sendTextTest(ctx, chatID, arg)
	case "d":
		a.sendTextDefault(ctx, chatID, arg)
	case "r":
		e, ok := i18n.EditableByKey(arg)
		if !ok {
			a.showTextsHome(ctx, chatID)
			return
		}
		ask, yes := "tx.reset_ask", "tx.btn_yes_reset"
		if e.Optional {
			ask, yes = "tx.clear_ask", "tx.btn_yes_clear"
		}
		a.sendIfaceKB(ctx, chatID, i18n.T(lang, ask), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, yes), "tx:ry:"+arg), btn(i18n.T(lang, "btn.cancel"), "tx:k:"+arg)},
		})
	case "ry":
		e, ok := i18n.EditableByKey(arg)
		if !ok {
			a.showTextsHome(ctx, chatID)
			return
		}
		note := i18n.T(lang, "tx.reset_done")
		if e.Optional {
			note = i18n.T(lang, "tx.cleared")
		}
		if err := a.setTextOverride(ctx, a.botLang(), arg, ""); err != nil {
			note = i18n.T(lang, "tx.save_failed")
		}
		a.showTextCard(ctx, chatID, arg, note)
	case "kp":
		note := i18n.T(lang, "tx.kept")
		if err := a.keepTextOverride(ctx, a.botLang(), arg); err != nil {
			note = i18n.T(lang, "tx.save_failed")
		}
		a.showTextCard(ctx, chatID, arg, note)
	case "ra":
		n := len(a.textOverrides(a.botLang()))
		a.sendIfaceKB(ctx, chatID, i18n.T(lang, "tx.reset_all_ask", n), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "tx.btn_yes_reset"), "tx:ray"), btn(i18n.T(lang, "btn.cancel"), "tx:home")},
		})
	case "ray":
		note := i18n.T(lang, "tx.reset_all_done")
		if err := a.resetAllTexts(ctx, a.botLang()); err != nil {
			note = i18n.T(lang, "tx.save_failed")
		}
		a.showTextsHomeNote(ctx, chatID, note)
	case "pm":
		a.showPayMethodTexts(ctx, chatID, arg)
	case "noop":
	default:
		a.showTextsHome(ctx, chatID)
	}
}

func txtSectionTitle(lang, sec string) string { return i18n.T(lang, "tx.sec."+sec) }

func (a *App) showTextsHome(ctx context.Context, chatID int64) {
	a.showTextsHomeNote(ctx, chatID, "")
}

func (a *App) showTextsHomeNote(ctx context.Context, chatID int64, note string) {
	lang := a.lang(chatID)
	bl := a.botLang()
	own := a.textOverrides(bl)
	perSec := map[string]int{}
	for key := range own {
		if e, ok := i18n.EditableByKey(key); ok {
			perSec[e.Section]++
		}
	}
	rows := [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "tx.btn_search"), "tx:q")},
		{btn(i18n.T(lang, "tx.btn_methods"), "tx:pm:t")},
	}
	var row []models.InlineKeyboardButton
	for _, sec := range i18n.EditSections() {
		label := txtSectionTitle(lang, sec)
		if n := perSec[sec]; n > 0 {
			label += " · ✏️" + strconv.Itoa(n)
		}
		row = append(row, btn(label, "tx:s:"+sec+":0"))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if len(own) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_changed", len(own)), "tx:ch:0")})
	}
	if stale, broken := a.textsAttention(bl); len(stale)+len(broken) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_attention", len(stale)+len(broken)), "tx:att:0")})
	}
	if len(own) > 0 {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_reset_all"), "tx:ra")})
	}
	rows = append(rows, navBack(lang, "menu:iface"))
	text := i18n.T(lang, "tx.title", len(own))
	if note != "" {
		text = note + "\n\n" + text
	}
	a.sendIfaceKB(ctx, chatID, text, rows)
}

// txtLabel — подпись текста в списке: метка состояния и начало текста.
func (a *App) txtLabel(lang, key string) string {
	bl := a.botLang()
	canon, own := i18n.Effective(bl, key)
	mark := ""
	if own {
		mark = "✏️ "
		if o, ok := a.textOverride(bl, key); ok && textStale(bl, key, o) {
			mark = "⚠️ "
		}
	} else if _, ok := a.textOverride(bl, key); ok {
		mark = "⛔ "
	}
	e, _ := i18n.EditableByKey(key)
	body := i18n.Display(lang, key, canon)
	if e != nil && e.Kind == i18n.KindHTML {
		body = visibleText(body)
	}
	body = strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(body) > txtLabelMax {
		body = string([]rune(body)[:txtLabelMax]) + "…"
	}
	if body == "" && e != nil {
		// Пустой необязательный текст узнаётся по подсказке «где видно».
		body = e.WhereText(lang)
		if utf8.RuneCountInString(body) > txtLabelMax {
			body = string([]rune(body)[:txtLabelMax]) + "…"
		}
	}
	if body == "" {
		body = key
	}
	return mark + body
}

// showTextsList — общий список текстов с листанием.
func (a *App) showTextsList(ctx context.Context, chatID int64, head string, keys []string, page int, pagePrefix, back string) {
	lang := a.lang(chatID)
	pages := (len(keys) + txtPageSize - 1) / txtPageSize
	if pages == 0 {
		pages = 1
	}
	if page < 0 || page >= pages {
		page = 0
	}
	a.getUI(chatID).txtBack = pagePrefix + strconv.Itoa(page)
	var rows [][]models.InlineKeyboardButton
	end := min((page+1)*txtPageSize, len(keys))
	for _, key := range keys[page*txtPageSize : end] {
		rows = append(rows, []models.InlineKeyboardButton{btn(a.txtLabel(lang, key), "tx:k:"+key)})
	}
	if nav := paginationRow(pagePrefix, page, pages, i18n.T(lang, "btn.prev"), i18n.T(lang, "btn.next")); len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, navBack(lang, back))
	a.sendIfaceKB(ctx, chatID, head, rows)
}

func (a *App) showTextsSection(ctx context.Context, chatID int64, sec string, page int) {
	lang := a.lang(chatID)
	var keys []string
	for _, e := range i18n.EditableIn(sec) {
		keys = append(keys, e.Key)
	}
	if len(keys) == 0 {
		a.showTextsHome(ctx, chatID)
		return
	}
	a.showTextsList(ctx, chatID, i18n.T(lang, "tx.list_title", txtSectionTitle(lang, sec), len(keys)), keys, page, "tx:s:"+sec+":", "tx:home")
}

func (a *App) showTextsChanged(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	own := a.textOverrides(a.botLang())
	var keys []string
	for _, e := range i18n.EditableAll() {
		if _, ok := own[e.Key]; ok {
			keys = append(keys, e.Key)
		}
	}
	if len(keys) == 0 {
		a.showTextsHome(ctx, chatID)
		return
	}
	a.showTextsList(ctx, chatID, i18n.T(lang, "tx.changed_title", len(keys)), keys, page, "tx:ch:", "tx:home")
}

func (a *App) showTextsAttention(ctx context.Context, chatID int64, page int) {
	lang := a.lang(chatID)
	stale, broken := a.textsAttention(a.botLang())
	keys := append(broken, stale...)
	if len(keys) == 0 {
		a.showTextsHome(ctx, chatID)
		return
	}
	a.showTextsList(ctx, chatID, i18n.T(lang, "tx.attention_title"), keys, page, "tx:att:", "tx:home")
}

// searchTexts ищет по тексту, который видит пользователь, по стандартному
// тексту, по подсказке «где видно», по именам переменных и по ключу.
func (a *App) searchTexts(q string) []string {
	norm := func(s string) string { return strings.ReplaceAll(strings.ToLower(s), "ё", "е") }
	q = norm(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	bl := a.botLang()
	var out []string
	for _, e := range i18n.EditableAll() {
		canon, _ := i18n.Effective(bl, e.Key)
		hay := []string{
			visibleText(i18n.Display("ru", e.Key, canon)),
			visibleText(i18n.Display("en", e.Key, canon)),
			visibleText(i18n.DefaultCanonical(bl, e.Key)),
			e.Where[0], e.Where[1], e.Key,
		}
		for _, v := range e.Vars {
			hay = append(hay, v.Ru, v.Name)
		}
		for _, h := range hay {
			if strings.Contains(norm(h), q) {
				out = append(out, e.Key)
				break
			}
		}
	}
	return out
}

func (a *App) showTextsSearch(ctx context.Context, chatID int64, q string, page int) {
	lang := a.lang(chatID)
	keys := a.searchTexts(q)
	shown := escapeHTMLText(q)
	if len(keys) == 0 {
		a.sendIfaceKB(ctx, chatID, i18n.T(lang, "tx.search_none", shown), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "tx.btn_search"), "tx:q")},
			navBack(lang, "tx:home"),
		})
		return
	}
	a.showTextsList(ctx, chatID, i18n.T(lang, "tx.search_title", shown, len(keys)), keys, page, "tx:f:", "tx:home")
}

// applyTextSearch — ответ на «Найти текст».
func (a *App) applyTextSearch(ctx context.Context, chatID int64, q string) {
	ui := a.getUI(chatID)
	ui.adminInput = ""
	ui.inputBack = ""
	ui.txtQuery = q
	a.showTextsSearch(ctx, chatID, q, 0)
}

// txtHTMLLimit — предел видимой длины сообщения.
func txtHTMLLimit(e *i18n.Editable) int {
	if e.MaxLen > 0 {
		return e.MaxLen
	}
	return txtMessageMax
}

// txtKindLine — строка «формат» карточки.
func txtKindLine(lang string, e *i18n.Editable) string {
	switch e.Kind {
	case i18n.KindButton:
		return i18n.T(lang, "tx.kind_button")
	case i18n.KindPlain:
		if e.MaxLen > 0 {
			return i18n.T(lang, "tx.kind_plain_max", e.MaxLen)
		}
		return i18n.T(lang, "tx.kind_plain")
	}
	return i18n.T(lang, "tx.kind_html_max", txtHTMLLimit(e))
}

// txtPreviewHTML — текст с примерами значений в виде, пригодном для
// сообщения с разметкой HTML.
func txtPreviewHTML(lang, key, canonical string) string {
	e, _ := i18n.EditableByKey(key)
	out := i18n.RenderExample(lang, key, canonical)
	if e != nil && e.Kind != i18n.KindHTML {
		out = escapeHTMLText(out)
	}
	return out
}

// txtKeyRefRe — ссылка описания на другой текст («ap.when_days»): код текста
// показывается моноширинным, чтобы его можно было скопировать в поиск.
var txtKeyRefRe = regexp.MustCompile(`«([a-z]+\.[a-z_*]+)»`)

func (a *App) txtVarsBlock(lang string, e *i18n.Editable) string {
	vars := e.UniqueVars()
	if len(vars) == 0 {
		return i18n.T(lang, "tx.no_vars")
	}
	var b strings.Builder
	b.WriteString(i18n.T(lang, "tx.vars_head"))
	idx := 0
	if lang == "en" {
		idx = 1
	}
	for _, v := range vars {
		b.WriteString("\n")
		desc := txtKeyRefRe.ReplaceAllString(escapeHTMLText(v.Desc[idx]), "<code>$1</code>")
		// Пример бывает целым фрагментом с разметкой и переносами — в строке
		// списка он показывается как его увидит человек.
		ex := strings.Join(strings.Fields(visibleText(v.Example[idx])), " ")
		b.WriteString(i18n.T(lang, "tx.var_line", escapeHTMLText(v.VarName(lang)), desc, escapeHTMLText(ex)))
	}
	return b.String()
}

func (a *App) showTextCard(ctx context.Context, chatID int64, key, note string) {
	lang := a.lang(chatID)
	e, ok := i18n.EditableByKey(key)
	if !ok {
		a.showTextsHomeNote(ctx, chatID, i18n.T(lang, "tx.gone"))
		return
	}
	bl := a.botLang()
	canon, own := i18n.Effective(bl, key)
	o, stored := a.textOverride(bl, key)

	var b strings.Builder
	if note != "" {
		b.WriteString(note + "\n\n")
	}
	b.WriteString(i18n.T(lang, "tx.card_head", txtSectionTitle(lang, e.Section), key, escapeHTMLText(e.WhereText(lang))))
	b.WriteString("\n" + txtKindLine(lang, e))
	switch {
	case stored && !own:
		b.WriteString("\n\n" + i18n.T(lang, "tx.st_broken"))
	case own:
		at := o.At
		if t, err := time.Parse(time.RFC3339, o.At); err == nil {
			at = t.In(displayTZ).Format("02.01.2006 15:04")
		}
		b.WriteString("\n" + i18n.T(lang, "tx.st_custom", escapeHTMLText(at)))
		if textStale(bl, key, o) {
			b.WriteString("\n\n" + i18n.T(lang, "tx.st_stale"))
		}
	default:
		if e.Optional {
			b.WriteString("\n" + i18n.T(lang, "tx.st_empty"))
		} else {
			b.WriteString("\n" + i18n.T(lang, "tx.st_default"))
		}
	}
	b.WriteString("\n\n" + a.txtVarsBlock(lang, e))
	head := b.String()

	preview := i18n.T(lang, "tx.preview_head")
	if e.Kind == i18n.KindButton {
		preview += "\n" + i18n.T(lang, "tx.preview_button")
	} else {
		body := txtPreviewHTML(bl, key, canon)
		if strings.TrimSpace(canon) == "" {
			body = i18n.T(lang, "tx.preview_empty")
		}
		preview += "\n——————\n" + body + "\n——————"
	}

	var rows [][]models.InlineKeyboardButton
	if e.Kind == i18n.KindButton {
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.RenderExample(bl, key, canon), "tx:noop")})
	}
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_edit"), "tx:e:"+key), btn(i18n.T(lang, "tx.btn_test"), "tx:t:"+key)})
	switch {
	case stored && e.Optional:
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_clear"), "tx:r:"+key)})
	case stored:
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_default"), "tx:d:"+key), btn(i18n.T(lang, "tx.btn_reset"), "tx:r:"+key)})
	}
	if stored {
		if own && textStale(bl, key, o) {
			rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_keep"), "tx:kp:"+key)})
		}
	}
	back := a.getUI(chatID).txtBack
	if back == "" {
		back = "tx:s:" + e.Section + ":0"
	}
	rows = append(rows, navBack(lang, back))

	full := head + "\n\n" + preview
	if utf8.RuneCountInString(visibleText(full)) > 3900 {
		a.sendKBParts(ctx, chatID, []string{head, preview}, rows)
		return
	}
	a.sendIfaceKB(ctx, chatID, full, rows)
}

// txtTemplate — текст для копирования: переменные с именами языка админа,
// ссылки на переменные — записью [текст]({ссылка}).
func txtTemplate(lang, key, canonical string) string {
	e, _ := i18n.EditableByKey(key)
	out := i18n.Display(lang, key, canonical)
	if e != nil && e.Kind != i18n.KindHTML {
		return escapeHTMLText(out)
	}
	return varLinksToMarkup(out)
}

func (a *App) txtRules(lang string, e *i18n.Editable) string {
	var rules string
	switch e.Kind {
	case i18n.KindButton:
		rules = i18n.T(lang, "tx.rules_button")
	case i18n.KindPlain:
		rules = i18n.T(lang, "tx.rules_plain")
		if e.MaxLen > 0 {
			rules += "\n" + i18n.T(lang, "tx.rules_max", e.MaxLen)
		}
	default:
		rules = i18n.T(lang, "tx.rules_html") + "\n" + i18n.T(lang, "tx.rules_max", txtHTMLLimit(e))
	}
	if e.Optional {
		rules += "\n• " + i18n.T(lang, "tx.optional_hint")
	}
	vars := e.UniqueVars()
	if len(vars) == 0 {
		return rules + "\n\n" + i18n.T(lang, "tx.rules_no_vars")
	}
	names := make([]string, 0, len(vars))
	for _, v := range vars {
		names = append(names, "<code>{"+escapeHTMLText(v.VarName(lang))+"}</code>")
	}
	return rules + "\n\n" + i18n.T(lang, "tx.rules_vars", strings.Join(names, ", "))
}

// hrefLinkRe — ссылка, адрес которой — переменная.
var hrefLinkRe = regexp.MustCompile(`<a href="\{([a-z0-9_]+)\}">`)

// hrefVars — переменные, стоящие адресом ссылки, но адресом не являющиеся:
// ссылку «на имя» Telegram не примет.
func hrefVars(canon string, e *i18n.Editable) []string {
	var bad []string
	for _, m := range hrefLinkRe.FindAllStringSubmatch(canon, -1) {
		if !e.IsLinkVar(m[1]) {
			bad = append(bad, m[1])
		}
	}
	return bad
}

// resetPendingInputs снимает ожидания ввода других разделов: они проверяются
// раньше редактора текстов и перехватили бы присланный текст (запрос поиска
// ушёл бы в приветствие или в причину отказа по заявке).
func resetPendingInputs(ui *uiState) {
	ui.adminInput = ""
	ui.inputBack = ""
	ui.welcomeAwait = ""
	ui.torAwait = false
	ui.awaitEmojiFor = ""
	ui.rejectReq = 0
	ui.awaitTopUp = false
	ui.awaitPromo = false
	ui.awaitLogo = ""
	ui.awaitSectionBanner = ""
	ui.awaitRSDump = false
	ui.awaitPlanImport = false
}

// clearTextInput снимает ожидание текста и убирает сообщение-шаблон.
func (a *App) clearTextInput(ctx context.Context, chatID int64) {
	ui := a.getUI(chatID)
	ui.txtKey = ""
	ui.txtDraft = ""
	if ui.txtTplMsg != 0 {
		a.msg.Delete(ctx, chatID, ui.txtTplMsg)
		ui.txtTplMsg = 0
	}
}

func (a *App) startTextEdit(ctx context.Context, chatID int64, key string) {
	lang := a.lang(chatID)
	e, ok := i18n.EditableByKey(key)
	if !ok {
		a.showTextsHomeNote(ctx, chatID, i18n.T(lang, "tx.gone"))
		return
	}
	a.clearTextInput(ctx, chatID)
	ui := a.getUI(chatID)
	resetPendingInputs(ui)
	ui.txtKey = key
	canon, _ := i18n.Effective(a.botLang(), key)
	// Пустому тексту шаблон не нужен: пустое сообщение Telegram не примет.
	empty := strings.TrimSpace(canon) == ""
	ask := "tx.edit_ask"
	if empty {
		ask = "tx.edit_ask_empty"
	}
	a.sendIfaceKB(ctx, chatID, i18n.T(lang, ask, a.txtRules(lang, e)), [][]models.InlineKeyboardButton{
		{btn(i18n.T(lang, "btn.cancel"), "tx:x")},
	})
	if !empty {
		ui.txtTplMsg = a.msg.Send(ctx, chatID, txtTemplate(lang, key, canon))
	}
}

// textDraftFromMessage превращает сообщение админа в канонический текст.
// errs — почему текст не годится, warns — что стоит знать перед сохранением.
func (a *App) textDraftFromMessage(lang, key string, m *models.Message) (canon string, errs, warns []string) {
	e, ok := i18n.EditableByKey(key)
	if !ok {
		return "", []string{i18n.T(lang, "tx.gone")}, nil
	}
	if m.Text == "" {
		return "", []string{i18n.T(lang, "tx.err_not_text")}, nil
	}
	var raw string
	if e.Kind == i18n.KindHTML {
		raw = varLinksToHTML(entitiesToHTML(m.Text, m.Entities))
	} else {
		raw = m.Text
		for _, ent := range m.Entities {
			if openTag(ent) != "" {
				warns = append(warns, i18n.T(lang, "tx.warn_format"))
				break
			}
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", []string{i18n.T(lang, "tx.err_empty")}, nil
	}
	bl := a.botLang()
	// Telegram обрезает пробелы и переносы по краям сообщения, а у части
	// текстов они смысловые: « и » между названиями, пустая строка перед
	// дописанной строкой. Края берутся у стандартного текста.
	def := i18n.DefaultCanonical(bl, key)
	lead := def[:len(def)-len(strings.TrimLeft(def, " \t\r\n"))]
	trail := def[len(strings.TrimRight(def, " \t\r\n")):]
	// Знак препинания в начале прилипает к предыдущему слову: «, » между
	// названиями, а не « , ».
	if r, _ := utf8.DecodeRuneInString(raw); unicode.IsPunct(r) {
		lead = strings.TrimLeft(lead, " ")
	}
	raw = lead + raw + trail
	canon, unknown := i18n.Canonicalize(key, raw)
	if len(unknown) > 0 {
		quoted := make([]string, len(unknown))
		for i, u := range unknown {
			quoted[i] = "{" + escapeHTMLText(u) + "}"
		}
		if len(e.Vars) == 0 {
			errs = append(errs, i18n.T(lang, "tx.err_no_vars", strings.Join(quoted, ", ")))
		} else {
			var have []string
			for _, v := range e.UniqueVars() {
				have = append(have, "{"+escapeHTMLText(v.VarName(lang))+"}")
			}
			errs = append(errs, i18n.T(lang, "tx.err_unknown", strings.Join(quoted, ", "), strings.Join(have, ", ")))
		}
	}
	if bad := hrefVars(canon, e); len(bad) > 0 {
		var links []string
		for _, v := range e.UniqueVars() {
			if e.IsLinkVar(v.Name) {
				links = append(links, "{"+escapeHTMLText(v.VarName(lang))+"}")
			}
		}
		if len(links) == 0 {
			errs = append(errs, i18n.T(lang, "tx.err_link_none"))
		} else {
			errs = append(errs, i18n.T(lang, "tx.err_link_var", strings.Join(links, ", ")))
		}
	}
	example := i18n.RenderWorst(bl, key, canon)
	switch e.Kind {
	case i18n.KindButton:
		if strings.ContainsAny(canon, "\r\n") {
			errs = append(errs, i18n.T(lang, "tx.err_newline"))
		}
		if strings.ContainsAny(canon, "<>&") {
			errs = append(errs, i18n.T(lang, "tx.err_chars"))
		}
		if n := utf8.RuneCountInString(example); n > txtButtonMax {
			errs = append(errs, i18n.T(lang, "tx.err_len", n, txtButtonMax))
		}
	case i18n.KindPlain:
		if strings.ContainsAny(canon, "<>&") {
			errs = append(errs, i18n.T(lang, "tx.err_chars"))
		}
		if e.MaxLen > 0 {
			if n := utf8.RuneCountInString(example); n > e.MaxLen {
				errs = append(errs, i18n.T(lang, "tx.err_len", n, e.MaxLen))
			}
		}
	default:
		limit := txtHTMLLimit(e)
		if n := utf8.RuneCountInString(visibleText(example)); n > limit {
			errs = append(errs, i18n.T(lang, "tx.err_len", n, limit))
		}
	}
	if len(errs) == 0 {
		if err := i18n.Compile(bl, key, canon); err != nil {
			switch {
			case errors.Is(err, i18n.ErrBadChars):
				errs = append(errs, i18n.T(lang, "tx.err_chars"))
			case errors.Is(err, i18n.ErrUnknownVar):
				errs = append(errs, i18n.T(lang, "tx.err_bad_var"))
			case errors.Is(err, i18n.ErrNewline):
				errs = append(errs, i18n.T(lang, "tx.err_newline"))
			case errors.Is(err, i18n.ErrTooLong):
				errs = append(errs, i18n.T(lang, "tx.err_too_long"))
			default:
				errs = append(errs, i18n.T(lang, "tx.err_empty"))
			}
		}
	}
	if len(errs) > 0 {
		return "", errs, nil
	}
	if miss := i18n.MissingVars(key, canon); len(miss) > 0 {
		names := make([]string, len(miss))
		for i, v := range miss {
			names[i] = "{" + escapeHTMLText(v.VarName(lang)) + "}"
		}
		warns = append(warns, i18n.T(lang, "tx.warn_missing", strings.Join(names, ", ")))
	}
	return canon, nil, warns
}

// onTextInput — сообщение админа, пока ждём новый текст.
func (a *App) onTextInput(ctx context.Context, chatID int64, m *models.Message) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	key := ui.txtKey
	canon, errs, warns := a.textDraftFromMessage(lang, key, m)
	cancel := []models.InlineKeyboardButton{btn(i18n.T(lang, "btn.cancel"), "tx:x")}
	if len(errs) > 0 {
		ui.txtDraft = ""
		a.sendIfaceKB(ctx, chatID, i18n.T(lang, "tx.err_head", strings.Join(errs, "\n")), [][]models.InlineKeyboardButton{cancel})
		return
	}
	ui.txtDraft = canon
	e, _ := i18n.EditableByKey(key)
	var b strings.Builder
	b.WriteString(i18n.T(lang, "tx.preview_title", key))
	var rows [][]models.InlineKeyboardButton
	if e.Kind == i18n.KindButton {
		b.WriteString("\n" + i18n.T(lang, "tx.preview_button"))
		rows = append(rows, []models.InlineKeyboardButton{btn(i18n.RenderExample(a.botLang(), key, canon), "tx:noop")})
	} else {
		b.WriteString("\n——————\n" + txtPreviewHTML(a.botLang(), key, canon) + "\n——————")
	}
	for _, w := range warns {
		b.WriteString("\n\n" + w)
	}
	b.WriteString("\n\n" + i18n.T(lang, "tx.save_ask"))
	rows = append(rows, []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_save"), "tx:ok"), cancel[0]})
	a.sendIfaceKB(ctx, chatID, b.String(), rows)
}

func (a *App) saveTextDraft(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	ui := a.getUI(chatID)
	key, draft := ui.txtKey, ui.txtDraft
	if key == "" || draft == "" {
		a.showTextsHome(ctx, chatID)
		return
	}
	if err := i18n.Compile(a.botLang(), key, draft); err != nil {
		a.showTextCard(ctx, chatID, key, "")
		return
	}
	bl := a.botLang()
	// Текст, совпавший со стандартным, — это возврат к стандартному: иначе
	// правка стандартного текста в новой версии бота до пользователя не дошла
	// бы.
	if draft == i18n.DefaultCanonical(bl, key) {
		draft = ""
	}
	note := i18n.T(lang, "tx.saved")
	if err := a.setTextOverride(ctx, bl, key, draft); err != nil {
		// Черновик и шаблон остаются: админ повторит «Сохранить».
		a.log.Warn("тексты: не сохранено", "key", key, "err", err)
		a.sendIfaceKB(ctx, chatID, i18n.T(lang, "tx.save_failed"), [][]models.InlineKeyboardButton{
			{btn(i18n.T(lang, "tx.btn_save"), "tx:ok"), btn(i18n.T(lang, "btn.cancel"), "tx:x")},
		})
		return
	}
	a.clearTextInput(ctx, chatID)
	a.showTextCard(ctx, chatID, key, note)
}

func (a *App) sendTextTest(ctx context.Context, chatID int64, key string) {
	lang := a.lang(chatID)
	e, ok := i18n.EditableByKey(key)
	if !ok {
		return
	}
	bl := a.botLang()
	canon, _ := i18n.Effective(bl, key)
	closeRow := []models.InlineKeyboardButton{btn(i18n.T(lang, "tx.btn_close"), "x:close")}
	if strings.TrimSpace(canon) == "" {
		a.msg.SendKB(ctx, chatID, i18n.T(lang, "tx.test_head")+"\n\n"+i18n.T(lang, "tx.preview_empty"),
			[][]models.InlineKeyboardButton{closeRow})
		return
	}
	if e.Kind == i18n.KindButton {
		a.msg.SendKB(ctx, chatID, i18n.T(lang, "tx.test_button"), [][]models.InlineKeyboardButton{
			{btn(i18n.RenderExample(bl, key, canon), "tx:noop")}, closeRow,
		})
		return
	}
	a.msg.SendKB(ctx, chatID, a.applyPremium(i18n.T(lang, "tx.test_head")+"\n\n"+txtPreviewHTML(bl, key, canon)),
		[][]models.InlineKeyboardButton{closeRow})
}

func (a *App) sendTextDefault(ctx context.Context, chatID int64, key string) {
	lang := a.lang(chatID)
	if _, ok := i18n.EditableByKey(key); !ok {
		return
	}
	a.msg.SendKB(ctx, chatID, i18n.T(lang, "tx.default_head")+"\n\n"+txtTemplate(lang, key, i18n.DefaultCanonical(a.botLang(), key)),
		[][]models.InlineKeyboardButton{{btn(i18n.T(lang, "tx.btn_close"), "x:close")}})
}
