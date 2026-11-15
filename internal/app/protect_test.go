package app

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/config"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func TestCopyableText(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"обычный экран", "💼 <b>Кошелёк</b>\n\nНа счету: <b>1 500 ₽</b>", false},
		{"дата и время", "Подписка активна до 01.12.2030 03:00 МСК", false},
		{"премиум-эмодзи", `<tg-emoji emoji-id="5368324170671202286">👋</tg-emoji> Привет`, false},
		{"код", "Ваша ссылка:\n<code>abc</code>", true},
		{"ссылка тегом", `<a href="https://example.com">тут</a>`, true},
		{"адрес текстом", "Пишите на help.example.com", true},
		{"ссылка текстом", "https://sub.example.com/x", true},
		{"ник", "Поддержка: @support_bot", true},
		{"карта с пробелами", "Карта: 2200 1234 5678 9012", true},
		{"телефон", "СБП: +7 (999) 123-45-67", true},
		{"длинное число", "ID 1000000001", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := copyableText(c.text); got != c.want {
				t.Fatalf("copyableText(%q) = %v, ждали %v", c.text, got, c.want)
			}
		})
	}
}

func protectApp(t *testing.T) (*App, *fakeMsg) {
	t.Helper()
	srv := fullPanel(t)
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		wiz:   map[int64]*wizard{},
		ui:    map[int64]*uiState{},
		store: fs,
	}
	a.botCfg = fullyConfigured()
	a.botCfg.Referral = model.ReferralConfig{Enabled: true, BonusKind: "days", BonusValue: 3, Init: true}
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	_ = fs.UpsertUser(context.Background(), protUser)
	return a, fm
}

const protUser = int64(1000000001)

// cbUnder — нажатие кнопки под сообщением msgID, закрытым или открытым.
func cbUnder(uid int64, data string, msgID int, prot bool) *models.CallbackQuery {
	q := cb(uid, data)
	q.Message = models.MaybeInaccessibleMessage{Message: &models.Message{ID: msgID, HasProtectedContent: prot}}
	return q
}

// Экраны пользователя без того, что нужно копировать, уходят закрытыми;
// экраны со ссылками, админка и уведомления — открытыми.
func TestUserScreensProtect(t *testing.T) {
	ctx := context.Background()
	a, fm := protectApp(t)

	tap := func(uid int64, data string) int {
		t.Helper()
		n := fm.seq
		a.handleCallback(ctx, cb(uid, data))
		if fm.seq == n {
			t.Fatalf("%s: экран не пришёл", data)
		}
		return fm.seq
	}
	if id := tap(protUser, "menu:home"); !fm.isProt(id) {
		t.Fatal("главное меню пользователя ушло без запрета пересылки")
	}
	if id := tap(protUser, "menu:topup"); !fm.isProt(id) {
		t.Fatal("экран пополнения ушёл без запрета пересылки")
	}
	if id := tap(protUser, "menu:mysubs"); fm.isProt(id) {
		t.Fatal("экран подписки со ссылкой закрыт: ссылку не скопировать")
	}
	if id := tap(protUser, "menu:ref"); fm.isProt(id) {
		t.Fatal("реферальный экран закрыт: ссылку не скопировать")
	}
	// Экраны админки закрываются по разделам: меню, «Продажи», «Тарифы и
	// триал», «Обновление» — да. «Способы оплаты» и «Система» открыты, как и
	// большинство их экранов с ключами и адресами, карточка пользователя — тоже.
	for _, data := range []string{"menu:home", "menu:pay", "menu:payplans", "menu:analytics", "menu:updates"} {
		if id := tap(100, data); !fm.isProt(id) {
			t.Fatalf("%s у админа ушёл без запрета пересылки", data)
		}
	}
	for _, data := range []string{"menu:paymethods", "menu:system", "menu:status"} {
		if id := tap(100, data); fm.isProt(id) {
			t.Fatalf("%s у админа закрыт", data)
		}
	}
	if id := tap(100, "usr:view:"+strconv.FormatInt(protUser, 10)); fm.isProt(id) {
		t.Fatal("карточка пользователя у админа закрыта: ID не скопировать")
	}
	n := fm.seq
	a.notify(ctx, protUser, "уведомление без ссылок")
	if fm.seq == n || fm.isProt(fm.seq) {
		t.Fatal("уведомление ушло с запретом пересылки")
	}
}

// Запрет ставится только при отправке: экран другого режима не правит
// сообщение на месте, а уходит новым.
func TestProtectEditMatchesMode(t *testing.T) {
	ctx := context.Background()
	a, fm := protectApp(t)
	fm.editOK = true

	cases := []struct {
		name       string
		data       string
		targetProt bool
		wantEdit   bool
	}{
		{"закрытый экран под закрытым", "menu:topup", true, true},
		{"закрытый экран под открытым", "menu:topup", false, false},
		{"открытый экран под закрытым", "menu:mysubs", true, false},
		{"открытый экран под открытым", "menu:mysubs", false, true},
		{"текстовый экран под закрытым", "pr:enter", true, true},
		{"текстовый экран под открытым", "pr:enter", false, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target := 500 + i
			// Раздел экрана совпадает с прошлым: иначе баннер не правится
			// и без запрета.
			a.handleCallback(ctx, cb(protUser, c.data))
			edits, n := len(fm.edits), fm.seq
			a.handleCallback(ctx, cbUnder(protUser, c.data, target, c.targetProt))
			edited := len(fm.edits) > edits && fm.edits[len(fm.edits)-1] == target
			if edited != c.wantEdit {
				t.Fatalf("правка на месте: %v, ждали %v", edited, c.wantEdit)
			}
			if !c.wantEdit && fm.seq == n {
				t.Fatal("экран не ушёл новым сообщением")
			}
		})
	}
}

// Экран согласия с текстом документов остаётся открытым, даже если в тексте
// нет ни ссылок, ни кода.
func TestLegalAcceptScreenOpen(t *testing.T) {
	a, fm := protectApp(t)
	a.botCfg.Legal = model.LegalConfig{Terms: model.LegalDoc{Text: "Сервис оказывается как есть."}, GateBuy: true, Migrated: true}
	n := fm.seq
	a.handleCallback(context.Background(), cb(protUser, "menu:topup"))
	if fm.seq == n {
		t.Fatal("экран согласия не пришёл")
	}
	if !strings.Contains(fm.last(), "как есть") {
		t.Fatalf("пришёл не экран согласия с текстом: %q", fm.last())
	}
	if fm.isProt(fm.seq) {
		t.Fatal("экран согласия с текстом документа закрыт от копирования")
	}
}

// Разделы «Тарифы и триал» и «Названия и описания способов» закрыты от
// пересылки со всеми вложенными экранами — и после нажатия кнопки, и после
// ответа на вопрос. Ссылка на тариф и экраны самих способов оплаты (там карты
// и ключи) остаются открытыми.
func TestAdminNestedSalesScreensClosed(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	_ = fs.SavePlan(ctx, &model.Plan{Code: "family", Name: "Семья", Enabled: true, Currency: "₽",
		Availability: model.PlanAvailLink, Durations: []model.PlanDuration{{Months: 1, Base: "499"}}})
	a.botUser = "example_bot"
	tap := func(data string) bool {
		t.Helper()
		n := fm.seq
		a.handleCallback(ctx, cb(planAdmin, data))
		if fm.seq == n {
			t.Fatalf("%s: экран не пришёл", data)
		}
		return fm.isProt(fm.seq)
	}
	for _, data := range []string{
		"menu:plans", "pln:open:" + model.PlanCodeBase, "pln:pr:" + model.PlanCodeBase, "prc:quick",
		"menu:trial", "menu:squads", "menu:addsub", "tx:pm", "tx:k:method.yk_name", "trial:days",
	} {
		if !tap(data) {
			t.Errorf("%s ушёл без запрета пересылки", data)
		}
	}
	// Ответ на вопрос «сколько дней триала» — экран триала снова закрыт.
	n := fm.seq
	a.handleMessage(ctx, msgText(planAdmin, "5"))
	if fm.seq == n || !fm.isProt(fm.seq) {
		t.Error("экран после ввода дней триала ушёл без запрета пересылки")
	}
	if tap("pln:av:family") {
		t.Error("экран со ссылкой на тариф закрыт: ссылку не скопировать")
	}
	if tap("menu:p2p") {
		t.Error("экран P2P с картами закрыт")
	}
	// Цену задают и экраны самих способов оплаты: после ответа они остаются
	// открытыми, а из карточки тарифа — закрытыми.
	for _, data := range []string{"star:price:1", "yk:price:1", "adm:price:1"} {
		tap(data)
		n := fm.seq
		a.handleMessage(ctx, msgText(planAdmin, "100"))
		if fm.seq == n || fm.isProt(fm.seq) {
			t.Errorf("%s: экран способа оплаты после ввода цены закрыт", data)
		}
	}
	tap("pln:prm:1:" + model.PlanCodeBase)
	tap("pln:in:y:1:" + model.PlanCodeBase)
	n = fm.seq
	a.handleMessage(ctx, msgText(planAdmin, "100"))
	if fm.seq == n || !fm.isProt(fm.seq) {
		t.Error("экран тарифа после ввода цены ЮKassa открыт")
	}
}

// Подразделы «Системы», где копировать нечего, закрыты целиком: обновление и
// бэкап с его настройками и ответами на вопросы. Экраны с адресами, ключами и
// журналом запросов остаются открытыми.
func TestAdminSystemChildrenClosed(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := newTextsApp(t)
	tap := func(data string) bool {
		t.Helper()
		n := fm.seq
		a.handleCallback(ctx, cb(100, data))
		if fm.seq == n {
			t.Fatalf("%s: экран не пришёл", data)
		}
		return fm.isProt(fm.seq)
	}
	for _, data := range []string{"upd:chan", "menu:backup", "bk:dest", "bk:pass"} {
		if !tap(data) {
			t.Errorf("%s ушёл без запрета пересылки", data)
		}
	}
	n := fm.seq
	a.handleMessage(ctx, msgText(100, "a-long-enough-password"))
	if fm.seq == n || !fm.isProt(fm.seq) {
		t.Error("экран бэкапа после ввода пароля ушёл без запрета пересылки")
	}
	for _, data := range []string{"menu:apilog", "menu:webhooks", "menu:panelauth", "menu:miniapp", "menu:cabinet", "menu:mail"} {
		if tap(data) {
			t.Errorf("%s закрыт, а там адреса и ключи", data)
		}
	}
}

// Переход между экраном раздела и его экранами правит сообщение на месте, а
// не присылает новое: Telegram не даёт сменить запрет пересылки правкой,
// поэтому у раздела и его экранов режим один. Новое сообщение — только на
// входе в подраздел с другим режимом.
func TestAdminNavigationEditsInPlace(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := newTextsApp(t)
	fm.editOK = true
	_ = fs.UpsertUser(ctx, protUser)
	_ = fs.AddPayment(ctx, &model.Payment{ID: 5, TelegramID: protUser, Method: model.PayMethodP2P, Months: 1, Amount: "199 ₽",
		Status: model.PaymentPaid, CreatedAt: "2026-11-01T10:00:00Z", ExtID: "p2p:1"})
	id := strconv.FormatInt(protUser, 10)
	clusters := map[string][]string{
		"menu:pay":              {"menu:payments", "menu:analytics", "menu:payplans"},
		"menu:paymethods":       {"menu:p2p", "menu:stars", "menu:yookassa", "menu:cryptobot", "menu:platega", "menu:heleket", "menu:tribute", "menu:yoomoney", "menu:wallet", "menu:moynalog"},
		"menu:payplans":         {"menu:plans", "menu:pricing", "menu:trial", "menu:squads", "menu:addsub"},
		"menu:system":           {"menu:status", "menu:apilog", "menu:panelauth", "menu:webhooks", "menu:mail", "menu:miniapp", "menu:cabinet", "menu:rsimp", "menu:reconf"},
		"menu:payments":         {"pay:card:5:0", "pay:err"},
		"menu:updates":          {"upd:chan"},
		"menu:backup":           {"bk:dest"},
		"usr:view:" + id:        {"usr:pays:" + id + ":0", "usr:plans:" + id},
		"usr:pays:" + id + ":0": {"usr:pay:5:0"},
	}
	for hub, kids := range clusters {
		for _, kid := range kids {
			for _, p := range [][2]string{{hub, kid}, {kid, hub}} {
				a.handleCallback(ctx, cb(100, p[0]))
				msg := fm.seq
				seq, edits := fm.seq, len(fm.edits)
				a.handleCallback(ctx, cbUnder(100, p[1], msg, fm.isProt(msg)))
				if fm.seq != seq || len(fm.edits) == edits {
					t.Errorf("%s → %s: новое сообщение вместо правки", p[0], p[1])
				}
			}
		}
	}
}
