package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func TestEntitiesToHTML(t *testing.T) {
	cases := []struct {
		name string
		text string
		ents []models.MessageEntity
		want string
	}{
		{"экранирование", "a<b>&c", nil, "a&lt;b&gt;&amp;c"},
		{"жирный", "Привет мир", []models.MessageEntity{{Type: "bold", Offset: 0, Length: 6}}, "<b>Привет</b> мир"},
		{"вложенные", "abcd", []models.MessageEntity{
			{Type: "bold", Offset: 0, Length: 4}, {Type: "italic", Offset: 1, Length: 2},
		}, "<b>a<i>bc</i>d</b>"},
		{"пересекающиеся", "abcd", []models.MessageEntity{
			{Type: "bold", Offset: 0, Length: 3}, {Type: "italic", Offset: 1, Length: 3},
		}, "<b>a</b><i><b>bc</b>d</i>"},
		// Короткое внутри длинного: цитата не распадается на две.
		{"цитата длиннее жирного", "abcdef", []models.MessageEntity{
			{Type: "bold", Offset: 0, Length: 4}, {Type: "blockquote", Offset: 2, Length: 4},
		}, "<b>ab</b><blockquote><b>cd</b>ef</blockquote>"},
		// Эмодзи — две единицы UTF-16: смещения после него обязаны сойтись.
		{"эмодзи", "😀 x y", []models.MessageEntity{{Type: "code", Offset: 3, Length: 1}}, "😀 <code>x</code> y"},
		{"ссылка", "тут", []models.MessageEntity{{Type: "text_link", Offset: 0, Length: 3, URL: `https://e.test/?a=1&b="2"`}},
			`<a href="https://e.test/?a=1&amp;b=&quot;2&quot;">тут</a>`},
		{"pre с языком", "x:=1", []models.MessageEntity{{Type: "pre", Offset: 0, Length: 4, Language: "go"}},
			`<pre><code class="language-go">x:=1</code></pre>`},
		{"автоссылка не переносится", "e.test", []models.MessageEntity{{Type: "url", Offset: 0, Length: 6}}, "e.test"},
		{"премиум-эмодзи не переносятся", "😀", []models.MessageEntity{{Type: "custom_emoji", Offset: 0, Length: 2, CustomEmojiID: "5"}}, "😀"},
		{"цитата", "q", []models.MessageEntity{{Type: "expandable_blockquote", Offset: 0, Length: 1}}, "<blockquote expandable>q</blockquote>"},
		{"вне текста", "ab", []models.MessageEntity{{Type: "bold", Offset: 1, Length: 5}}, "ab"},
	}
	for _, c := range cases {
		if got := entitiesToHTML(c.text, c.ents); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVarLinks(t *testing.T) {
	in := `Документ: [Открыть полностью]({ссылка}) и [x](https://e.test)`
	out := varLinksToHTML(in)
	if out != `Документ: <a href="{ссылка}">Открыть полностью</a> и [x](https://e.test)` {
		t.Fatalf("got %q", out)
	}
	if back := varLinksToMarkup(out); back != in {
		t.Fatalf("обратно: %q", back)
	}
}

func newTextsApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	t.Cleanup(i18n.ResetOverrides)
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	return a, fm, fs
}

func adminMsg(text string, ents ...models.MessageEntity) *models.Message {
	m := msgText(100, text)
	m.Chat.Type = models.ChatTypePrivate
	m.Entities = ents
	return m
}

// Полный путь через диспетчер: карточка → «Изменить» → сообщение с
// форматированием и переменной → предпросмотр → «Сохранить» → пользователь
// видит новый текст → «Вернуть стандартный».
func TestTextEditFlow(t *testing.T) {
	a, fm, fs := newTextsApp(t)
	ctx := context.Background()
	const key = "buy.traffic"
	e, ok := i18n.EditableByKey(key)
	if !ok || len(e.Vars) != 1 {
		t.Fatalf("в каталоге нет %s с одной переменной", key)
	}
	ru := e.Vars[0].Ru

	a.handleCallback(ctx, cb(100, "menu:iface"))
	if !hasCB(fm.allCallbackData(), "tx:home") {
		t.Fatal("в «Интерфейсе» нет входа в тексты")
	}
	a.handleCallback(ctx, cb(100, "tx:home"))
	a.handleCallback(ctx, cb(100, "tx:k:"+key))
	if !strings.Contains(fm.joined(), "{"+ru+"}") {
		t.Fatalf("в карточке нет переменной {%s}", ru)
	}
	a.handleCallback(ctx, cb(100, "tx:e:"+key))
	if a.getUI(100).txtKey != key {
		t.Fatal("ожидание текста не взведено")
	}
	if !strings.Contains(fm.last(), "{"+ru+"}") {
		t.Fatalf("шаблон для копирования без переменной: %q", fm.last())
	}

	text := "📶 Лимит: {" + strings.ToUpper(ru) + "}!"
	a.handleMessage(ctx, adminMsg(text, models.MessageEntity{Type: "bold", Offset: 3, Length: 5}))
	want := "📶 <b>Лимит</b>: {" + e.Vars[0].Name + "}!"
	if got := a.getUI(100).txtDraft; got != want {
		t.Fatalf("черновик %q, want %q", got, want)
	}
	if !strings.Contains(fm.joined(), e.Vars[0].Example[0]) {
		t.Fatal("в предпросмотре нет примера значения")
	}
	if got := i18n.T("ru", key, "50 ГБ"); strings.Contains(got, "Лимит") {
		t.Fatal("текст применился до «Сохранить»")
	}

	a.handleCallback(ctx, cb(100, "tx:ok"))
	if got := i18n.T("ru", key, "50 ГБ"); got != "📶 <b>Лимит</b>: 50 ГБ!" {
		t.Fatalf("пользователь видит %q", got)
	}
	if fs.cfg == nil || fs.cfg.Texts.Overrides["ru"][key].Text != want {
		t.Fatal("текст не записан в конфиг")
	}
	if a.getUI(100).txtKey != "" || a.getUI(100).txtTplMsg != 0 {
		t.Fatal("ожидание не снято после сохранения")
	}

	a.handleCallback(ctx, cb(100, "tx:r:"+key))
	a.handleCallback(ctx, cb(100, "tx:ry:"+key))
	if got := i18n.T("ru", key, "50 ГБ"); strings.Contains(got, "Лимит") {
		t.Fatalf("после сброса пользователь видит свой текст: %q", got)
	}
	if _, ok := fs.cfg.Texts.Overrides["ru"][key]; ok {
		t.Fatal("сброс не записан в конфиг")
	}
}

func TestTextEditRejects(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()

	// Неизвестная переменная.
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	a.handleMessage(ctx, adminMsg("Трафик: {трафф}"))
	if a.getUI(100).txtDraft != "" || !strings.Contains(fm.joined(), "{трафф}") {
		t.Fatal("неизвестная переменная принята")
	}
	a.handleCallback(ctx, cb(100, "tx:ok"))
	if got := i18n.T("ru", "buy.traffic", "1"); strings.Contains(got, "трафф") {
		t.Fatal("текст с ошибкой сохранён")
	}

	// Кнопка: перенос строки и запрещённые символы.
	var btnKey string
	for _, e := range i18n.EditableAll() {
		if e.Kind == i18n.KindButton && len(e.Vars) == 0 {
			btnKey = e.Key
			break
		}
	}
	a.handleCallback(ctx, cb(100, "tx:e:"+btnKey))
	a.handleMessage(ctx, adminMsg("две\nстроки"))
	if a.getUI(100).txtDraft != "" {
		t.Fatal("кнопка с переносом принята")
	}
	a.handleMessage(ctx, adminMsg("a < b"))
	if a.getUI(100).txtDraft != "" {
		t.Fatal("кнопка с < принята")
	}
	a.handleMessage(ctx, adminMsg(strings.Repeat("я", 65)))
	if a.getUI(100).txtDraft != "" {
		t.Fatal("длинная кнопка принята")
	}
	a.handleMessage(ctx, adminMsg("Нормально"))
	if a.getUI(100).txtDraft != "Нормально" {
		t.Fatalf("годная кнопка отклонена: %q", fm.last())
	}

	// Предел платёжной системы.
	a.handleCallback(ctx, cb(100, "tx:e:stars.invoice_title"))
	a.handleMessage(ctx, adminMsg("Очень длинное название подписки на {месяцев} мес"))
	if a.getUI(100).txtDraft != "" {
		t.Fatal("название счёта длиннее 32 символов принято")
	}
}

func TestTextEditWarnsMissingVar(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	a.handleMessage(ctx, adminMsg("Без значения"))
	if a.getUI(100).txtDraft != "Без значения" {
		t.Fatal("текст без переменной не принят")
	}
	if !strings.Contains(fm.last(), "⚠️") {
		t.Fatalf("нет предупреждения о пропущенной переменной: %q", fm.last())
	}
}

// Текст, совпавший со стандартным, не хранится: иначе исправление
// стандартного текста в новой версии не дошло бы до пользователя.
func TestTextSameAsDefaultIsReset(t *testing.T) {
	a, _, fs := newTextsApp(t)
	ctx := context.Background()
	_ = a.setTextOverride(ctx, "ru", "buy.traffic", "свой {traffic}")
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	ui := a.getUI(100)
	ui.txtDraft = i18n.DefaultCanonical("ru", "buy.traffic")
	a.handleCallback(ctx, cb(100, "tx:ok"))
	if _, ok := fs.cfg.Texts.Overrides["ru"]["buy.traffic"]; ok {
		t.Fatal("стандартный текст сохранён как свой")
	}
}

// Кнопка другого раздела снимает ожидание текста: ответ на чужой вопрос не
// должен уйти в текст бота.
func TestTextEditCancelledByOtherButton(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	a.handleCallback(ctx, cb(100, "menu:iface"))
	if a.getUI(100).txtKey != "" {
		t.Fatal("ожидание текста пережило переход в другой раздел")
	}
	a.handleMessage(ctx, adminMsg("случайный текст"))
	if got := i18n.T("ru", "buy.traffic", "1"); strings.Contains(got, "случайный") {
		t.Fatal("текст ушёл в редактор после ухода из него")
	}
}

func TestTextsNotForUsers(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(555, "tx:e:buy.traffic"))
	if a.getUI(555).txtKey != "" {
		t.Fatal("пользователь открыл редактор текстов")
	}
}

func TestTextSearch(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:q"))
	a.handleMessage(ctx, adminMsg(i18n.EditableIn("buy")[0].Key))
	if !hasCB(fm.allCallbackData(), "tx:k:"+i18n.EditableIn("buy")[0].Key) {
		t.Fatal("поиск по ключу не нашёл текст")
	}
}

// Свои тексты применяются при загрузке конфига; устаревшие и сломанные
// видны админу и вызывают одно уведомление.
func TestTextsOnLoadAndNotice(t *testing.T) {
	a, fm, fs := newTextsApp(t)
	ctx := context.Background()
	fs.cfg = &model.BotConfig{Installed: true, Language: "ru", Texts: model.TextsConfig{
		Overrides: map[string]map[string]model.TextOverride{"ru": {
			"buy.traffic":     {Text: "Т: {traffic}", Base: "old"},
			"buy.devices":     {Text: "У: {gone_var}", Base: i18n.DefaultHash("ru", "buy.devices")},
			"admin.not_found": {Text: "подмена", Base: "x"},
		}},
	}}
	if err := a.loadConfigIfStore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := i18n.T("ru", "buy.traffic", "5 ГБ"); got != "Т: 5 ГБ" {
		t.Fatalf("свой текст не применён при загрузке: %q", got)
	}
	if got := i18n.T("ru", "admin.not_found"); got == "подмена" {
		t.Fatal("админский текст подменён через конфиг")
	}
	stale, broken := a.textsAttention("ru")
	if len(stale) != 1 || stale[0] != "buy.traffic" || len(broken) != 1 || broken[0] != "buy.devices" {
		t.Fatalf("stale=%v broken=%v", stale, broken)
	}
	before := len(fm.texts)
	a.sendTextsNotice(ctx)
	if len(fm.texts) != before+1 {
		t.Fatal("нет уведомления об устаревших текстах")
	}
	a.sendTextsNotice(ctx)
	if len(fm.texts) != before+1 {
		t.Fatal("уведомление повторилось")
	}
	a.handleCallback(ctx, cb(100, "tx:kp:buy.traffic"))
	if stale, _ := a.textsAttention("ru"); len(stale) != 0 {
		t.Fatal("«Оставить мой» не сверил текст")
	}
}

// Предел длины описания платежа проверяется по худшему случаю: «12 мес», а не
// пример «3 мес».
func TestTextPlainLimitWorstCase(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:stars.invoice_title"))
	// С примером «3» — ровно 32 символа, с длинным числом — больше.
	a.handleMessage(ctx, adminMsg("Подписка нашего сервиса на {месяцев} мес"))
	if a.getUI(100).txtDraft != "" {
		t.Fatal("название счёта принято без запаса на длинное число")
	}
}

// Кнопка «🏠» под полем ввода тоже снимает ожидание текста.
func TestTextEditCancelledByHomeKey(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	a.handleMessage(ctx, adminMsg(i18n.T("ru", "btn.home")))
	if a.getUI(100).txtKey != "" {
		t.Fatal("ожидание текста пережило «Главное меню»")
	}
}

// Правила вида текста действуют и при загрузке конфига: < в кнопке из
// поправленного руками конфига не применяется.
func TestTextKindCheckedOnLoad(t *testing.T) {
	a, _, _ := newTextsApp(t)
	a.applyTexts(&model.BotConfig{Texts: model.TextsConfig{Overrides: map[string]map[string]model.TextOverride{
		"ru": {"btn.buy": {Text: "<b>Купить</b>"}},
	}}})
	if got := i18n.T("ru", "btn.buy"); strings.Contains(got, "<b>") {
		t.Fatalf("разметка в кнопке применена: %q", got)
	}
}

// Пробелы и переносы по краям стандартного текста сохраняются: Telegram их
// обрезает, а у « и » они смысловые.
func TestTextKeepsEdgeWhitespace(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:legal.and"))
	a.handleMessage(ctx, adminMsg("и также"))
	if got := a.getUI(100).txtDraft; got != " и также " {
		t.Fatalf("края потеряны: %q", got)
	}
}

// Адресом ссылки может быть только переменная-адрес.
func TestTextLinkOnlyAddressVar(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:legal.read_full"))
	a.handleMessage(ctx, adminMsg("[Весь текст]({ссылка})"))
	if got := a.getUI(100).txtDraft; got != `<a href="{link}">Весь текст</a>` {
		t.Fatalf("ссылка на адрес не принята: %q", got)
	}
	a.handleCallback(ctx, cb(100, "tx:e:ap.on_title"))
	a.handleMessage(ctx, adminMsg("[Карта]({карта}) {срок} {сумма} {момент_списания}"))
	if a.getUI(100).txtDraft != "" {
		t.Fatal("ссылка на не-адрес принята")
	}
}

// Поиск снимает чужие ожидания ввода: запрос не должен уйти в приветствие.
func TestTextSearchDropsOtherInputs(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.getUI(100).welcomeAwait = "txt"
	a.handleCallback(ctx, cb(100, "tx:q"))
	a.handleMessage(ctx, adminMsg("покупка"))
	if a.botCfg.Welcome.Text != "" {
		t.Fatalf("запрос поиска ушёл в приветствие: %q", a.botCfg.Welcome.Text)
	}
	if a.getUI(100).txtQuery != "покупка" {
		t.Fatal("поиск не выполнен")
	}
}

// Переход к другому тексту внутри редактора снимает ожидание: присланный
// потом текст не должен сохраниться в прежний ключ.
func TestTextEditCancelledByEditorNavigation(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	a.handleCallback(ctx, cb(100, "tx:k:buy.devices"))
	if a.getUI(100).txtKey != "" {
		t.Fatal("ожидание пережило переход к другому тексту")
	}
}

// Текст, начинающийся с «/», пока редактор ждёт текст, — это текст.
func TestTextEditAcceptsSlash(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:cmd.support_none"))
	a.handleMessage(ctx, adminMsg("/support пока не работает"))
	if got := a.getUI(100).txtDraft; got != "/support пока не работает" {
		t.Fatalf("текст со «/» не принят: %q", got)
	}
}

type failSaveStore struct{ *fakeStore }

func (failSaveStore) SaveConfig(context.Context, *model.BotConfig) error {
	return errors.New("база недоступна")
}

// Не записался — не применился: иначе текст пропал бы после перезапуска.
func TestTextSaveFailureRollsBack(t *testing.T) {
	a, fm, fs := newTextsApp(t)
	a.store = failSaveStore{fs}
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:buy.traffic"))
	a.handleMessage(ctx, adminMsg("Свой {трафик}"))
	a.handleCallback(ctx, cb(100, "tx:ok"))
	if got := i18n.T("ru", "buy.traffic", "5 ГБ"); strings.Contains(got, "Свой") {
		t.Fatal("несохранённый текст применён")
	}
	if a.getUI(100).txtDraft == "" || !strings.Contains(fm.last(), "⚠️") {
		t.Fatal("админ не предупреждён или черновик потерян")
	}
	if _, ok := a.botCfg.Texts.Overrides["ru"]["buy.traffic"]; ok {
		t.Fatal("конфиг в памяти не откатан")
	}
}

// Недоставленное уведомление повторяется на следующем запуске.
func TestTextsNoticeRetriesAfterFailedDelivery(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.botCfg.Texts.Overrides = map[string]map[string]model.TextOverride{"ru": {"buy.traffic": {Text: "Т {traffic}", Base: "old"}}}
	a.applyTexts(a.botCfg)
	fm.kbFail = true
	a.sendTextsNotice(ctx)
	if a.botCfg.Texts.NotifiedStale != "" {
		t.Fatal("недоставленное уведомление помечено отправленным")
	}
	fm.kbFail = false
	a.sendTextsNotice(ctx)
	if a.botCfg.Texts.NotifiedStale == "" {
		t.Fatal("уведомление не отправлено повторно")
	}
}

// «Реквизиты не настроены» доходит до человека как есть, а не общей ошибкой.
func TestP2PNoCardsShownToUser(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	_, _, _, err := a.prepareP2PCard(ctx, 555, 1)
	if got := a.clientErr(ctx, 555, "перевод", err); got != i18n.T("ru", "p2p.no_cards") {
		t.Fatalf("пользователь видит %q", got)
	}
}

func TestDisplayNameFallbackByLang(t *testing.T) {
	if got := displayName("en", "", ""); got != "friend" {
		t.Fatalf("en: %q", got)
	}
	if got := displayName("ru", "", ""); got != "друг" {
		t.Fatalf("ru: %q", got)
	}
}

// Одно название способа — в кнопке чата, в списке мини-аппа и в описании.
func TestPayMethodNameAndNote(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.botCfg.YooKassa.Enabled = true
	before := i18n.T("ru", "method.yk_btn", methodName("ru", model.PayMethodYooKassa), "990 ₽")
	if before != "💳 Картой (ЮKassa) — 990 ₽" {
		t.Fatalf("кнопка по умолчанию изменилась: %q", before)
	}
	// Пока название не изменено, мини-апп показывает свои встроенные названия.
	if dto := a.MiniMenu(ctx, 555, false); len(dto.PayLabels) != 0 {
		t.Fatalf("без правки мини-апп получил названия: %v", dto.PayLabels)
	}
	if n := ownMethodName("ru", model.PayMethodTribute); n != "" {
		t.Fatalf("незаданное название считается своим: %q", n)
	}
	_ = a.setTextOverride(ctx, "ru", "method.yk_name", "Карты МИР, СБП")
	_ = a.setTextOverride(ctx, "ru", "method.yk_note", "Оплата <b>любой</b> картой")

	a.getUI(555).topUpKopecks = 50000
	a.showTopUpMethods(ctx, 555)
	found := false
	for _, l := range fm.buttonLabels() {
		if strings.Contains(l, "Карты МИР, СБП") {
			found = true
		}
	}
	if !found {
		t.Fatalf("новое название не на кнопке: %v", fm.buttonLabels())
	}
	if got := withPayNote("ru", model.PayMethodYooKassa, "Экран"); got != "Экран\n\nОплата <b>любой</b> картой" {
		t.Fatalf("описание не добавлено к экрану оплаты: %q", got)
	}
	if got := withPayNote("ru", model.PayMethodCryptoBot, "Экран"); got != "Экран" {
		t.Fatalf("пустое описание что-то добавило: %q", got)
	}
	dto := a.MiniMenu(ctx, 555, false)
	if dto.PayLabels[model.PayMethodYooKassa] != "Карты МИР, СБП" || dto.PayLabels["yk"] != "Карты МИР, СБП" {
		t.Fatalf("мини-апп не получил название: %v", dto.PayLabels)
	}
	if dto.PayNotes[model.PayMethodYooKassa] != "Оплата любой картой" {
		t.Fatalf("описание для страницы — без разметки: %v", dto.PayNotes)
	}
	if _, ok := dto.PayNotes[model.PayMethodCryptoBot]; ok {
		t.Fatal("пустое описание ушло в мини-апп")
	}
}

// Необязательный текст: пустой по умолчанию, правится и убирается.
func TestOptionalNoteEditAndClear(t *testing.T) {
	a, fm, fs := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:k:method.pl_note"))
	if !strings.Contains(fm.joined(), i18n.T("ru", "tx.st_empty")) {
		t.Fatal("карточка пустого описания не говорит, что оно пусто")
	}
	sent := len(fm.texts)
	a.handleCallback(ctx, cb(100, "tx:e:method.pl_note"))
	if a.getUI(100).txtTplMsg != 0 || len(fm.texts) != sent+1 {
		t.Fatal("для пустого текста отправлен шаблон")
	}
	a.handleMessage(ctx, adminMsg("Карты любых банков"))
	a.handleCallback(ctx, cb(100, "tx:ok"))
	if methodNote("ru", model.PayMethodPlatega) != "Карты любых банков" {
		t.Fatal("описание не сохранено")
	}
	if hasCB(fm.allCallbackData(), "tx:d:method.pl_note") {
		t.Fatal("у необязательного текста кнопка «Стандартный текст» вместо «Убрать»")
	}
	a.handleCallback(ctx, cb(100, "tx:r:method.pl_note"))
	a.handleCallback(ctx, cb(100, "tx:ry:method.pl_note"))
	if methodNote("ru", model.PayMethodPlatega) != "" {
		t.Fatal("описание не убрано")
	}
	if _, ok := fs.cfg.Texts.Overrides["ru"]["method.pl_note"]; ok {
		t.Fatal("убранное описание осталось в конфиге")
	}
}

// Экран «Способы оплаты»: вход из продаж и из текстов, «Назад» — туда же.
func TestPayMethodsScreen(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "menu:pay"))
	if !hasCB(fm.allCallbackData(), "tx:pm") {
		t.Fatal("в продажах нет входа в названия способов")
	}
	a.handleCallback(ctx, cb(100, "tx:pm"))
	cbs := fm.allCallbackData()
	for _, want := range []string{"tx:k:method.yk_name", "tx:k:method.yk_note", "tx:k:method.trb_name", "menu:pay"} {
		if !hasCB(cbs, want) {
			t.Fatalf("на экране нет %s", want)
		}
	}
	a.handleCallback(ctx, cb(100, "tx:k:method.yk_name"))
	if !hasCB(fm.allCallbackData(), "tx:pm:") {
		t.Fatal("из карточки не вернуться к способам оплаты")
	}
	a.handleCallback(ctx, cb(100, "tx:pm:t"))
	if !hasCB(fm.allCallbackData(), "tx:home") {
		t.Fatal("из текстов «Назад» не ведёт в тексты")
	}
}

// Кнопка со своим текстом без {способ_оплаты} не подхватит новое название —
// экран способов об этом предупреждает.
func TestPayMethodsScreenWarnsFixedButton(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	_ = a.setTextOverride(ctx, "ru", "method.pl_btn", "💠 Платёж — {amount}")
	a.handleCallback(ctx, cb(100, "tx:pm"))
	if !strings.Contains(fm.joined(), "{способ_оплаты}") {
		t.Fatal("нет предупреждения о кнопке со своим текстом")
	}
}

// Новое название приходит на все кнопки способов при покупке.
func TestPayMethodNamesOnPurchaseButtons(t *testing.T) {
	t.Cleanup(i18n.ResetOverrides)
	a, fs := snapApp(t, "http://127.0.0.1:1")
	fm := &fakeMsg{}
	a.msg = fm
	ctx := context.Background()
	c := a.botCfg
	c.P2P.Enabled, c.P2P.Cards = true, []string{"0000"}
	c.Stars.Enabled = true
	c.YooKassa.Enabled = true
	c.CryptoBot.Enabled = true
	c.Platega.Enabled = true
	c.Heleket.Enabled = true
	for _, m := range payMethodOrder {
		_ = a.setTextOverride(ctx, "ru", payMethodKeys[m].name, "Своё "+m)
	}
	_ = fs.UpsertUser(ctx, 555)
	p := vipPlan(t, fs, model.PlanAvailAll)
	a.showMethodsSale(ctx, 555, &sale{Plan: p, D: p.Duration(1), Months: 1})
	labels := strings.Join(fm.buttonLabels(), " | ")
	for _, m := range []string{model.PayMethodP2P, model.PayMethodStars, model.PayMethodYooKassa, model.PayMethodCryptoBot, model.PayMethodPlatega, model.PayMethodHeleket} {
		if !strings.Contains(labels, "Своё "+m) {
			t.Errorf("на кнопке %s нет нового названия: %s", m, labels)
		}
	}
}

type blockingSaveStore struct {
	*fakeStore
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (s *blockingSaveStore) SaveConfig(ctx context.Context, c *model.BotConfig) error {
	s.mu.Lock()
	s.calls++
	first := s.calls == 1
	s.mu.Unlock()
	if first {
		close(s.started)
		<-s.release
		return errors.New("база недоступна")
	}
	return s.fakeStore.SaveConfig(ctx, c)
}

// Откат неудачной записи не стирает правку, сохранённую в это же время.
func TestTextSaveRollbackKeepsConcurrentEdit(t *testing.T) {
	a, _, fs := newTextsApp(t)
	st := &blockingSaveStore{fakeStore: fs, started: make(chan struct{}), release: make(chan struct{})}
	a.store = st
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.setTextOverride(ctx, "ru", "buy.traffic", "A {traffic}") }()
	<-st.started
	go func() { defer wg.Done(); _ = a.setTextOverride(ctx, "ru", "btn.buy", "Купить B") }()
	time.Sleep(50 * time.Millisecond)
	close(st.release)
	wg.Wait()
	if got := i18n.T("ru", "btn.buy"); got != "Купить B" {
		t.Fatalf("успешная правка стёрта откатом соседней: %q", got)
	}
	if fs.cfg == nil || fs.cfg.Texts.Overrides["ru"]["btn.buy"].Text != "Купить B" {
		t.Fatal("успешная правка не в базе")
	}
	if got := i18n.T("ru", "buy.traffic", "5 ГБ"); strings.HasPrefix(got, "A ") {
		t.Fatal("неудачная правка применена")
	}
}

// «Отмена» в поиске возвращает в тексты, а не в главное меню админки.
func TestTextSearchCancelReturnsToTexts(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:q"))
	a.handleCallback(ctx, cb(100, "inp:cancel"))
	if !strings.Contains(fm.last(), "Тексты бота") {
		t.Fatalf("после отмены не экран текстов: %q", fm.last())
	}
}

// Правила места действуют и при загрузке: перенос в кнопке и длинное
// название счёта из конфига не применяются и видны как ⛔.
func TestTextKindRulesOnLoad(t *testing.T) {
	a, _, _ := newTextsApp(t)
	a.botCfg.Texts.Overrides = map[string]map[string]model.TextOverride{"ru": {
		"btn.buy":             {Text: "Купить\nсейчас"},
		"stars.invoice_title": {Text: strings.Repeat("я", 40) + " {months}"},
	}}
	a.applyTexts(a.botCfg)
	if got := i18n.T("ru", "btn.buy"); strings.Contains(got, "\n") {
		t.Fatal("перенос в кнопке применён")
	}
	if _, broken := a.textsAttention("ru"); len(broken) != 2 {
		t.Fatalf("недопустимые тексты не отмечены: %v", broken)
	}
}

// Знак препинания в начале не отрывается пробелом: «, » между названиями.
func TestTextEdgeWhitespaceWithPunctuation(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.handleCallback(ctx, cb(100, "tx:e:legal.and"))
	a.handleMessage(ctx, adminMsg(","))
	if got := a.getUI(100).txtDraft; got != ", " {
		t.Fatalf("got %q", got)
	}
}

// Запись [текст]({ссылка}) внутри кода остаётся текстом.
func TestVarLinkInsideCodeStaysText(t *testing.T) {
	in := `<code>[x]({ссылка})</code> [y]({ссылка})`
	want := `<code>[x]({ссылка})</code> <a href="{ссылка}">y</a>`
	if got := varLinksToHTML(in); got != want {
		t.Fatalf("got %q", got)
	}
}

// Без реквизитов мини-апп не зовёт «завершить оплату в чате».
func TestMiniP2PNoCards(t *testing.T) {
	a, _, fs := newTextsApp(t)
	ctx := context.Background()
	a.botCfg.P2P.Enabled = true
	a.botCfg.P2P.Cards = nil
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.SetP2PApproved(ctx, 555, true)
	dto := a.MiniP2P(ctx, 555, baseSale(1))
	if dto.Redirect || dto.Error == "" {
		t.Fatalf("got %+v", dto)
	}
}

// CryptoBot с валютой, которую он не принимает, не предлагается.
func TestCryptoBotHiddenForUnsupportedCurrency(t *testing.T) {
	a, _, _ := newTextsApp(t)
	ctx := context.Background()
	a.botCfg.CryptoBot.Enabled = true
	a.botCfg.Pricing.Currency = "CNY"
	for _, m := range a.MiniMenu(ctx, 555, false).PayMethods {
		if m == model.PayMethodCryptoBot {
			t.Fatal("CryptoBot предложен для CNY")
		}
	}
}
