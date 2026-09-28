package app

import (
	"context"
	"strings"
	"testing"

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
		}, "<b>a<i>bc</i></b><i>d</i>"},
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
