package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func storefrontApp(t *testing.T) (*App, *fakeMsg, *model.Plan) {
	t.Helper()
	t.Cleanup(i18n.ResetOverrides)
	a, fm, fs := planAdminApp(t)
	a.botCfg.CryptoBot.Enabled = true
	a.botCfg.CryptoBot.Token = "t"
	if err := a.syncBasePlan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, fm, vipPlan(t, fs, model.PlanAvailAll)
}

// storefrontLabels — подписи кнопок витрины, показанной по нажатию «Купить».
func storefrontLabels(t *testing.T, a *App, fm *fakeMsg, uid int64) []string {
	t.Helper()
	n := len(fm.buttonLabels())
	a.handleCallback(context.Background(), cb(uid, "menu:buy"))
	return fm.buttonLabels()[n:]
}

// Приписку «от N ₽» можно убрать в «Текстах бота»: кнопка тарифа остаётся
// с одним названием, без висящего разделителя; «Вернуть стандартный» её
// возвращает.
func TestStorefrontFromPriceHide(t *testing.T) {
	a, fm, _ := storefrontApp(t)
	const uid = int64(1000000001)

	labels := strings.Join(storefrontLabels(t, a, fm, uid), "|")
	if !strings.Contains(labels, "VIP · от 990 ₽") {
		t.Fatalf("по умолчанию цена на кнопке тарифа: %q", labels)
	}

	planTap(t, a, "tx:k:buy.from_price")
	if !hasCB(fm.allCallbackData(), "tx:hd:buy.from_price") {
		t.Fatal("в карточке текста нет кнопки «Убрать»")
	}
	planTap(t, a, "tx:hd:buy.from_price")
	planTap(t, a, "tx:hdy:buy.from_price")
	o, ok := a.botCfg.Texts.Overrides["ru"]["buy.from_price"]
	if !ok || o.Text != "" {
		t.Fatalf("убранный текст не сохранён: %+v %v", o, ok)
	}
	if !strings.Contains(fm.last(), "пусто") {
		t.Fatalf("карточка не показывает, что текст убран: %q", fm.last())
	}
	if fm.emptyBtns != 0 {
		t.Fatalf("кнопок без подписи: %d", fm.emptyBtns)
	}

	labels = strings.Join(storefrontLabels(t, a, fm, uid), "|")
	if strings.Contains(labels, "·") || strings.Contains(labels, "990") {
		t.Fatalf("цена на кнопке тарифа осталась: %q", labels)
	}
	if !strings.Contains(labels, "VIP") {
		t.Fatalf("кнопки тарифа нет: %q", labels)
	}

	planTap(t, a, "tx:ry:buy.from_price")
	labels = strings.Join(storefrontLabels(t, a, fm, uid), "|")
	if !strings.Contains(labels, "VIP · от 990 ₽") {
		t.Fatalf("стандартный текст не вернулся: %q", labels)
	}
}

// Убрать можно только тексты, где это предусмотрено.
func TestTextHideOnlyHideable(t *testing.T) {
	a, _, _ := storefrontApp(t)
	planTap(t, a, "tx:hdy:buy.choose_tariff")
	if _, ok := a.botCfg.Texts.Overrides["ru"]["buy.choose_tariff"]; ok {
		t.Fatal("обязательный текст убран")
	}
}

// «Назад» с экрана способов оплаты возвращает в карточку того же тарифа — с
// теми же кнопками, с какими она была показана.
func TestMethodsBackToPlanCard(t *testing.T) {
	a, fm, p := storefrontApp(t)
	ctx := context.Background()
	const uid = int64(1000000002)

	a.handleCallback(ctx, cb(uid, "menu:buy"))
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if !strings.Contains(fm.last(), "Как удобнее оплатить") {
		t.Fatalf("экран способов не открылся: %q", fm.last())
	}
	n := len(fm.allCallbackData())
	a.handleCallback(ctx, cb(uid, "plbk"))
	if !strings.Contains(fm.last(), "VIP") {
		t.Fatalf("«Назад» не вернул карточку тарифа: %q", fm.last())
	}
	after := fm.allCallbackData()[n:]
	if !hasCB(after, "plb:"+p.Code+":12") || !hasCB(after, "menu:buy") {
		t.Fatalf("карточка без сроков или без «Назад» к списку: %v", after)
	}
}

// Кнопка «Назад» есть на экране способов и для тарифа по ссылке: он с витрины
// не открывается, поэтому возврат идёт через намерение покупки.
func TestMethodsBackLinkPlan(t *testing.T) {
	t.Cleanup(i18n.ResetOverrides)
	a, fm, fs := planAdminApp(t)
	a.botCfg.CryptoBot.Enabled = true
	a.botCfg.CryptoBot.Token = "t"
	p := vipPlan(t, fs, model.PlanAvailLink)
	ctx := context.Background()
	const uid = int64(1000000003)

	a.handleMessage(ctx, msgText(uid, "/start plan_"+p.Code))
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if !hasCB(fm.allCallbackData(), "plbk") {
		t.Fatal("на экране способов нет «Назад»")
	}
	a.handleCallback(ctx, cb(uid, "plbk"))
	if !strings.Contains(fm.last(), "VIP") || strings.Contains(fm.last(), "недоступен") {
		t.Fatalf("«Назад» не вернул карточку тарифа по ссылке: %q", fm.last())
	}

	// Тариф выключили, пока экран висел в переписке: «Назад» ведёт на
	// витрину, а не в закрытую карточку.
	p.Enabled = false
	if err := fs.SavePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	nt, nc := len(fm.texts), len(fm.allCallbackData())
	a.handleCallback(ctx, cb(uid, "plbk"))
	if !strings.Contains(strings.Join(fm.texts[nt:], "\n"), "Выбор устарел") {
		t.Fatalf("устаревшая кнопка не отправила на витрину: %q", fm.last())
	}
	for _, d := range fm.allCallbackData()[nc:] {
		if strings.HasPrefix(d, "plb:"+p.Code+":") {
			t.Fatalf("выключенный тариф показан карточкой: %v", fm.allCallbackData()[nc:])
		}
	}
}

// Число серверов — на карточке тарифа и на экране оплаты; убирается в
// «Текстах бота».
func TestPlanCardServers(t *testing.T) {
	a, fm, p := storefrontApp(t)
	ctx := context.Background()
	const uid = int64(1000000004)
	a.infraCache = &infraCacheEntry{
		fetchedAt: time.Now(),
		squads:    []remnawave.SquadFull{{UUID: "sq-vip", InboundsCount: 1, InboundUUIDs: []string{"ib1"}}},
		hosts: []remnawave.Host{
			{Remark: "🇩🇪 Германия #1", InboundUUID: "ib1"},
			{Remark: "🇩🇪 Германия #2", InboundUUID: "ib1"},
			{Remark: "🇳🇱 Нидерланды", InboundUUID: "ib1", Hidden: true},
		},
	}
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	if !strings.Contains(fm.last(), "Доступно стран: 1") || !strings.Contains(fm.last(), "Серверов: 2") {
		t.Fatalf("карточка без стран или серверов: %q", fm.last())
	}
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if !strings.Contains(fm.last(), "Серверов: 2") {
		t.Fatalf("экран оплаты без серверов: %q", fm.last())
	}

	planTap(t, a, "tx:hdy:buy.servers")
	a.handleCallback(ctx, cb(uid, "plo:"+p.Code))
	if strings.Contains(fm.last(), "Серверов") || !strings.Contains(fm.last(), "Доступно стран: 1") {
		t.Fatalf("строка серверов не убралась: %q", fm.last())
	}
}
