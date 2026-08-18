package app

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Документы сервиса: пользовательское соглашение и политика конфиденциальности.
//
// Один документ — это текст в боте и/или ссылка на страницу (model.LegalDoc).
// Места показа включаются оператором по отдельности: кнопка в меню, согласие
// перед покупкой, согласие при первом входе, приписка на экране оплаты.
// Согласие одно на все включённые документы — дата пишется в
// users.terms_accepted_at, как и раньше у единственного соглашения.

// legalTextLimit — сколько знаков документа влезает в одно сообщение.
// Лимит Telegram — 4096 знаков вместе с разметкой; остаток оставлен под
// заголовок, приписку и служебные строки.
const legalTextLimit = 3500

func (a *App) legalCfg() model.LegalConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.botCfg == nil {
		return model.LegalConfig{}
	}
	return a.botCfg.Legal
}

// legalDocTitle — название документа на языке бота.
func legalDocTitle(lang, kind string) string {
	if kind == model.LegalPrivacy {
		return i18n.T(lang, "legal.doc_privacy")
	}
	return i18n.T(lang, "legal.doc_terms")
}

// legalAccepted — принял ли пользователь документы.
func (a *App) legalAccepted(ctx context.Context, chatID int64) bool {
	if a.store == nil {
		return true
	}
	u, err := a.store.GetUser(ctx, chatID)
	if err != nil || u == nil {
		// Пользователя ещё нет в базе (или база недоступна) — гейт не вешаем:
		// иначе первая же ошибка хранилища закрывала бы бота целиком.
		return true
	}
	return u.TermsAcceptedAt != ""
}

// legalRequired — нужно ли показать экран согласия ПЕРЕД покупкой.
// Совместимо со старым termsRequired: гейт работает, только когда оператор его
// включил и хоть один документ задан.
func (a *App) legalRequired(ctx context.Context, chatID int64) bool {
	cfg := a.legalCfg()
	if !cfg.Any() || !(cfg.GateBuy || cfg.GateStart) {
		return false
	}
	return !a.legalAccepted(ctx, chatID)
}

// legalStartRequired — нужно ли показать согласие при входе в бота.
func (a *App) legalStartRequired(ctx context.Context, chatID int64) bool {
	cfg := a.legalCfg()
	if !cfg.Any() || !cfg.GateStart {
		return false
	}
	return !a.legalAccepted(ctx, chatID)
}

// legalTelegramHTML приводит текст документа к разметке, которую Telegram
// точно примет: одиночная «<» (например, «возраст < 18») ломает разбор, и
// сообщение уходит по запасному пути — без разметки И БЕЗ ЧАСТИ ТЕКСТА
// (stripHTMLTags режет от такой скобки до конца строки). Для документа, под
// которым человек ставит согласие, это недопустимо, поэтому всё, что не
// является разрешённым тегом, экранируется на входе.
func legalTelegramHTML(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		switch text[i] {
		case '<':
			if m := legalTagScan.FindString(text[i:]); m != "" && strings.HasPrefix(text[i:], m) {
				b.WriteString(m)
				i += len(m)
				continue
			}
			b.WriteString("&lt;")
			i++
		case '&':
			if legalEntityRe.MatchString(text[i:]) {
				ent := legalEntityRe.FindString(text[i:])
				b.WriteString(ent)
				i += len(ent)
				continue
			}
			b.WriteString("&amp;")
			i++
		case '>':
			b.WriteString("&gt;")
			i++
		default:
			b.WriteByte(text[i])
			i++
		}
	}
	return b.String()
}

// legalDocParts — документ, нарезанный на сообщения. Обрезать документ, под
// которым человек ставит согласие, нельзя, а лимит сообщения Telegram — 4096
// знаков вместе с разметкой, поэтому длинный текст уходит частями. Режем по
// переносам строк, а теги, оставшиеся открытыми на границе, закрываем в конце
// части и заново открываем в начале следующей — иначе Telegram отвергнет
// разметку и часть текста пропадёт.
func legalDocParts(lang string, it model.LegalItem) []string {
	head := "<b>" + legalDocTitle(lang, it.Kind) + "</b>"
	var lines []string
	if it.Doc.Text != "" {
		lines = splitLegalLines(legalTelegramHTML(it.Doc.Text))
	}
	if it.Doc.URL != "" {
		lines = append(lines, "", i18n.T(lang, "legal.read_full", html_(it.Doc.URL)))
	}
	if len(lines) == 0 {
		return []string{head}
	}

	var parts []string
	var open []string
	cur, started := head, false
	for _, line := range lines {
		// В бюджет части входят и закрывающие теги, которые допишутся в её
		// конец: без этого часть с открытой разметкой перерастала лимит.
		if len([]rune(cur))+len([]rune(line))+len([]rune(closeTags(open)))+1 > legalTextLimit {
			parts = append(parts, cur+closeTags(open))
			cur, started = strings.Join(open, ""), false
		}
		if started || cur == head {
			cur += "\n"
		}
		cur += line
		started = true
		open = trackTags(open, line)
	}
	return append(parts, cur+closeTags(open))
}

// splitLegalLines разбивает текст на строки, дополнительно разрезая строки
// длиннее лимита: одна «простыня» без переносов иначе не уехала бы никогда.
// Рез ищется по безопасной границе — не внутри тега и не внутри HTML-сущности,
// иначе разметка разъедется и Telegram выбросит часть текста.
func splitLegalLines(text string) []string {
	var out []string
	// Запас на заголовок и на переоткрытые теги в начале части.
	limit := legalTextLimit - 400
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > limit {
			cut := safeCut(r, limit)
			out = append(out, string(r[:cut]))
			r = r[cut:]
		}
		out = append(out, string(r))
	}
	return out
}

// safeCut возвращает позицию реза не дальше limit: сначала пробует последний
// пробел, потом отступает от незакрытого «<» или «&».
func safeCut(r []rune, limit int) int {
	cut := limit
	for i := limit; i > limit/2; i-- {
		if r[i-1] == ' ' {
			cut = i
			break
		}
	}
	for cut > 0 {
		tail := string(r[:cut])
		if lt := strings.LastIndexByte(tail, '<'); lt >= 0 && !strings.Contains(tail[lt:], ">") {
			cut = len([]rune(tail[:lt]))
			continue
		}
		if amp := strings.LastIndexByte(tail, '&'); amp >= 0 && len(tail)-amp <= 10 && !strings.Contains(tail[amp:], ";") {
			cut = len([]rune(tail[:amp]))
			continue
		}
		break
	}
	if cut <= 0 {
		cut = limit
	}
	return cut
}

// trackTags ведёт стек открытых тегов: в стеке лежат целые открывающие теги,
// чтобы следующая часть открылась ровно так же (у ссылки важен href).
func trackTags(open []string, line string) []string {
	for _, m := range legalTagAny.FindAllStringSubmatch(line, -1) {
		name := strings.ToLower(m[2])
		if m[1] == "/" {
			for i := len(open) - 1; i >= 0; i-- {
				if tagNameOf(open[i]) == name {
					open = append(open[:i], open[i+1:]...)
					break
				}
			}
			continue
		}
		if name == "br" {
			continue
		}
		// Кривой документ с горой незакрытых тегов не должен раздувать
		// каждую следующую часть — глубина ограничена.
		if len(open) >= 8 {
			continue
		}
		open = append(open, m[0])
	}
	return open
}

func closeTags(open []string) string {
	var b strings.Builder
	for i := len(open) - 1; i >= 0; i-- {
		b.WriteString("</" + tagNameOf(open[i]) + ">")
	}
	return b.String()
}

func tagNameOf(tag string) string {
	m := legalTagScan.FindStringSubmatch(tag)
	if len(m) < 3 {
		return ""
	}
	return strings.ToLower(m[2])
}

// legalDocRows — кнопки документов: со ссылкой ведут на страницу, текстовые
// открывают документ сообщением в боте.
func legalDocRows(lang string, docs []model.LegalItem) [][]models.InlineKeyboardButton {
	var rows [][]models.InlineKeyboardButton
	for _, it := range docs {
		label := i18n.T(lang, "legal.btn_doc", legalDocTitle(lang, it.Kind))
		if it.Doc.Text != "" {
			rows = append(rows, []models.InlineKeyboardButton{btn(label, "terms:doc_"+it.Kind)})
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: label, URL: it.Doc.URL}})
	}
	return rows
}

// askLegal — экран согласия: тексты документов целиком, если влезают, иначе
// кнопки на каждый документ. Одна кнопка «Принимаю» на все документы сразу.
func (a *App) askLegal(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	docs := a.legalCfg().Docs()
	if len(docs) == 0 {
		// Документы могли вычистить, пока человек стоял на экране согласия:
		// молча пропасть нельзя — возвращаем в меню.
		a.getUI(chatID).pendingLegalHome = false
		a.sendKB(ctx, chatID, i18n.T(lang, "cmd.terms_none"), [][]models.InlineKeyboardButton{homeRow(lang)})
		return
	}
	body := i18n.T(lang, "legal.accept_intro")
	var rows [][]models.InlineKeyboardButton
	// Влезет ли всё одним сообщением, решаем по тексту, который реально уйдёт:
	// подстановка премиум-эмодзи оборачивает каждый значок в <tg-emoji …> и
	// увеличивает длину в разы.
	if full, ok := legalInline(lang, docs); ok && len([]rune(a.applyPremium(body+"\n\n"+full))) <= 4000 {
		body += "\n\n" + full
	} else {
		body += "\n\n" + i18n.T(lang, "legal.open_hint")
		rows = legalDocRows(lang, docs)
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "terms.btn_accept"), "terms:accept")},
		[]models.InlineKeyboardButton{btn(i18n.T(lang, "terms.btn_decline"), "terms:decline")},
	)
	a.sendKB(ctx, chatID, body, rows)
}

// legalInline собирает все документы в одно сообщение. Не влезли — false, и
// экран показывает кнопки вместо текста: обрезать соглашение на экране, где
// человек его ПРИНИМАЕТ, нельзя.
func legalInline(lang string, docs []model.LegalItem) (string, bool) {
	var parts []string
	for _, it := range docs {
		part := "<b>" + legalDocTitle(lang, it.Kind) + "</b>"
		if it.Doc.Text != "" {
			part += "\n" + legalTelegramHTML(it.Doc.Text)
		}
		if it.Doc.URL != "" {
			part += "\n" + i18n.T(lang, "legal.read_full", html_(it.Doc.URL))
		}
		parts = append(parts, part)
	}
	joined := strings.Join(parts, "\n\n")
	if len([]rune(joined)) > legalTextLimit {
		return "", false
	}
	return joined, true
}

// showLegalDocs — экран «Документы» для пользователя (кнопка в меню, /terms,
// /privacy). Ничего не требует, просто даёт прочитать.
func (a *App) showLegalDocs(ctx context.Context, chatID int64) {
	lang := a.lang(chatID)
	docs := a.legalCfg().Docs()
	if len(docs) == 0 {
		a.notify(ctx, chatID, i18n.T(lang, "cmd.terms_none"))
		return
	}
	body := i18n.T(lang, "legal.screen")
	rows := legalDocRows(lang, docs)
	rows = append(rows, homeRow(lang))
	a.sendKB(ctx, chatID, body, rows)
}

// showLegalDoc — один документ сообщением (при необходимости несколькими).
func (a *App) showLegalDoc(ctx context.Context, chatID int64, kind string) {
	lang := a.lang(chatID)
	for _, it := range a.legalCfg().Docs() {
		if it.Kind != kind {
			continue
		}
		parts := legalDocParts(lang, it)
		for i, part := range parts {
			// Части уходят обычными сообщениями, а не экраном: экран удаляет
			// предыдущий, и документ съедал бы сам себя вместе с экраном
			// согласия, с которого его открыли.
			if i == len(parts)-1 {
				a.msg.SendKB(ctx, chatID, part, [][]models.InlineKeyboardButton{backHomeRow(lang)})
				return
			}
			a.msg.Send(ctx, chatID, part)
		}
		return
	}
	a.notify(ctx, chatID, i18n.T(lang, "cmd.terms_none"))
}

// legalPayFooter — приписка со ссылками на документы для экрана способов
// оплаты. Пусто, когда оператор её не включил или документов нет.
func (a *App) legalPayFooter(lang string) string {
	cfg := a.legalCfg()
	if !cfg.OnPay {
		return ""
	}
	docs := cfg.Docs()
	if len(docs) == 0 {
		return ""
	}
	var names []string
	for _, it := range docs {
		title := legalDocTitle(lang, it.Kind)
		if it.Doc.URL != "" {
			names = append(names, "<a href=\""+html_(it.Doc.URL)+"\">"+title+"</a>")
			continue
		}
		names = append(names, title)
	}
	return i18n.T(lang, "legal.pay_footer", strings.Join(names, i18n.T(lang, "legal.and")))
}

// legalPayRow — кнопка «Документы» на экране оплаты: нужна, когда хотя бы у
// одного документа нет ссылки и в приписке он остаётся просто названием.
func (a *App) legalPayRow(lang string) []models.InlineKeyboardButton {
	cfg := a.legalCfg()
	if !cfg.OnPay {
		return nil
	}
	for _, it := range cfg.Docs() {
		if it.Doc.URL == "" {
			return []models.InlineKeyboardButton{btn(i18n.T(lang, "legal.btn_menu"), "terms:docs")}
		}
	}
	return nil
}

// legalMenuRow — кнопка «Документы» в главном меню пользователя.
func (a *App) legalMenuRow(lang string) []models.InlineKeyboardButton {
	cfg := a.legalCfg()
	if !cfg.InMenu || !cfg.Any() {
		return nil
	}
	return []models.InlineKeyboardButton{btn(i18n.T(lang, "legal.btn_menu"), "terms:docs")}
}

// onTerms — пользовательские нажатия на экранах документов.
func (a *App) onTerms(ctx context.Context, chatID int64, val, firstName, username string) {
	lang := a.lang(chatID)
	switch {
	case val == "accept":
		fromStart := a.getUI(chatID).pendingLegalHome
		if a.store != nil {
			if err := a.store.SetTermsAccepted(ctx, chatID, time.Now().UTC().Format(time.RFC3339)); err != nil {
				// Не записалось — дальше пускать нельзя: гейт всё равно
				// встретит человека на следующем экране, и он будет ходить по
				// кругу, не понимая почему.
				a.log.Warn("согласие с документами не записано", "err", err, "user", chatID)
				a.sendKB(ctx, chatID, i18n.T(lang, "err.storage"), [][]models.InlineKeyboardButton{
					{btn(i18n.T(lang, "legal.btn_show_again"), "terms:start")},
				})
				return
			}
		}
		// Пришедшего по ссылке на тариф условия перехватили на его экране —
		// после согласия возвращаем туда же, а не на витрину «Базового», где
		// скрытого тарифа нет. Доступность перепроверит openPlanLink.
		ui := a.getUI(chatID)
		ui.pendingLegalHome = false
		if code := ui.pendingPlanOffer; code != "" {
			ui.pendingPlanOffer = ""
			a.openPlanLink(ctx, chatID, code)
			return
		}
		// Согласие на входе (а не перед покупкой) ведёт в меню: человек ничего
		// не покупал, витрина ему сейчас не нужна.
		if fromStart {
			a.enterHome(ctx, chatID, chatID == a.cfg.AdminID, firstName, username)
			return
		}
		a.showPlans(ctx, chatID)
	case val == "decline":
		ui := a.getUI(chatID)
		ui.pendingPlanOffer = ""
		// Вход закрыт согласием — отказ не должен возвращать в меню ни с
		// экрана входа, ни с экрана покупки: оба ведут в одно и то же место.
		if ui.pendingLegalHome || a.legalStartRequired(ctx, chatID) {
			// Вход без согласия закрыт: молча пускать в меню нельзя, но и
			// зацикливать экран согласия незачем — даём кнопку вернуться.
			ui.pendingLegalHome = false
			a.sendKB(ctx, chatID, i18n.T(lang, "legal.start_declined"), [][]models.InlineKeyboardButton{
				{btn(i18n.T(lang, "legal.btn_show_again"), "terms:start")},
			})
			return
		}
		a.enterHome(ctx, chatID, chatID == a.cfg.AdminID, firstName, username)
	case val == "docs":
		a.showLegalDocs(ctx, chatID)
	case val == "start":
		a.getUI(chatID).pendingLegalHome = true
		a.askLegal(ctx, chatID)
	case strings.HasPrefix(val, "doc_"):
		a.showLegalDoc(ctx, chatID, strings.TrimPrefix(val, "doc_"))
	}
}

// legalNames — перечень заданных документов через запятую (для админского
// экрана контактов).
func (a *App) legalNames(lang string) string {
	var names []string
	for _, it := range a.legalCfg().Docs() {
		names = append(names, legalDocTitle(lang, it.Kind))
	}
	return strings.Join(names, ", ")
}

// sanitizeLegalHTML приводит текст документа к безопасной разметке для
// браузера: в чате разметку разбирает Telegram по своему белому списку, а в
// мини-аппе и кабинете HTML попадает прямо в страницу. Экранируем всё, после
// чего возвращаем разрешённые теги — так ни скрипт, ни обработчик события из
// текста документа в страницу не попадут.
func sanitizeLegalHTML(s string) string {
	out := html_(s)
	out = legalTagRe.ReplaceAllString(out, "<$1$2>")
	out = legalLinkRe.ReplaceAllString(out, `<a href="$1" target="_blank" rel="noopener noreferrer">`)
	out = legalLinkEndRe.ReplaceAllString(out, "</a>")
	out = strings.ReplaceAll(out, "\n", "<br>")
	return out
}

var (
	// legalTagScan — разрешённая Telegram разметка (её же понимает и браузер
	// после sanitizeLegalHTML).
	legalTagScan  = regexp.MustCompile(`(?i)^<(/?)(b|strong|i|em|u|ins|s|strike|del|code|pre|br|span|tg-spoiler|blockquote|a)((?:\s[^<>]*)?)>`)
	legalEntityRe = regexp.MustCompile(`^&(?:amp|lt|gt|quot|#\d{1,6}|#x[0-9a-fA-F]{1,6});`)
	// Тот же список без якоря — для поиска тегов внутри строки.
	legalTagAny = regexp.MustCompile(`(?i)<(/?)(b|strong|i|em|u|ins|s|strike|del|code|pre|br|span|tg-spoiler|blockquote|a)((?:\s[^<>]*)?)>`)

	legalTagRe     = regexp.MustCompile(`(?i)&lt;(/?)(b|strong|i|em|u|s|code|pre|br)\s*/?&gt;`)
	legalLinkEndRe = regexp.MustCompile(`(?i)&lt;/a&gt;`)
	legalLinkRe    = regexp.MustCompile(`(?i)&lt;a\s+href=(?:&#34;|&#39;)?(https?://[^&\s]*(?:&amp;[^&\s]*)*)(?:&#34;|&#39;)?\s*&gt;`)
)
