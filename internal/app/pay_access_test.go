package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

func payAccessApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, fm, fs := planAdminApp(t)
	c := a.botCfg
	c.Stars.Enabled = true
	c.YooKassa.Enabled = true
	c.CryptoBot.Enabled = true
	c.Tribute.Enabled = true
	return a, fm, fs
}

func sentCB(fm *fakeMsg, data string) bool {
	return hasCB(fm.allCallbackData(), data)
}

// resetSent — забыть отправленное, чтобы проверять только следующий шаг.
func resetSent(fm *fakeMsg) {
	fm.mu.Lock()
	fm.texts, fm.cbData = nil, nil
	fm.mu.Unlock()
}

// Экран «Доступность»: кнопка из «Способов оплаты», галочки по местам, Stars
// на сайт не попадают, чужие нажатия ничего не меняют.
func TestPayAccess_AdminScreens(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := payAccessApp(t)

	a.handleCallback(ctx, cb(planAdmin, "menu:paymethods"))
	if !sentCB(fm, "pacc:home") {
		t.Fatal("в «Способах оплаты» нет кнопки «Доступность»")
	}
	a.handleCallback(ctx, cb(planAdmin, "pacc:home"))
	if !sentCB(fm, "pacc:p:tg") || !sentCB(fm, "pacc:p:web") {
		t.Fatal("на экране доступности нет мест")
	}
	a.handleCallback(ctx, cb(planAdmin, "pacc:p:web"))
	if sentCB(fm, "pacc:t:web:stars") || !sentCB(fm, "pacc:t:web:yookassa") {
		t.Fatalf("список сайта неверный: %v", fm.allCallbackData())
	}
	if sentCB(fm, "pacc:t:web:heleket") {
		t.Fatal("выключенный способ попал в список")
	}

	steps := []struct {
		name     string
		uid      int64
		data     string
		tgOff    []string
		webOff   []string
		wantNone bool
	}{
		{"снять ЮKassa с сайта", planAdmin, "pacc:t:web:yookassa", nil, []string{"yookassa"}, false},
		{"Stars на сайт не ставятся", planAdmin, "pacc:t:web:stars", nil, []string{"yookassa"}, false},
		{"чужое нажатие", 555, "pacc:t:tg:cryptobot", nil, []string{"yookassa"}, false},
		{"неизвестный способ", planAdmin, "pacc:t:tg:bogus", nil, []string{"yookassa"}, false},
		{"снять CryptoBot с сайта", planAdmin, "pacc:t:web:cryptobot", nil, []string{"yookassa", "cryptobot"}, false},
		{"снять Tribute с сайта — больше нечем платить", planAdmin, "pacc:t:web:tribute", nil, []string{"yookassa", "cryptobot", "tribute"}, true},
		{"вернуть ЮKassa", planAdmin, "pacc:t:web:yookassa", nil, []string{"cryptobot", "tribute"}, false},
		{"снять Stars с Telegram", planAdmin, "pacc:t:tg:stars", []string{"stars"}, []string{"cryptobot", "tribute"}, false},
	}
	none := i18n.T("ru", "pacc.none")
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			resetSent(fm)
			a.handleCallback(ctx, cb(s.uid, s.data))
			pa := a.botCfg.PayAccess
			if !slices.Equal(pa.TelegramOff, s.tgOff) || !slices.Equal(pa.WebOff, s.webOff) {
				t.Fatalf("галочки: tg=%v web=%v, ждали tg=%v web=%v", pa.TelegramOff, pa.WebOff, s.tgOff, s.webOff)
			}
			if s.uid == planAdmin && strings.Contains(fm.joined(), none) != s.wantNone {
				t.Fatalf("предупреждение «нельзя оплатить»: ждали %v\n%s", s.wantNone, fm.joined())
			}
		})
	}
}

// Чат — это Telegram: снятый способ пропадает с экрана оплаты, а старая кнопка
// не выставляет счёт, а показывает свежий список.
func TestPayAccess_ChatMethods(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := payAccessApp(t)
	a.botCfg.PayAccess.TelegramOff = []string{model.PayMethodYooKassa}
	a.botCfg.PayAccess.WebOff = []string{model.PayMethodCryptoBot}
	p := vipPlan(t, fs, model.PlanAvailAll)
	const uid int64 = 1000000001
	_ = fs.UpsertUser(ctx, uid)

	a.showMethodsSale(ctx, uid, &sale{Plan: p, D: p.Duration(1), Months: 1})
	if sentCB(fm, "method:yk") {
		t.Fatal("ЮKassa, снятая с Telegram, видна в чате")
	}
	if !sentCB(fm, "method:cb") || !sentCB(fm, "method:stars") {
		t.Fatalf("снятый только с сайта способ пропал из чата: %v", fm.allCallbackData())
	}

	if err := a.setBuyIntent(ctx, uid, p.Code, 1, "990"); err != nil {
		t.Fatal(err)
	}
	resetSent(fm)
	a.handleCallback(ctx, cb(uid, "method:yk"))
	if strings.Contains(fm.joined(), i18n.T("ru", "yk.not_configured")) {
		t.Fatal("старая кнопка ЮKassa дошла до создания счёта")
	}
	if !sentCB(fm, "method:cb") {
		t.Fatalf("вместо счёта ждали свежий список способов:\n%s", fm.joined())
	}
}

// Мини-апп и сайт: свои списки, отказ в счёте снятым способом, пополнение
// по тем же галочкам; Tribute на сайте — только с Telegram.
func TestPayAccess_MiniAndWeb(t *testing.T) {
	ctx := context.Background()
	a, _, fs := payAccessApp(t)
	a.botCfg.P2P.Enabled, a.botCfg.P2P.Cards = true, []string{"0000"}
	a.botCfg.PayAccess.TelegramOff = []string{model.PayMethodCryptoBot}
	a.botCfg.PayAccess.WebOff = []string{model.PayMethodYooKassa}
	p := vipPlan(t, fs, model.PlanAvailAll)
	const uid int64 = 1000000002
	_ = fs.UpsertUser(ctx, uid)

	tg := a.MiniMenu(ctx, uid, false).PayMethods
	if !slices.Contains(tg, model.PayMethodYooKassa) || slices.Contains(tg, model.PayMethodCryptoBot) || !slices.Contains(tg, model.PayMethodStars) {
		t.Fatalf("мини-апп: %v", tg)
	}
	web := a.MiniMenu(ctx, uid, true).PayMethods
	if slices.Contains(web, model.PayMethodYooKassa) || !slices.Contains(web, model.PayMethodCryptoBot) || slices.Contains(web, model.PayMethodStars) {
		t.Fatalf("сайт: %v", web)
	}
	if !slices.Contains(web, model.PayMethodTribute) {
		t.Fatalf("Tribute пропал у аккаунта с Telegram: %v", web)
	}
	if mail := a.MiniMenu(ctx, -5, true).PayMethods; slices.Contains(mail, model.PayMethodTribute) {
		t.Fatalf("Tribute предложен аккаунту по почте без Telegram: %v", mail)
	}

	want := i18n.T("ru", "mini.pay_method")
	for _, c := range []struct {
		method string
		web    bool
	}{
		{model.PayMethodYooKassa, true},
		{model.PayMethodCryptoBot, false},
		{model.PayMethodStars, true},
	} {
		r := a.MiniCheckout(ctx, uid, p.Code, 1, c.method, "990", c.web, false)
		if r.Error != want {
			t.Errorf("%s (сайт=%v): ждали отказ «%s», получили %+v", c.method, c.web, want, r)
		}
	}

	if m := a.MiniTopUpOptions(ctx, uid, true).Methods; slices.Contains(m, "yk") || !slices.Contains(m, "cb") {
		t.Fatalf("пополнение на сайте: %v", m)
	}
	if m := a.MiniTopUpOptions(ctx, uid, false).Methods; !slices.Contains(m, "yk") || slices.Contains(m, "cb") {
		t.Fatalf("пополнение в мини-аппе: %v", m)
	}
	if _, _, err := a.topUpCreate(ctx, uid, 10000, "yk", true); err == nil || !strings.Contains(err.Error(), i18n.T("ru", "topup.unavailable")) {
		t.Fatalf("пополнение на сайте снятым способом не отклонено галочкой: %v", err)
	}
	if r := a.MiniTopUp(ctx, uid, 10000, "cb", false); r.Error == "" {
		t.Fatal("пополнение в мини-аппе снятым способом прошло")
	}
}

// Счёт, выставленный до снятия галочки, оплачивается: предпроверка Stars
// галочки не читает.
func TestPayAccess_IssuedInvoiceStillPaid(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := payAccessApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink)
	const uid int64 = 720

	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	a.handleCallback(ctx, cb(uid, "method:stars"))
	if len(fm.invoices) != 1 {
		t.Fatalf("счёт Stars не выставлен: %+v", fm.invoices)
	}
	a.handleCallback(ctx, cb(planAdmin, "pacc:t:tg:stars"))
	if !slices.Contains(a.botCfg.PayAccess.TelegramOff, model.PayMethodStars) {
		t.Fatal("Stars не сняты с Telegram")
	}
	a.handlePreCheckout(ctx, &models.PreCheckoutQuery{ID: "q1", InvoicePayload: "stars:1", TotalAmount: 55, From: &models.User{ID: uid}})
	if !fm.lastPreOK() {
		t.Fatal("выставленный счёт Stars отклонён после снятия галочки")
	}
}

// Конфиг без настройки (до обновления) — всё везде, кроме Stars на сайте.
func TestPayAccess_OldConfigShowsAll(t *testing.T) {
	c, err := model.ConfigFromJSON([]byte(`{"installed":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range payMethodOrder {
		if !payAccessLocked(c, m, false) {
			t.Errorf("%s скрыт в Telegram", m)
		}
		if got := payAccessLocked(c, m, true); got != (m != model.PayMethodStars) {
			t.Errorf("%s на сайте: %v", m, got)
		}
	}
}

// Пополнение в чате — это Telegram: снятый способ пропадает со списка, а старая
// кнопка не выставляет счёт.
func TestPayAccess_ChatTopUp(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := payAccessApp(t)
	a.botCfg.PayAccess.TelegramOff = []string{model.PayMethodYooKassa}
	const uid int64 = 1000000003
	_ = fs.UpsertUser(ctx, uid)
	a.getUI(uid).topUpKopecks = 10000

	a.showTopUpMethods(ctx, uid)
	if sentCB(fm, "top:m:yk") || !sentCB(fm, "top:m:cb") {
		t.Fatalf("список пополнения в чате: %v", fm.allCallbackData())
	}
	resetSent(fm)
	a.handleCallback(ctx, cb(uid, "top:m:yk"))
	if !strings.Contains(fm.joined(), i18n.T("ru", "topup.unavailable")) {
		t.Fatalf("старая кнопка ЮKassa не отклонена:\n%s", fm.joined())
	}
}

// «Пополнить» и пустой кошелёк в меню пропадают там, где не осталось ни одного
// способа пополнения, и остаются в другом месте.
func TestPayAccess_TopUpEntryHidden(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := payAccessApp(t)
	// Способы пополнения: ЮKassa и CryptoBot. В Telegram оба сняты, на сайте
	// оба на месте.
	a.botCfg.PayAccess.TelegramOff = []string{model.PayMethodYooKassa, model.PayMethodCryptoBot}
	p := vipPlan(t, fs, model.PlanAvailAll)
	const uid int64 = 1000000004
	_ = fs.UpsertUser(ctx, uid)

	steps := []struct {
		name string
		do   func()
	}{
		{"экран оплаты", func() { a.showMethodsSale(ctx, uid, &sale{Plan: p, D: p.Duration(1), Months: 1}) }},
		{"кошелёк", func() { a.handleCallback(ctx, cb(uid, "menu:balance")) }},
		{"главное меню", func() { a.handleCallback(ctx, cb(uid, "menu:home")) }},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			resetSent(fm)
			s.do()
			if sentCB(fm, "menu:topup") {
				t.Fatalf("«Пополнить» видна без способов пополнения в Telegram: %v", fm.allCallbackData())
			}
			if s.name == "главное меню" && sentCB(fm, "menu:balance") {
				t.Fatal("пустой кошелёк в меню без пополнения в Telegram")
			}
		})
	}
	resetSent(fm)
	a.handleCallback(ctx, cb(uid, "menu:topup"))
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "top:") {
			t.Fatalf("старая кнопка «Пополнить» открыла пополнение: %v", fm.allCallbackData())
		}
	}
	if !sentCB(fm, "menu:buy") {
		t.Fatalf("старая кнопка «Пополнить» должна вести в кошелёк: %v", fm.allCallbackData())
	}

	if a.MiniMenu(ctx, uid, false).TopUpOn || a.MiniWallet(ctx, uid, false).TopUpOn {
		t.Fatal("мини-апп показывает пополнение без способов в Telegram")
	}
	if !a.MiniMenu(ctx, uid, true).TopUpOn || !a.MiniWallet(ctx, uid, true).TopUpOn {
		t.Fatal("на сайте пополнение пропало, хотя способы там есть")
	}

	// Вернули способ в Telegram — «Пополнить» на месте.
	a.botCfg.PayAccess.TelegramOff = []string{model.PayMethodYooKassa}
	resetSent(fm)
	a.handleCallback(ctx, cb(uid, "menu:balance"))
	if !sentCB(fm, "menu:topup") {
		t.Fatalf("«Пополнить» пропала при оставшемся способе: %v", fm.allCallbackData())
	}
}
