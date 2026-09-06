package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// При «перевод всем без одобрения» реквизиты выдаются сразу, без заявки админу.
func TestP2POpenForAll(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.P2P = model.P2PConfig{Enabled: true, OpenForAll: true, Cards: []string{"0000 1111 2222 3333"}}
	a.botCfg.NormalizePricing()
	a.botCfg.Pricing.Base = map[int]string{1: "100"}
	ctx := context.Background()

	a.onBuyPlan(ctx, 200, "1")
	a.startP2P(ctx, 200)

	joined := strings.Join(fm.texts, "\n")
	if !strings.Contains(joined, "0000 1111 2222 3333") {
		t.Fatalf("ожидались реквизиты карты, получено: %q", joined)
	}
	if reqs := len(fs.reqs); reqs != 1 {
		t.Fatalf("ожидалась одна заявка на оплату, получено %d", reqs)
	}
}

// Без опции — прежнее поведение: нужен ручной допуск админа.
func TestP2PNeedsApprovalWhenClosed(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.P2P = model.P2PConfig{Enabled: true, Cards: []string{"0000"}}
	a.botCfg.NormalizePricing()
	ctx := context.Background()

	a.startP2P(ctx, 201)
	if strings.Contains(strings.Join(fm.texts, "\n"), "0000") {
		t.Fatal("без одобрения реквизиты выдавать нельзя")
	}
	if u, _ := fs.GetUser(ctx, 201); u != nil && u.P2PApproved {
		t.Fatal("пользователь не должен становиться одобренным сам по себе")
	}
}

// Одна незакрытая заявка на человека: повторные нажатия «Перевод на карту»
// возвращают ТУ ЖЕ заявку и ТЕ ЖЕ реквизиты, а конфиг на этом пути вообще не
// переписывается.
func TestP2PRequest_OneOpenPerUser(t *testing.T) {
	ctx := context.Background()
	a, _, fs := planAdminApp(t)
	const uid int64 = 777
	_ = fs.UpsertUser(ctx, uid)
	a.mu.Lock()
	a.botCfg.P2P = model.P2PConfig{Enabled: true, OpenForAll: true, Rotate: true,
		Cards: []string{"карта-1", "карта-2", "карта-3"}}
	a.mu.Unlock()

	card1, price1, id1, err := a.prepareP2PCard(ctx, uid, 1)
	if err != nil {
		t.Fatalf("первая заявка: %v", err)
	}
	for i := 0; i < 20; i++ {
		card, price, id, err := a.prepareP2PCard(ctx, uid, 1)
		if err != nil {
			t.Fatalf("повтор %d: %v", i, err)
		}
		if id != id1 || card != card1 || price != price1 {
			t.Fatalf("повтор %d создал новое: id=%d card=%q (было id=%d card=%q)", i, id, card, id1, card1)
		}
	}
	open := 0
	for _, r := range fs.reqs {
		if r.TelegramID == uid && (r.Status == model.P2PAwaiting || r.Status == model.P2PSubmitted) {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("незакрытых заявок: %d, ожидалась 1", open)
	}

	// После закрытия заявки следующая — новая, и карта сдвигается по ротации.
	r, _ := fs.GetP2PRequest(ctx, id1)
	r.Status = model.P2PApproved
	_ = fs.UpdateP2PRequest(ctx, r)
	card2, _, id2, err := a.prepareP2PCard(ctx, uid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatal("после закрытия заявки должна создаваться новая")
	}
	if card2 == card1 {
		t.Fatalf("ротация не сдвинулась: обе заявки на %q", card1)
	}
}

// Ротация карт крутится по кругу и не пишет конфиг.
func TestP2PCardRotation(t *testing.T) {
	a := &App{}
	cfg := model.P2PConfig{Rotate: true, Cards: []string{"a", "b", "c"}}
	got := make([]int, 7)
	for i := range got {
		got[i] = a.nextP2PCardIdx(cfg)
	}
	want := []int{0, 1, 2, 0, 1, 2, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ротация: получено %v, ожидалось %v", got, want)
		}
	}
	// Выключенная ротация и одна карта всегда дают первую.
	off := model.P2PConfig{Cards: []string{"a", "b"}}
	one := model.P2PConfig{Rotate: true, Cards: []string{"a"}}
	for i := 0; i < 3; i++ {
		if a.nextP2PCardIdx(off) != 0 || a.nextP2PCardIdx(one) != 0 {
			t.Fatal("без ротации всегда первая карта")
		}
	}
}

// /setup на настроенном боте ведёт в ПЕРЕустановку и не обнуляет настройки.
// Первичный мастер стартует с пустого конфига и пишет его поверх боевого
// полной заменой — на живой установке это стирало ключи платёжек, документы,
// триал, рефералку и делало закрытый бот публичным.
func TestSetupOnInstalledBot_DoesNotWipe(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	a.mu.Lock()
	if a.wiz == nil {
		a.wiz = map[int64]*wizard{}
	}
	a.botCfg.Installed = true
	a.botCfg.AccessMode = model.AccessWhitelist
	a.botCfg.YooKassa.ShopID = "shop-1"
	a.botCfg.YooKassa.SecretKey = "secret-1"
	a.botCfg.Trial.Enabled = true
	a.botCfg.Referral.Enabled = true
	a.mu.Unlock()

	a.handleMessage(ctx, msgText(planAdmin, "/setup"))

	a.mu.Lock()
	cfg := a.botCfg
	w := a.wiz[planAdmin]
	a.mu.Unlock()

	if w == nil {
		t.Fatal("мастер не запущен")
	}
	if !w.reconfig {
		t.Fatal("на настроенном боте /setup обязан вести в переустановку")
	}
	// Мастер работает с КОПИЕЙ живого конфига, а не с пустым.
	if w.cfg.YooKassa.ShopID != "shop-1" || w.cfg.YooKassa.SecretKey != "secret-1" {
		t.Fatalf("ключи платёжки не перенесены в мастер: %+v", w.cfg.YooKassa)
	}
	if !w.cfg.Trial.Enabled || !w.cfg.Referral.Enabled {
		t.Fatal("триал/рефералка не перенесены в мастер")
	}
	if w.cfg.AccessMode != model.AccessWhitelist {
		t.Fatalf("режим доступа не перенесён: %q", w.cfg.AccessMode)
	}
	// Живой конфиг не тронут.
	if cfg.YooKassa.ShopID != "shop-1" || cfg.AccessMode != model.AccessWhitelist {
		t.Fatal("живой конфиг изменён запуском мастера")
	}
	if !strings.Contains(fm.joined(), "ПЕРЕустановка") {
		t.Fatalf("админа не предупредили:\n%s", fm.joined())
	}
}

// Выключенный способ «перевод на карту» не выдаёт реквизиты — ни одобренным
// ранее, ни по режиму «всем без одобрения», ни в кабинете.
func TestP2PDisabled_NoCard(t *testing.T) {
	a := &App{botCfg: &model.BotConfig{}}
	approved := &model.User{TelegramID: 1, P2PApproved: true}

	a.botCfg.P2P = model.P2PConfig{Enabled: false, OpenForAll: true, Cards: []string{"карта"}}
	if a.p2pAllowed(approved) {
		t.Fatal("выключенный способ выдал реквизиты одобренному")
	}
	if a.p2pAllowed(&model.User{TelegramID: 2}) {
		t.Fatal("выключенный способ выдал реквизиты по режиму «всем»")
	}
	a.botCfg.P2P.Enabled = true
	if !a.p2pAllowed(approved) {
		t.Fatal("включённый способ обязан работать")
	}
}
