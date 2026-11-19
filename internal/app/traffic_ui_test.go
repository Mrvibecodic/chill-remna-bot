package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func hasCallback(fm *fakeMsg, data string) bool {
	for _, row := range fm.lastKB {
		for _, b := range row {
			if b.CallbackData == data {
				return true
			}
		}
	}
	return false
}

func tap(a *App, data string) {
	a.handleCallback(context.Background(), cb(trafficUID, data))
}

// Витрина, карточка трафик-тарифа и покупка с баланса — через кнопки.
func TestTrafficUI_StorefrontCardAndBuy(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	fm := a.msg.(*fakeMsg)
	_ = fs.SavePlan(ctx, &model.Plan{Code: "term01", Name: "Стандарт", Enabled: true, Currency: "₽", Availability: model.PlanAvailAll,
		Durations: []model.PlanDuration{{Months: 1, Base: "199"}}})

	tap(a, "menu:buy")
	if !hasCallback(fm, "plo:"+plan.Code) {
		t.Fatalf("трафик-тарифа нет в витрине: %v", fm.lastKB)
	}
	found := false
	for _, row := range fm.lastKB {
		for _, b := range row {
			if b.CallbackData == "plo:"+plan.Code {
				found = strings.Contains(b.Text, "450 ₽ · бессрочно")
			}
		}
	}
	if !found {
		t.Fatalf("подпись трафик-тарифа в витрине: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)

	tap(a, "plo:"+plan.Code)
	card := fm.last()
	for _, want := range []string{"150 ГБ, без сброса", "Срок: бессрочно", "докупите пакет"} {
		if !strings.Contains(card, want) {
			t.Fatalf("на карточке нет %q: %s", want, card)
		}
	}
	if strings.Contains(card, "заменится") {
		t.Fatal("предупреждение о замене без действующей подписки")
	}
	if !hasCallback(fm, "tfk:buy:"+plan.Code) {
		t.Fatalf("нет кнопки покупки: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)

	tap(a, "tfk:buy:"+plan.Code)
	methods := fm.last()
	if !strings.Contains(methods, "Трафик-тариф «Трафик 150»") || !hasCallback(fm, "method:bal") {
		t.Fatalf("экран способов: %s %v", methods, fm.lastKB)
	}
	if hasCallback(fm, "method:trb") {
		t.Fatal("Tribute предложен для трафик-тарифа")
	}
	assertPhoneFit(t, fm.lastKB)
	tap(a, "method:bal")
	if exp, limit, _, _ := panel.state(); exp != remnawave.EternalExpire || limit != 150*gib {
		t.Fatalf("покупка кнопками: %s %d", exp, limit)
	}

	// Повторный показ карточки — с предупреждением: подписка уже идёт.
	tap(a, "plo:"+plan.Code)
	if !strings.Contains(fm.last(), "заменится") {
		t.Fatalf("нет предупреждения о замене: %s", fm.last())
	}
}

// Владелец: «Купить трафик» в главном меню и в «Мои подписки», пакеты,
// покупка пакета с баланса, бессрочный срок на экране подписки.
func TestTrafficUI_OwnerScreens(t *testing.T) {
	a, _, panel, plan := trafficDealApp(t)
	fm := a.msg.(*fakeMsg)
	buyTrafficWithBalance(t, a, plan.Code, "")

	tap(a, "menu:home")
	if !hasCallback(fm, cbTrafficPacks) || hasCallback(fm, "menu:renew") {
		t.Fatalf("главное меню владельца: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)

	tap(a, "menu:mysubs")
	subs := fm.last()
	if !strings.Contains(subs, "бессрочно") || strings.Contains(subs, "2099") || !strings.Contains(subs, "Сброс трафика: <b>нет") {
		t.Fatalf("экран подписки: %s", subs)
	}
	if !hasCallback(fm, cbTrafficPacks) {
		t.Fatalf("нет «Купить трафик» в подписке: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)

	panel.setUsed(100 * gib)
	tap(a, cbTrafficPacks)
	if !strings.Contains(fm.last(), "Осталось: <b>50 из 150 ГБ</b>") || !hasCallback(fm, "tfk:pb:pk5000") {
		t.Fatalf("экран пакетов: %s %v", fm.last(), fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)

	tap(a, "tfk:pb:pk5000")
	if !strings.Contains(fm.last(), "Пакет трафика + 50 ГБ") || !hasCallback(fm, cbTrafficPacks) {
		t.Fatalf("способы оплаты пакета: %s %v", fm.last(), fm.lastKB)
	}
	tap(a, "method:bal")
	if _, limit, used, _ := panel.state(); limit != 200*gib || used != 100*gib {
		t.Fatalf("пакет кнопками: лимит %d, расход %d", limit, used)
	}
	if !strings.Contains(fm.joined(), "Трафик добавлен: + 50 ГБ") {
		t.Fatal("нет сообщения о добавленном трафике")
	}

	// Старая кнопка «Продлить» ведёт к пакетам.
	tap(a, "menu:renew")
	if !hasCallback(fm, "tfk:pb:pk5000") {
		t.Fatalf("«Продлить» у трафик-тарифа: %s", fm.last())
	}
	// Чужой пакет и подделанный ID.
	tap(a, "tfk:pb:zzzzzz")
	if !strings.Contains(fm.joined(), "Этого пакета больше нет") {
		t.Fatal("подделанный пакет не отклонён")
	}
}

// Исчерпанный трафик: в подписке и в уведомлении — «Купить трафик».
func TestTrafficUI_LimitedAndNotices(t *testing.T) {
	a, _, panel, plan := trafficDealApp(t)
	fm := a.msg.(*fakeMsg)
	ctx := context.Background()
	buyTrafficWithBalance(t, a, plan.Code, "")
	panel.mu.Lock()
	panel.used = 150 * gib
	panel.mu.Unlock()

	if !a.pushTrafficLimited(ctx, rwUserPayload{TelegramID: trafficUID}) {
		t.Fatal("уведомление не доставлено")
	}
	if !strings.Contains(fm.last(), "Докупите пакет") || !hasCallback(fm, cbTrafficPacks) {
		t.Fatalf("уведомление об исчерпании: %s %v", fm.last(), fm.lastKB)
	}
	a.pushTrafficThreshold(ctx, rwUserPayload{TelegramID: trafficUID, LastTriggeredThreshold: 80})
	if !hasCallback(fm, cbTrafficPacks) || !hasCallback(fm, "menu:mysubs") {
		t.Fatalf("уведомление о пороге: %v", fm.lastKB)
	}
	assertPhoneFit(t, fm.lastKB)
}

// Не владелец: пакеты не показываются, кнопки в меню нет.
func TestTrafficUI_NotOwner(t *testing.T) {
	a, _, _, _ := trafficDealApp(t)
	fm := a.msg.(*fakeMsg)
	tap(a, "menu:home")
	if hasCallback(fm, cbTrafficPacks) {
		t.Fatal("«Купить трафик» у человека без трафик-тарифа")
	}
	tap(a, cbTrafficPacks)
	if !strings.Contains(fm.last(), "на тарифе «Трафик»") {
		t.Fatalf("экран пакетов не владельцу: %s", fm.last())
	}
	if formatExpire(remnawave.EternalExpire, "ru") != "бессрочно" {
		t.Fatal("бессрочный срок печатается датой")
	}
}

// «Купить трафик» — только у подписки на существующий трафик-тариф: не у
// срочного тарифа, не после удаления трафик-тарифа, не без подписки.
func TestTrafficUI_BuyTrafficOnlyForTrafficPlans(t *testing.T) {
	a, fs, panel, plan := trafficDealApp(t)
	ctx := context.Background()
	fm := a.msg.(*fakeMsg)
	noButton := func(where string) {
		t.Helper()
		if hasCallback(fm, cbTrafficPacks) {
			t.Fatalf("%s: «Купить трафик» не на трафик-тарифе: %v", where, fm.lastKB)
		}
	}

	// Срочный тариф: подписка есть, снимок — срочный.
	timed := &model.PlanSnapshot{Code: "term01", Months: 1, TrafficGB: 50}
	_ = fs.SetUserSnapshot(ctx, trafficUID, timed)
	panel.mu.Lock()
	panel.exists, panel.expire, panel.limit, panel.used = true, "2030-01-01T00:00:00Z", 50*gib, 50*gib
	panel.mu.Unlock()
	tap(a, "menu:home")
	noButton("меню срочного тарифа")
	tap(a, "menu:mysubs")
	noButton("подписка срочного тарифа")
	a.pushTrafficLimited(ctx, rwUserPayload{TelegramID: trafficUID})
	noButton("исчерпание на срочном тарифе")
	a.pushTrafficThreshold(ctx, rwUserPayload{TelegramID: trafficUID, LastTriggeredThreshold: 80})
	noButton("порог на срочном тарифе")

	// Трафик-тариф: кнопка есть везде.
	buyTrafficWithBalance(t, a, plan.Code, "")
	tap(a, "menu:home")
	if !hasCallback(fm, cbTrafficPacks) {
		t.Fatal("у владельца трафик-тарифа нет «Купить трафик»")
	}

	// Тариф удалён: пакеты не продаются — и кнопки нет.
	_ = fs.DeletePlan(ctx, plan.Code)
	tap(a, "menu:home")
	noButton("меню после удаления тарифа")
	tap(a, "menu:mysubs")
	noButton("подписка после удаления тарифа")

	// Подписки в панели нет (удалили руками): кнопки в меню нет.
	_ = fs.SavePlan(ctx, plan)
	panel.mu.Lock()
	panel.exists = false
	panel.mu.Unlock()
	a.invalidateSubCache(trafficUID)
	tap(a, "menu:home")
	noButton("меню без подписки в панели")
}
