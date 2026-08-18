package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// Старый конфиг знал единственный текст соглашения перед покупкой — после
// обновления он становится документом «Соглашение» с тем же поведением, а
// легаси-поле остаётся зеркалом (откат на старый образ не теряет документ).
func TestLegal_MigrateLegacyTerms(t *testing.T) {
	cfg := &model.BotConfig{}
	cfg.Contact.TermsText = "старые правила"
	cfg.NormalizeLegal()

	if cfg.Legal.Terms.Text != "старые правила" {
		t.Fatalf("текст не перенесён: %q", cfg.Legal.Terms.Text)
	}
	if !cfg.Legal.GateBuy || !cfg.Legal.InMenu {
		t.Fatalf("старое поведение не восстановлено: %+v", cfg.Legal)
	}
	// Повторный запуск ничего не переигрывает.
	cfg.Legal.InMenu = false
	cfg.NormalizeLegal()
	if cfg.Legal.InMenu {
		t.Fatal("перенос повторился поверх выбора оператора")
	}
	// Очистка документа чистит и легаси-зеркало.
	cfg.Legal.Terms.Text = ""
	cfg.NormalizeLegal()
	if cfg.Contact.TermsText != "" {
		t.Fatalf("зеркало не очищено: %q", cfg.Contact.TermsText)
	}
}

// Гейт согласия срабатывает только когда оператор его включил.
func TestLegal_GateOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	uid := int64(501)
	_ = fs.UpsertUser(ctx, uid)

	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "правила"}}
	if a.legalRequired(ctx, uid) {
		t.Fatal("без включённого гейта согласие спрашивать нечего")
	}
	a.botCfg.Legal.InMenu = true
	if a.legalRequired(ctx, uid) {
		t.Fatal("кнопка в меню — не гейт")
	}
	a.botCfg.Legal.GateBuy = true
	if !a.legalRequired(ctx, uid) {
		t.Fatal("гейт перед покупкой не сработал")
	}
	a.handleCallback(ctx, cb(uid, "terms:accept"))
	if a.legalRequired(ctx, uid) {
		t.Fatal("после согласия гейт должен пропускать")
	}
}

// Согласие при входе: до «Принимаю» меню не показывается, после — показывается.
func TestLegal_StartGate(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(502)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{
		Terms:     model.LegalDoc{Text: "правила сервиса"},
		Privacy:   model.LegalDoc{URL: "https://example.com/privacy"},
		GateStart: true,
	}

	a.handleMessage(ctx, msgText(uid, "/start"))
	if !strings.Contains(fm.last(), "правила сервиса") {
		t.Fatalf("на входе должны показать документы: %q", fm.last())
	}
	if !hasCB(fm.allCallbackData(), "terms:accept") {
		t.Fatalf("нет кнопки согласия: %v", fm.allCallbackData())
	}

	// «Не сейчас» в меню не пускает, но оставляет путь назад.
	a.handleCallback(ctx, cb(uid, "terms:decline"))
	if !hasCB(fm.allCallbackData(), "terms:start") {
		t.Fatalf("после отказа нужна кнопка вернуться к документам: %v", fm.allCallbackData())
	}

	a.handleCallback(ctx, cb(uid, "terms:start"))
	a.handleCallback(ctx, cb(uid, "terms:accept"))
	if a.legalStartRequired(ctx, uid) {
		t.Fatal("согласие не записано")
	}
}

// Кнопка «Документы» в меню появляется только с включённым тумблером, а сам
// экран отдаёт документ текстом.
func TestLegal_MenuButtonAndDocScreen(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(503)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "текст соглашения"}}

	if a.legalMenuRow("ru") != nil {
		t.Fatal("кнопка не должна появляться без тумблера")
	}
	a.botCfg.Legal.InMenu = true
	row := a.legalMenuRow("ru")
	if len(row) != 1 || row[0].CallbackData != "terms:docs" {
		t.Fatalf("кнопка «Документы» не показана: %+v", row)
	}

	a.handleCallback(ctx, cb(uid, "terms:docs"))
	if !hasCB(fm.allCallbackData(), "terms:doc_terms") {
		t.Fatalf("экран документов пуст: %v", fm.allCallbackData())
	}
	a.handleCallback(ctx, cb(uid, "terms:doc_terms"))
	if !strings.Contains(fm.last(), "текст соглашения") {
		t.Fatalf("документ не показан: %q", fm.last())
	}
}

// /privacy и /docs открывают тот же экран документов, что и /terms.
func TestLegal_Commands(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(504)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{Privacy: model.LegalDoc{Text: "как мы храним данные"}}

	for _, cmd := range []string{"/terms", "/privacy", "/docs"} {
		a.handleMessage(ctx, msgText(uid, cmd))
		if !strings.Contains(fm.last(), "Документы") {
			t.Fatalf("%s не открыл документы: %q", cmd, fm.last())
		}
		if !hasCB(fm.allCallbackData(), "terms:doc_privacy") {
			t.Fatalf("%s не открыл документы: %v", cmd, fm.allCallbackData())
		}
	}
}

// На экране способов оплаты появляется приписка со ссылками — так требуют
// платёжные провайдеры.
func TestLegal_PayFooter(t *testing.T) {
	lang := "ru"
	a, _, _ := planAdminApp(t)
	a.botCfg.Legal = model.LegalConfig{
		Terms:   model.LegalDoc{URL: "https://example.com/terms"},
		Privacy: model.LegalDoc{Text: "политика"},
	}
	if got := a.legalPayFooter(lang); got != "" {
		t.Fatalf("без тумблера приписки быть не должно: %q", got)
	}
	a.botCfg.Legal.OnPay = true
	got := a.legalPayFooter(lang)
	if !strings.Contains(got, "https://example.com/terms") || !strings.Contains(got, "Политика конфиденциальности") {
		t.Fatalf("в приписке нет документов: %q", got)
	}
}

// Экран согласия не режет документ: не влезло — показываем кнопками.
func TestLegal_LongDocsGoToButtons(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(505)
	_ = fs.UpsertUser(ctx, uid)
	long := strings.Repeat("я", legalTextLimit+10)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: long}, GateBuy: true}

	a.askLegal(ctx, uid)
	if strings.Contains(fm.last(), long) {
		t.Fatal("длинный документ не должен уезжать в одно сообщение целиком")
	}
	if !hasCB(fm.allCallbackData(), "terms:doc_terms") {
		t.Fatalf("нет кнопки открыть документ: %v", fm.allCallbackData())
	}
}

// Админ задаёт документ и места показа; первый документ сам включает показ.
func TestLegal_AdminFlow(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)

	a.handleCallback(ctx, cb(planAdmin, "leg:open"))
	if !strings.Contains(fm.last(), "Политика конфиденциальности") {
		t.Fatalf("экран документов не открылся: %q", fm.last())
	}

	a.handleCallback(ctx, cb(planAdmin, "leg:pt"))
	a.handleMessage(ctx, msgText(planAdmin, "как мы храним данные"))
	if a.botCfg.Legal.Privacy.Text != "как мы храним данные" {
		t.Fatalf("политика не сохранена: %+v", a.botCfg.Legal)
	}
	if !a.botCfg.Legal.InMenu || !a.botCfg.Legal.GateBuy {
		t.Fatalf("первый документ должен включить показ: %+v", a.botCfg.Legal)
	}

	a.handleCallback(ctx, cb(planAdmin, "leg:pu"))
	a.handleMessage(ctx, msgText(planAdmin, "example.com/privacy"))
	if a.botCfg.Legal.Privacy.URL != "https://example.com/privacy" {
		t.Fatalf("ссылка не нормализована: %q", a.botCfg.Legal.Privacy.URL)
	}

	a.handleCallback(ctx, cb(planAdmin, "leg:tu"))
	a.handleMessage(ctx, msgText(planAdmin, "не ссылка"))
	if a.botCfg.Legal.Terms.URL != "" {
		t.Fatalf("мусор вместо ссылки не должен сохраняться: %q", a.botCfg.Legal.Terms.URL)
	}

	a.handleCallback(ctx, cb(planAdmin, "leg:pay"))
	if !a.botCfg.Legal.OnPay {
		t.Fatal("тумблер ссылок на оплате не сработал")
	}
}

// Сброс согласий заставляет принять документы заново.
func TestLegal_ResetConsent(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	uid := int64(506)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "правила"}, GateBuy: true}
	a.handleCallback(ctx, cb(uid, "terms:accept"))

	a.handleCallback(ctx, cb(planAdmin, "leg:reset"))
	if a.legalRequired(ctx, uid) {
		t.Fatal("сброс не должен срабатывать до подтверждения")
	}
	a.handleCallback(ctx, cb(planAdmin, "leg:resetok"))
	if !a.legalRequired(ctx, uid) {
		t.Fatal("после сброса согласие должно спрашиваться заново")
	}
}

// Текст документа уезжает в браузер (мини-апп, кабинет) без активной разметки.
func TestLegal_SanitizeHTML(t *testing.T) {
	got := sanitizeLegalHTML("<b>Правила</b> <script>alert(1)</script>\n<a href=\"https://example.com/a?b=1&c=2\">тут</a> <img src=x onerror=alert(1)>")
	if strings.Contains(got, "<script") || strings.Contains(got, "<img") || strings.Contains(got, "<a href=\"javascript") {
		t.Fatalf("опасная разметка не вычищена: %q", got)
	}
	if !strings.Contains(got, "<b>Правила</b>") {
		t.Fatalf("разрешённый тег потерян: %q", got)
	}
	if !strings.Contains(got, `<a href="https://example.com/a?b=1&amp;c=2" target="_blank" rel="noopener noreferrer">тут</a>`) {
		t.Fatalf("ссылка разобрана неверно: %q", got)
	}
	if strings.Contains(sanitizeLegalHTML(`<a href="javascript:alert(1)">x</a>`), "<a href") {
		t.Fatal("javascript-ссылка не должна становиться ссылкой")
	}
}

func TestLegal_NormalizeDocURL(t *testing.T) {
	cases := map[string]string{
		"https://example.com/terms":  "https://example.com/terms",
		"example.com/terms":          "https://example.com/terms",
		"  example.com  ":            "https://example.com",
		"не ссылка":                  "",
		"https://":                   "",
		"https://example":            "",
		"javascript:alert(1)":        "",
		"https://example.com:99999/": "",
		`https://example.com/a"x`:    "",
		"https://example.com/a b":    "",
		"https://..com/a":            "",
	}
	for in, want := range cases {
		got, ok := normalizeDocURL(in)
		if want == "" {
			if ok {
				t.Fatalf("%q должно быть отвергнуто, получено %q", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Fatalf("%q -> %q (ok=%v), ожидалось %q", in, got, ok, want)
		}
	}
}

// Гейт «согласие при входе» не обходится кнопкой «На главную».
func TestLegal_StartGateHoldsHomeButton(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(507)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "правила"}, GateStart: true}

	a.handleMessage(ctx, msgText(uid, "/start"))
	a.handleCallback(ctx, cb(uid, "menu:home"))
	if !strings.Contains(fm.last(), "правила") {
		t.Fatalf("«На главную» не должна пускать в меню без согласия: %q", fm.last())
	}
	a.handleCallback(ctx, cb(uid, "terms:accept"))
	a.handleCallback(ctx, cb(uid, "menu:home"))
	if strings.Contains(fm.last(), "правила") {
		t.Fatalf("после согласия меню должно открываться: %q", fm.last())
	}
}

// Оплата с баланса из мини-аппа и кабинета тоже проходит через гейт.
func TestLegal_MiniCheckoutGate(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	uid := int64(508)
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 100000)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "правила"}, GateBuy: true}

	res := a.MiniCheckout(ctx, uid, model.PlanCodeBase, 1, model.PayMethodBalance, false)
	if res.OK || res.Error == "" {
		t.Fatalf("покупка с баланса без согласия должна отбиваться: %+v", res)
	}
	if bal, _ := a.store.GetUser(ctx, uid); bal != nil && bal.Balance != 100000 {
		t.Fatalf("баланс не должен был списаться: %d", bal.Balance)
	}

	if r := a.MiniAcceptLegal(ctx, uid); !r.OK {
		t.Fatalf("согласие из мини-аппа не записано: %+v", r)
	}
	if a.legalRequired(ctx, uid) {
		t.Fatal("после согласия из мини-аппа гейт должен пропускать")
	}
}

// Мини-апп получает документы и состояние тумблеров.
func TestLegal_MiniMenuDocs(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	uid := int64(509)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{
		Terms:   model.LegalDoc{Text: "<b>правила</b>"},
		Privacy: model.LegalDoc{URL: "https://example.com/privacy"},
		InMenu:  true, OnPay: true, GateBuy: true,
	}

	dto := a.MiniMenu(ctx, uid, false)
	if len(dto.Legal) != 2 || !dto.LegalInMenu || !dto.LegalOnPay || !dto.LegalAccept {
		t.Fatalf("документы не доехали до мини-аппа: %+v", dto.Legal)
	}
	if dto.Legal[0].HTML != "<b>правила</b>" || dto.Legal[1].URL != "https://example.com/privacy" {
		t.Fatalf("документы разобраны неверно: %+v", dto.Legal)
	}
}

// Длинный документ уходит частями и целиком — обрезать его нельзя.
func TestLegal_DocSplitsIntoParts(t *testing.T) {
	body := strings.TrimRight(strings.Repeat("строка документа\n", 600), "\n")
	it := model.LegalItem{Kind: model.LegalTerms, Doc: model.LegalDoc{Text: body, URL: "https://example.com/terms"}}
	parts := legalDocParts("ru", it)
	if len(parts) < 2 {
		t.Fatalf("длинный документ должен разбиваться: частей %d", len(parts))
	}
	joined := strings.Join(parts, "\n")
	for _, line := range []string{"строка документа", "https://example.com/terms"} {
		if !strings.Contains(joined, line) {
			t.Fatalf("часть документа потеряна: %q", line)
		}
	}
	if strings.Count(joined, "строка документа") != 600 {
		t.Fatalf("документ обрезан: строк %d", strings.Count(joined, "строка документа"))
	}
	for _, p := range parts {
		if len([]rune(p)) > legalTextLimit {
			t.Fatalf("часть длиннее лимита: %d", len([]rune(p)))
		}
	}
}

// Правка текста при осознанно выключенных тумблерах их не включает.
func TestLegal_EditDoesNotReenablePlaces(t *testing.T) {
	ctx := context.Background()
	a, _, _ := planAdminApp(t)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "старый текст"}}

	a.handleCallback(ctx, cb(planAdmin, "leg:tt"))
	a.handleMessage(ctx, msgText(planAdmin, "новый текст"))
	if a.botCfg.Legal.InMenu || a.botCfg.Legal.GateBuy {
		t.Fatalf("правка текста не должна включать показ: %+v", a.botCfg.Legal)
	}
	if a.botCfg.Legal.Terms.Text != "новый текст" {
		t.Fatalf("текст не сохранён: %q", a.botCfg.Legal.Terms.Text)
	}
}

// liveHas — есть ли среди НЕудалённых сообщений текст с подстрокой.
func liveHas(fm *fakeMsg, sub string) bool {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	for _, txt := range fm.live {
		if strings.Contains(txt, sub) {
			return true
		}
	}
	return false
}

// Открытие длинного документа не должно убивать экран согласия: части уходят
// обычными сообщениями, а не «экраном» (тот удаляет предыдущий).
func TestLegal_DocDoesNotEatConsentScreen(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(510)
	_ = fs.UpsertUser(ctx, uid)
	long := strings.TrimRight(strings.Repeat("строка документа\n", 600), "\n")
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: long}, GateBuy: true}

	a.askLegal(ctx, uid)
	a.handleCallback(ctx, cb(uid, "terms:doc_terms"))
	if !liveHas(fm, "Документы сервиса") {
		t.Fatal("экран согласия пропал при открытии документа")
	}
	if !liveHas(fm, "строка документа") {
		t.Fatal("части документа не дошли")
	}
}

// Одиночная «<» в тексте не должна уносить с собой остаток документа.
func TestLegal_TelegramHTMLEscaping(t *testing.T) {
	got := legalTelegramHTML("Возраст < 18 — отказ.\n<b>Пункт 2</b> & прочее <img src=x>")
	if !strings.Contains(got, "Возраст &lt; 18 — отказ.") {
		t.Fatalf("одиночная скобка не экранирована: %q", got)
	}
	if !strings.Contains(got, "<b>Пункт 2</b>") {
		t.Fatalf("разрешённый тег потерян: %q", got)
	}
	if !strings.Contains(got, "&amp; прочее") || !strings.Contains(got, "&lt;img src=x&gt;") {
		t.Fatalf("неразрешённая разметка не экранирована: %q", got)
	}
	if got2 := legalTelegramHTML("уже &amp; экранировано &#128512;"); !strings.Contains(got2, "&amp; экранировано &#128512;") {
		t.Fatalf("готовые сущности не должны экранироваться повторно: %q", got2)
	}
}

// Тег, разорванный границей частей, закрывается и открывается заново.
func TestLegal_PartsKeepTagsBalanced(t *testing.T) {
	body := "<b>" + strings.TrimRight(strings.Repeat("жирная строка\n", 500), "\n") + "</b>"
	parts := legalDocParts("ru", model.LegalItem{Kind: model.LegalTerms, Doc: model.LegalDoc{Text: body}})
	if len(parts) < 2 {
		t.Fatalf("ожидалось несколько частей: %d", len(parts))
	}
	for i, p := range parts {
		if strings.Count(p, "<b>") != strings.Count(p, "</b>") {
			t.Fatalf("часть %d с несбалансированным тегом: %q", i, p[:80])
		}
		if len([]rune(p)) > legalTextLimit+200 {
			t.Fatalf("часть %d длиннее лимита: %d", i, len([]rune(p)))
		}
	}
}

// Кириллический домен сохраняется в punycode — иначе кнопка ведёт в никуда.
func TestLegal_DocURLPunycode(t *testing.T) {
	got, ok := normalizeDocURL("https://мойсервис.рф/оферта")
	if !ok || !strings.HasPrefix(got, "https://xn--") {
		t.Fatalf("кириллический домен не переведён в punycode: %q ok=%v", got, ok)
	}
	for _, in := range []string{"https://example.com:8443/terms#p3", "https://example.com/a?b=1&c=2"} {
		if out, ok := normalizeDocURL(in); !ok || out != in {
			t.Fatalf("нормальная ссылка отвергнута: %q -> %q ok=%v", in, out, ok)
		}
	}
}

// Правку соглашения, сделанную старым образом после отката, новая версия
// принимает, а не затирает своим значением.
func TestLegal_AdoptsLegacyEdit(t *testing.T) {
	cfg := &model.BotConfig{}
	cfg.Contact.TermsText = "версия 1"
	cfg.NormalizeLegal()
	// Откат: старый образ переписал легаси-поле.
	cfg.Contact.TermsText = "версия 2"
	cfg.NormalizeLegal()
	if cfg.Legal.Terms.Text != "версия 2" {
		t.Fatalf("правка со старого образа потеряна: %q", cfg.Legal.Terms.Text)
	}
}

// Кнопка способа оплаты, пролежавшая в переписке, не проносит покупку мимо
// согласия.
func TestLegal_MethodButtonGated(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	uid := int64(511)
	_ = fs.UpsertUser(ctx, uid)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "правила"}, GateBuy: true}

	a.handleCallback(ctx, cb(uid, "method:bal"))
	if !strings.Contains(fm.last(), "правила") {
		t.Fatalf("способ оплаты должен упереться в согласие: %q", fm.last())
	}
}

// Длинная строка режется по безопасной границе: тег и HTML-сущность не
// разрываются пополам.
func TestLegal_SplitKeepsTagsWhole(t *testing.T) {
	long := strings.Repeat("а", legalTextLimit-200) + ` <a href="https://example.com/full">полный текст</a> ` + strings.Repeat("б", 300)
	parts := legalDocParts("ru", model.LegalItem{Kind: model.LegalTerms, Doc: model.LegalDoc{Text: long}})
	joined := strings.Join(parts, "\n")
	if !strings.Contains(joined, `<a href="https://example.com/full">полный текст</a>`) {
		t.Fatalf("ссылка разорвана резкой: %q", joined[len(joined)/2:])
	}
	for i, p := range parts {
		if strings.Count(p, "<a ") != strings.Count(p, "</a>") {
			t.Fatalf("часть %d с рваным тегом", i)
		}
		if len([]rune(p)) > 4096 {
			t.Fatalf("часть %d длиннее лимита Telegram: %d", i, len([]rune(p)))
		}
	}

	// Сущность тоже не должна рваться.
	ent := strings.Repeat("a", legalTextLimit-402) + "&amp;" + strings.Repeat("b", 400)
	for _, p := range legalDocParts("ru", model.LegalItem{Kind: model.LegalTerms, Doc: model.LegalDoc{Text: ent}}) {
		if strings.HasSuffix(p, "&a") || strings.HasPrefix(p, "mp;") {
			t.Fatalf("HTML-сущность разорвана: %q", p[:40])
		}
	}
}

// Документ с горой незакрытых тегов не должен раздувать части сверх лимита.
func TestLegal_BrokenMarkupStaysWithinLimit(t *testing.T) {
	body := strings.TrimRight(strings.Repeat("<i>пункт документа\n", 1200), "\n")
	parts := legalDocParts("ru", model.LegalItem{Kind: model.LegalTerms, Doc: model.LegalDoc{Text: body}})
	for i, p := range parts {
		if len([]rune(p)) > 4096 {
			t.Fatalf("часть %d длиннее лимита Telegram: %d", i, len([]rune(p)))
		}
	}
}
