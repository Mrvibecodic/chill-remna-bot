package app

import (
	"context"
	"sync"
	"testing"

	"remnabot/internal/model"
)

// Сетка цен живёт в общем конфиге: админка правит её карты под замком, а читают
// их продажи, витрина, мини-апп, автосписание и вебхуки — каждый в своей
// горутине. Пока чтение уходило из-под замка вместе с самими картами,
// одновременное чтение и запись карты убивали процесс: это не паника, её не
// перехватить ни recover'ом, ни middleware.
//
// Тест держит правку и чтение одновременно. Смысл он имеет только под -race
// (в CI прогон с -race есть): без детектора падение здесь случайное, а с ним —
// гарантированное, если копию из чтения убрать.
func TestPricingConcurrentReadWrite(t *testing.T) {
	ctx := context.Background()
	// Потокобезопасная подмена хранилища: сеттеры теперь пишут в тариф и зеркалят
	// сетку в конфиг, то есть ходят в хранилище — обычная подмена (fakeStore) под
	// конкурентной нагрузкой дала бы находку про свои карты, а не про конфиг.
	a, _ := planSyncApp(t, 0)

	const rounds = 40
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Админ правит цены, лимиты и сквады — ровно теми же функциями, что зовёт
	// экран цен.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < rounds; i++ {
			for _, mo := range model.PlanMonths {
				_ = a.setPlanPrice(ctx, "", mo, "base", "100")
				_ = a.setPlanPrice(ctx, "", mo, "p2p", "90")
				_ = a.setPlanPrice(ctx, "", mo, "yk", "110")
				_ = a.setPlanStars(ctx, "", mo, 99)
				_ = a.setPlanTraffic(ctx, "", mo, i%50)
				_ = a.setPlanDevices(ctx, "", mo, i%5)
				_ = a.togglePlanSquad(ctx, "", mo, "squad-a", false)
				_ = a.togglePlanSquad(ctx, "", mo, "ext-a", true)
			}
			_ = a.setPlanCurrency(ctx, "", "₽")
			_ = a.setPlanDeviceLimit(ctx, "", i%7)
		}
	}()

	// Читатели: витрина (цена и признак «срок продаётся»), путь платежа
	// (цена по способу и лимиты для панели) и запись конфига в базу — именно
	// она превращала конфиг в JSON, обходя карты.
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				pr := a.pricing()
				for _, mo := range model.PlanMonths {
					_ = pr.Base[mo]
					_ = pr.Fiat(model.PayMethodYooKassa, mo)
					_ = pr.StarPrice(mo)
					_ = pr.TrafficBytes(mo)
					_ = pr.DeviceLimitFor(mo)
					_ = pr.SquadsInt[mo]
					_ = pr.SquadsExt[mo]
					_ = a.periodOnSale(mo)
				}
				_ = a.saveBotConfig(ctx)
			}
		}()
	}

	wg.Wait()
}

// Копия сетки обязана быть независимой: правка копии не должна доходить до
// конфига (иначе цена менялась бы у всех молча), а правка конфига — до уже
// выданной копии.
func TestPricingCloneIsIndependent(t *testing.T) {
	a, _ := planApp(t)

	got := a.pricing()
	got.Base[1] = "999"
	got.Stars[1] = 999
	got.Traffic[12] = 999
	got.SquadsExt[12] = "changed"
	if len(got.SquadsInt[12]) > 0 {
		got.SquadsInt[12][0] = "changed"
	}

	a.mu.Lock()
	live := a.botCfg.Pricing
	a.mu.Unlock()
	if live.Base[1] != "150" || live.Stars[1] != 99 || live.Traffic[12] != 500 {
		t.Fatalf("правка копии дошла до конфига: %+v", live)
	}
	if live.SquadsExt[12] != "ext-year" {
		t.Fatalf("правка копии дошла до конфига: внешний сквад %q", live.SquadsExt[12])
	}
	if len(live.SquadsInt[12]) != 1 || live.SquadsInt[12][0] != "squad-year" {
		t.Fatalf("правка копии дошла до конфига: внутренние сквады %v", live.SquadsInt[12])
	}

	// И обратно: выданная копия не меняется задним числом.
	a.mu.Lock()
	a.botCfg.Pricing.Base[1] = "777"
	a.mu.Unlock()
	if got.Base[1] != "999" {
		t.Fatalf("выданная копия изменилась после правки конфига: %q", got.Base[1])
	}
}

// Копия конфига, которая уезжает в хранилище, обязана быть глубокой: иначе
// запись в базу читала бы те же карты, что правит админка. Проверяем на
// картах и слайсах, а не только на скалярах — именно они делятся.
func TestBotConfigCloneIsDeep(t *testing.T) {
	a, _ := planApp(t)
	a.botCfg.Reminders.DaysList = []int{3, 1}
	a.botCfg.Trial.InternalSquads = []string{"trial-squad"}
	a.botCfg.AddSub.InternalSquads = []string{"add-squad"}
	a.botCfg.P2P.Cards = []string{"card-1"}
	a.botCfg.PremiumEmoji = map[string]string{"star": "1"}

	cp, err := a.botCfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	cp.Pricing.Base[1] = "999"
	cp.Pricing.SquadsInt[12][0] = "changed"
	cp.Reminders.DaysList[0] = 99
	cp.Trial.InternalSquads[0] = "changed"
	cp.AddSub.InternalSquads[0] = "changed"
	cp.P2P.Cards[0] = "changed"
	cp.PremiumEmoji["star"] = "changed"

	c := a.botCfg
	if c.Pricing.Base[1] != "150" || c.Pricing.SquadsInt[12][0] != "squad-year" {
		t.Fatalf("сетка цен поделена с копией: %+v", c.Pricing)
	}
	if c.Reminders.DaysList[0] != 3 || c.Trial.InternalSquads[0] != "trial-squad" ||
		c.AddSub.InternalSquads[0] != "add-squad" || c.P2P.Cards[0] != "card-1" ||
		c.PremiumEmoji["star"] != "1" {
		t.Fatal("слайсы или карты конфига поделены с копией")
	}

	// Пустой конфиг копируется без паники: путь сохранения зовёт копию на
	// возможном nil.
	var nilCfg *model.BotConfig
	if cp, err := nilCfg.Clone(); err != nil || cp != nil {
		t.Fatalf("копия nil-конфига: %v, %v", cp, err)
	}
}

// В хранилище обязана уезжать копия, а не живой конфиг: там он превращается в
// JSON уже без замка. Проверка не про гонку, а про сам факт — правка конфига
// после сохранения не должна доходить до того, что уже отдано хранилищу.
func TestSaveBotConfigStoresSnapshot(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)

	if err := a.saveBotConfig(ctx); err != nil {
		t.Fatal(err)
	}
	if fs.cfg == nil {
		t.Fatal("конфиг не сохранён")
	}
	a.mu.Lock()
	a.botCfg.Pricing.Base[1] = "777"
	a.mu.Unlock()
	if got := fs.cfg.Pricing.Base[1]; got != "150" {
		t.Fatalf("хранилище получило живой конфиг: цена стала %q", got)
	}
}
