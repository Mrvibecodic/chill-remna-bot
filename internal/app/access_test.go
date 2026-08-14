package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// В режиме «по приглашениям» новичок не проходит, а после активации кода —
// проходит, и повторно код не тратится.
func TestAccessInvite_RedeemGrantsAccess(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite, false)

	if !a.denyAccess(ctx, 500, false) {
		t.Fatal("в режиме приглашений посторонний не должен проходить")
	}
	inv := &model.Invite{Code: "abc123", MaxUses: 1}
	if err := fs.CreateInvite(ctx, inv); err != nil {
		t.Fatalf("создание приглашения: %v", err)
	}
	if msg, ok := a.redeemInvite(ctx, 500, "abc123"); !ok || msg == "" {
		t.Fatalf("активация приглашения: ok=%v msg=%q", ok, msg)
	}
	if a.denyAccess(ctx, 500, false) {
		t.Fatal("после активации приглашения доступ должен быть")
	}
	// Лимит исчерпан — второй пользователь не пройдёт.
	if _, ok := a.redeemInvite(ctx, 501, "abc123"); ok {
		t.Fatal("приглашение на 1 активацию не должно срабатывать дважды")
	}
	if !a.denyAccess(ctx, 501, false) {
		t.Fatal("второй пользователь без приглашения не должен проходить")
	}
}

func TestAccessInvite_ExpiredAndRevoked(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite, false)

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "old", MaxUses: 0, ExpiresAt: past})
	if _, ok := a.redeemInvite(ctx, 600, "old"); ok {
		t.Fatal("просроченное приглашение не должно активироваться")
	}

	_ = fs.CreateInvite(ctx, &model.Invite{Code: "rev", MaxUses: 5})
	_ = fs.RevokeInvite(ctx, "rev")
	if _, ok := a.redeemInvite(ctx, 601, "rev"); ok {
		t.Fatal("отозванное приглашение не должно активироваться")
	}
}

// Приглашение с неограниченным сроком и числом активаций работает многократно.
func TestAccessInvite_Unlimited(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite, false)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "many", MaxUses: 0})

	for _, id := range []int64{700, 701, 702} {
		if _, ok := a.redeemInvite(ctx, id, "many"); !ok {
			t.Fatalf("многоразовое приглашение должно срабатывать для %d", id)
		}
		if a.denyAccess(ctx, id, false) {
			t.Fatalf("пользователь %d должен иметь доступ", id)
		}
	}
}

// Публичный режим никого не отсекает, а вайтлист-режим продолжает работать
// по-старому (legacy-флаг WhitelistMode).
func TestAccessModes_PublicAndLegacyWhitelist(t *testing.T) {
	a, _ := refTestApp(t)
	ctx := context.Background()

	a.setAccessMode(ctx, model.AccessPublic, false)
	if a.denyAccess(ctx, 800, false) {
		t.Fatal("публичный режим должен пускать всех")
	}
	if a.botCfg.WhitelistMode {
		t.Fatal("в публичном режиме legacy-флаг должен быть выключен")
	}

	a.setAccessMode(ctx, model.AccessWhitelist, false)
	if !a.botCfg.WhitelistMode {
		t.Fatal("режим вайтлиста должен выставлять legacy-флаг")
	}
	if !a.denyAccess(ctx, 801, false) {
		t.Fatal("вайтлист должен отсекать посторонних")
	}
}

func TestNewInviteCode_Unique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		c := newInviteCode()
		if c == "" || len(c) != inviteCodeLen {
			t.Fatalf("некорректный код: %q", c)
		}
		if seen[c] {
			t.Fatalf("повтор кода приглашения: %q", c)
		}
		seen[c] = true
	}
}

// Полный путь: приглашённый открывает t.me/бот?start=inv_КОД — доступ должен
// выдаться ДО проверки режима публичности, иначе ссылка бесполезна.
func TestInviteViaStartCommand(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessInvite, false)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "linkcode", MaxUses: 1})

	a.handleMessage(ctx, msgText(900, "/start inv_linkcode"))
	if !a.accessGranted(ctx, 900) {
		t.Fatalf("после перехода по ссылке доступ должен быть выдан, сообщения: %v", fm.texts)
	}
	if a.denyAccess(ctx, 900, false) {
		t.Fatal("приглашённый должен проходить дальше")
	}

	// Неверный код: доступа нет, пользователь получает объяснение.
	a.handleMessage(ctx, msgText(901, "/start inv_нетакого"))
	if a.accessGranted(ctx, 901) {
		t.Fatal("по неверному коду доступ выдаваться не должен")
	}
}

// Регресс: колбэки экрана «Доступ» и автопродления должны реально роутиться
// в handleCallback, а не падать в «меню устарело».
func TestCallbackRouting_AccessAndAutoPay(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.YooKassa = model.YooKassaConfig{Enabled: true, ShopID: "s", SecretKey: "k", AutoPay: true}
	ctx := context.Background()

	// Закрытие публичного бота — в два шага: кнопка режима только спрашивает,
	// что делать с уже зарегистрированными, и сама режим не меняет.
	a.handleCallback(ctx, cb(100, "acc:mode:invite"))
	if a.accessMode() != model.AccessPublic {
		t.Fatalf("режим сменился без подтверждения: %q", a.accessMode())
	}
	a.handleCallback(ctx, cb(100, "acc:close:invite:all"))
	if a.accessMode() != model.AccessInvite {
		t.Fatalf("режим не переключился: %q", a.accessMode())
	}

	// Пользователь включает автопродление кнопкой из предложения.
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.SetWhitelisted(ctx, 555, true)
	_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: 555, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1})
	a.handleCallback(ctx, cb(555, "ap:on"))
	if !a.autoPayOn(ctx, 555) {
		t.Fatal("кнопка «включить автопродление» не сработала")
	}
	a.handleCallback(ctx, cb(555, "ap:off"))
	if a.autoPayOn(ctx, 555) {
		t.Fatal("кнопка «отключить автопродление» не сработала")
	}

	// Не-админ не может трогать экран доступа.
	a.handleCallback(ctx, cb(555, "acc:mode:public"))
	if a.accessMode() != model.AccessInvite {
		t.Fatal("обычный пользователь не должен менять режим публичности")
	}
}

// Закрытие ранее публичного бота сохраняет доступ действующим пользователям.
func TestAccess_GrandfathersExistingUsers(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 910)
	_ = fs.UpsertUser(ctx, 911)

	if n := a.setAccessMode(ctx, model.AccessInvite, true); n != 2 {
		t.Fatalf("ожидалось 2 сохранённых доступа, получено %d", n)
	}
	for _, id := range []int64{910, 911} {
		if a.denyAccess(ctx, id, false) {
			t.Fatalf("действующий пользователь %d не должен терять доступ", id)
		}
	}
	if !a.denyAccess(ctx, 912, false) {
		t.Fatal("новый пользователь без приглашения проходить не должен")
	}
}

// Приглашения действуют только в своём режиме, забаненный активацию не тратит.
func TestInvite_ModeAndBlocked(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessWhitelist, false)
	_ = fs.CreateInvite(ctx, &model.Invite{Code: "old1", MaxUses: 1})

	if _, ok := a.redeemInvite(ctx, 920, "old1"); ok {
		t.Fatal("в режиме вайтлиста старое приглашение работать не должно")
	}
	if a.accessGranted(ctx, 920) {
		t.Fatal("доступ выдан мимо режима")
	}

	a.setAccessMode(ctx, model.AccessInvite, false)
	_ = fs.UpsertUser(ctx, 921)
	_ = fs.SetBlocked(ctx, 921, true)
	if _, ok := a.redeemInvite(ctx, 921, "old1"); ok {
		t.Fatal("забаненный не должен активировать приглашение")
	}
	if inv, _ := fs.GetInvite(ctx, "old1"); inv.Used != 0 {
		t.Fatalf("активация не должна тратиться забаненным: used=%d", inv.Used)
	}
	// Код в другом регистре и с пробелами всё равно принимается.
	if _, ok := a.redeemInvite(ctx, 922, " OLD1 "); !ok {
		t.Fatal("код должен нормализоваться (регистр/пробелы)")
	}
}

// Мини-апп и веб-кабинет обязаны уважать режим публичности.
func TestMiniAccessDenied(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()

	if a.MiniAccessDenied(ctx, 930) {
		t.Fatal("в публичном режиме мини-апп должен пускать всех")
	}
	a.setAccessMode(ctx, model.AccessInvite, false)
	if !a.MiniAccessDenied(ctx, 930) {
		t.Fatal("в закрытом режиме посторонний не должен попадать в мини-апп")
	}
	if a.MiniAccessDenied(ctx, a.cfg.AdminID) {
		t.Fatal("админ проходит всегда")
	}
	_ = fs.UpsertUser(ctx, 930)
	_ = fs.SetWhitelisted(ctx, 930, true)
	if a.MiniAccessDenied(ctx, 930) {
		t.Fatal("впущенный пользователь должен проходить")
	}
}

// Откат на старую версию бота не должен открывать закрытый бот всем.
func TestNormalizeAccess_FailsClosed(t *testing.T) {
	// Так режим ставит сам бот (setAccessMode): режим + legacy-флаг «закрыт».
	cfg := &model.BotConfig{AccessMode: model.AccessInvite, WhitelistMode: true}
	cfg.NormalizeAccess()
	if cfg.AccessMode != model.AccessInvite || !cfg.WhitelistMode {
		t.Fatalf("режим приглашений должен сохраняться и помечать бота закрытым: %q/%v", cfg.AccessMode, cfg.WhitelistMode)
	}
	// Старая версия перезаписала конфиг, поле access_mode потеряно.
	old := &model.BotConfig{WhitelistMode: true}
	old.NormalizeAccess()
	if old.AccessMode != model.AccessWhitelist {
		t.Fatalf("после отката бот должен остаться закрытым, получено %q", old.AccessMode)
	}
	// Админ выключил вайтлист в старой версии — уважаем.
	opened := &model.BotConfig{AccessMode: model.AccessInvite, WhitelistMode: false}
	opened.NormalizeAccess()
	if opened.AccessMode != model.AccessPublic {
		t.Fatalf("выключенный в старой версии вайтлист должен открывать бота, получено %q", opened.AccessMode)
	}
}

// E-mail-аккаунты кабинета (отрицательный id) в закрытом боте: одобренные
// админом работают как до апгрейда, неодобренные идут в очередь на одобрение,
// а не получают глухой отказ «доступ ограничен».
func TestClosedBot_EmailCabinetAccounts(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.setAccessMode(ctx, model.AccessWhitelist, false)

	const approved, pending = int64(-100), int64(-101)
	_ = fs.UpsertUser(ctx, approved)
	_ = fs.SetWebApproved(ctx, approved, true)
	_ = fs.UpsertUser(ctx, pending)

	if a.MiniAccessDenied(ctx, approved) {
		t.Fatal("одобренный e-mail-аккаунт должен проходить в закрытом боте")
	}
	if err := a.CabinetGate(ctx, approved, true); err != nil {
		t.Fatalf("одобренный e-mail-аккаунт: %v", err)
	}
	if !a.MiniAccessDenied(ctx, pending) {
		t.Fatal("неодобренный e-mail-аккаунт не должен проходить в API закрытого бота")
	}
	err := a.CabinetGate(ctx, pending, true)
	if err == nil {
		t.Fatal("неодобренный e-mail-аккаунт должен ждать одобрения")
	}
	if errors.Is(err, errCabinetAccess) {
		t.Fatal("ожидалась очередь на одобрение, а не глухой отказ")
	}

	// В публичном боте без политики модерации e-mail проходит как раньше.
	a.setAccessMode(ctx, model.AccessPublic, false)
	if err := a.CabinetGate(ctx, pending, true); err != nil {
		t.Fatalf("публичный бот: %v", err)
	}
}

// Экран админки ЮKassa не должен содержать несовпавших плейсхолдеров
// (регресс: в шаблоне оставался лишний %s после выпиливания настройки).
func TestYKAdminScreenPlaceholders(t *testing.T) {
	for _, lang := range []string{model.LangRU, model.LangEN} {
		got := i18n.T(lang, "admin.yk_auto_block", "on", 3, 7)
		if strings.Contains(got, "%!") || strings.Contains(got, "MISSING") {
			t.Fatalf("битый шаблон admin.yk_auto_block (%s): %q", lang, got)
		}
	}
}

// Закрытие бота без явного согласия доступ никому не выдаёт. Раньше это
// делалось молча, и на импортированной базе закрытие открывало бота всем.
func TestAccess_CloseWithoutGrandfather(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 920)
	_ = fs.UpsertUser(ctx, 921)

	if n := a.setAccessMode(ctx, model.AccessWhitelist, false); n != 0 {
		t.Fatalf("доступ выдан без согласия: %d", n)
	}
	for _, id := range []int64{920, 921} {
		if !a.denyAccess(ctx, id, false) {
			t.Fatalf("пользователь %d не должен иметь доступ", id)
		}
	}
}

// Кнопка режима на публичном боте только спрашивает; выдача доступа зависит
// от того, что выбрал админ.
func TestAccess_CloseAsksAndHonoursChoice(t *testing.T) {
	for _, tc := range []struct {
		data  string
		keeps bool
	}{
		{"acc:close:whitelist:keep", true},
		{"acc:close:whitelist:all", false},
	} {
		a, _, fs := newTestApp(t)
		a.store = fs
		a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
		ctx := context.Background()
		_ = fs.UpsertUser(ctx, 930)

		a.handleCallback(ctx, cb(100, "acc:mode:whitelist"))
		if a.accessMode() != model.AccessPublic {
			t.Fatalf("%s: режим сменился до подтверждения", tc.data)
		}
		a.handleCallback(ctx, cb(100, tc.data))
		if a.accessMode() != model.AccessWhitelist {
			t.Fatalf("%s: режим не сменился", tc.data)
		}
		if got := !a.denyAccess(ctx, 930, false); got != tc.keeps {
			t.Fatalf("%s: доступ = %v, ожидалось %v", tc.data, got, tc.keeps)
		}
	}
}

// Массовое снятие доступа: обратная операция к выдаче всей базе. Админ мимо
// проверки ходит в любом случае.
func TestAccess_ClearAll(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 940)
	_ = fs.UpsertUser(ctx, 941)
	a.setAccessMode(ctx, model.AccessWhitelist, true)

	a.handleCallback(ctx, cb(100, "usr:wlclearok"))
	for _, id := range []int64{940, 941} {
		if !a.denyAccess(ctx, id, false) {
			t.Fatalf("у %d доступ не снят", id)
		}
	}
	if a.denyAccess(ctx, a.cfg.AdminID, true) {
		t.Fatal("админ должен остаться в боте")
	}
}

// Экран белого списка показывает тех, у кого доступ есть на самом деле, а не
// только предзаполненные ID (те опустошаются при первом входе).
func TestAccess_WhitelistScreenShowsGranted(t *testing.T) {
	a, msg, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 950)
	_ = fs.SetWhitelisted(ctx, 950, true)

	a.handleCallback(ctx, cb(100, "usr:wllist"))
	var found bool
	for _, d := range msg.cbData {
		if d == "usr:view:950" {
			found = true
		}
	}
	if !found {
		t.Fatalf("пользователь с доступом не показан: %v", msg.cbData)
	}
}
