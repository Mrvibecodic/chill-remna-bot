package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/storage"
)

// bonusPanel — учётка с изменяемыми потолком и расходом; помнит патчи.
type bonusPanel struct {
	mu      sync.Mutex
	limit   int64
	used    int64
	expire  string
	resetAt string
	patches []map[string]any
	srv     *httptest.Server
}

func newBonusPanel(t *testing.T, limit, used int64) *bonusPanel {
	t.Helper()
	p := &bonusPanel{limit: limit, used: used, expire: "2099-01-01T00:00:00Z", resetAt: "2026-09-01T00:00:00Z"}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p.mu.Lock()
		defer p.mu.Unlock()
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"response": []map[string]any{{
				"uuid": "u1", "tag": "CHILLBOT", "username": "tg_555", "expireAt": p.expire,
				"trafficLimitBytes": p.limit, "usedTrafficBytes": p.used,
				"lastTrafficResetAt": p.resetAt,
			}}})
			return
		}
		if r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			p.patches = append(p.patches, body)
			if v, ok := body["trafficLimitBytes"].(float64); ok {
				p.limit = int64(v)
			}
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","expireAt":"2099-01-01T00:00:00Z"}}`))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *bonusPanel) set(limit, used int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limit, p.used = limit, used
}

// rollPeriod — панель сменила период: обнулила счётчик и переставила отметку.
func (p *bonusPanel) rollPeriod(at string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used, p.resetAt = 0, at
}

func (p *bonusPanel) currentLimit() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.limit
}

func bonusApp(t *testing.T, p *bonusPanel) (*App, *fakeStore) {
	t.Helper()
	a, fs := snapApp(t, p.srv.URL)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	return a, fs
}

// Разовый подарок: потолок поднят, запись заведена, и как только панель
// обнулила расход, прибавка снимается ровно один раз.
func TestBonusTraffic_OneTimeTakenBackAfterReset(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 30*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})

	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	if p.currentLimit() != 75*gb {
		t.Fatalf("потолок не поднят: %d", p.currentLimit())
	}
	u, _ := fs.GetUser(ctx, 555)
	if u.TrafficBonus == nil || u.TrafficBonus.Bytes != 25*gb || u.TrafficBonus.Limit != 75*gb {
		t.Fatalf("запись о подарке неверна: %+v", u.TrafficBonus)
	}

	// Период ещё идёт, расход растёт — забирать рано.
	p.set(75*gb, 60*gb)
	if n := a.sweepBonusTrafficOnce(ctx); n != 0 {
		t.Fatalf("подарок забрали посреди периода: %d", n)
	}
	if p.currentLimit() != 75*gb {
		t.Fatalf("потолок тронут раньше времени: %d", p.currentLimit())
	}

	// Панель сменила период — вот теперь снимаем.
	p.rollPeriod("2026-10-01T00:00:00Z")
	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("подарок не снят после обнуления: %d", n)
	}
	if p.currentLimit() != 50*gb {
		t.Fatalf("потолок не вернулся к тарифному: %d", p.currentLimit())
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus != nil {
		t.Fatalf("запись о подарке осталась: %+v", u.TrafficBonus)
	}
	// Второй проход не имеет права урезать потолок ещё раз.
	if n := a.sweepBonusTrafficOnce(ctx); n != 0 || p.currentLimit() != 50*gb {
		t.Fatalf("повторное снятие: n=%d limit=%d", n, p.currentLimit())
	}
}

// Постоянная прибавка записи не заводит и после обнуления остаётся.
func TestBonusTraffic_PeriodKindStays(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 30*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "ALW", Kind: model.PromoKindTrafficPeriod, Value: 25, MaxUses: 1})

	if _, ok := a.redeemPromo(ctx, 555, "ALW"); !ok {
		t.Fatal("прибавка не выдана")
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus != nil {
		t.Fatalf("постоянная прибавка завела запись о разовом подарке: %+v", u.TrafficBonus)
	}
	p.rollPeriod("2026-10-01T00:00:00Z")
	if n := a.sweepBonusTrafficOnce(ctx); n != 0 {
		t.Fatalf("постоянную прибавку сняли: %d", n)
	}
	if p.currentLimit() != 75*gb {
		t.Fatalf("постоянная прибавка потеряна: %d", p.currentLimit())
	}
}

// Потолок переписали покупкой: подарка в панели больше нет, и вычитать его
// из тарифного лимита нельзя — это отобрало бы у человека оплаченное.
func TestBonusTraffic_RewrittenCeilingNotStolen(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 30*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	// Человек оплатил тариф на 200 ГБ, панель обнулила расход.
	p.set(200*gb, 0)
	p.rollPeriod("2026-10-01T00:00:00Z")
	p.set(200*gb, 0)

	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("запись о подарке не закрыта: %d", n)
	}
	if p.currentLimit() != 200*gb {
		t.Fatalf("у оплаченного тарифа отобрали трафик: %d", p.currentLimit())
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus != nil {
		t.Fatal("запись о подарке осталась после перезаписи потолка")
	}
}

// Два разовых кода подряд складываются, и снимается сумма.
func TestBonusTraffic_TwoGiftsStack(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "A", Kind: model.PromoKindTraffic, Value: 10, MaxUses: 1})
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "B", Kind: model.PromoKindTraffic, Value: 15, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "A"); !ok {
		t.Fatal("первый код")
	}
	if _, ok := a.redeemPromo(ctx, 555, "B"); !ok {
		t.Fatal("второй код")
	}
	if p.currentLimit() != 75*gb {
		t.Fatalf("потолок после двух кодов: %d", p.currentLimit())
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus == nil || u.TrafficBonus.Bytes != 25*gb {
		t.Fatalf("подарки не сложились: %+v", u.TrafficBonus)
	}
	p.rollPeriod("2026-10-01T00:00:00Z")
	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("не снято: %d", n)
	}
	if p.currentLimit() != 50*gb {
		t.Fatalf("сняли не всю сумму подарков: %d", p.currentLimit())
	}
}

// Истёкшая подписка: запись закрывается без похода в панель — потолок всё
// равно применят заново при следующей оплате.
func TestBonusTraffic_ExpiredSubClearsRecord(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	p.mu.Lock()
	p.expire = "2020-01-01T00:00:00Z"
	before := len(p.patches)
	p.mu.Unlock()

	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("запись не закрыта: %d", n)
	}
	p.mu.Lock()
	after := len(p.patches)
	p.mu.Unlock()
	if after != before {
		t.Fatalf("в панель ушёл лишний патч: %d", after-before)
	}
}

// Расход в момент выдачи равен нулю — обычное дело сразу после покупки.
// Признаком «подарок отработал» служит смена отметки сброса, а не падение
// счётчика, иначе такой подарок не сняли бы никогда.
func TestBonusTraffic_ZeroUsedAtGrantStillTakenBack(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 0)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	p.rollPeriod("2026-10-01T00:00:00Z")
	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("подарок при нулевом расходе не снят: %d", n)
	}
	if p.currentLimit() != 50*gb {
		t.Fatalf("потолок не вернулся: %d", p.currentLimit())
	}
}

// Период сменился, а человек успел выкачать больше прежнего до прихода
// прохода: признак по счётчику здесь бы стёрся, по отметке сброса — нет.
func TestBonusTraffic_TakenBackEvenAfterHeavyUse(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	p.rollPeriod("2026-10-01T00:00:00Z")
	p.set(75*gb, 12*gb)
	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("подарок уехал в новый период: %d", n)
	}
	if p.currentLimit() != 50*gb {
		t.Fatalf("потолок не вернулся: %d", p.currentLimit())
	}
}

// Новый тариф случайно совпал по числу с потолком «тариф плюс подарок».
// Совпадение чисел — не повод вычитать: сменился срок подписки, значит
// потолок ставила покупка.
func TestBonusTraffic_SameNumberAfterPurchaseNotStolen(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	// Человек купил тариф ровно на 75 ГБ: потолок тот же, срок другой.
	p.mu.Lock()
	p.expire = "2100-06-01T00:00:00Z"
	p.mu.Unlock()
	p.rollPeriod("2026-10-01T00:00:00Z")

	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("запись не закрыта: %d", n)
	}
	if p.currentLimit() != 75*gb {
		t.Fatalf("у оплаченного тарифа отобрали трафик: %d", p.currentLimit())
	}
}

// Пока очередь ползла, человек активировал второй код. Судить по устаревшей
// копии нельзя: она затёрла бы свежую запись, и оба подарка стали бы вечными.
func TestBonusTraffic_StaleQueueEntryIgnored(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "A", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "B", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "A"); !ok {
		t.Fatal("первый код")
	}
	u, _ := fs.GetUser(ctx, 555)
	stale := *u.TrafficBonus // копия очереди снята здесь
	if _, ok := a.redeemPromo(ctx, 555, "B"); !ok {
		t.Fatal("второй код")
	}

	if a.sweepBonusOne(ctx, fs, a.panel, storageTarget(555, &stale)) {
		t.Fatal("устаревшая копия закрыла свежую запись")
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus == nil || u.TrafficBonus.Bytes != 50*gb {
		t.Fatalf("свежая запись потеряна: %+v", u.TrafficBonus)
	}
}

// Прежний подарок уже отработал, а проход до человека не дошёл. Новая выдача
// обязана вычесть отработавшую прибавку, иначе она останется навсегда.
func TestBonusTraffic_GrantAfterRollTakesOldBack(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "A", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "B", Kind: model.PromoKindTraffic, Value: 10, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "A"); !ok {
		t.Fatal("первый код")
	}
	p.rollPeriod("2026-10-01T00:00:00Z")
	if _, ok := a.redeemPromo(ctx, 555, "B"); !ok {
		t.Fatal("второй код")
	}
	if p.currentLimit() != 60*gb {
		t.Fatalf("отработавший подарок остался в потолке: %d", p.currentLimit())
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus == nil || u.TrafficBonus.Bytes != 10*gb {
		t.Fatalf("запись помнит лишнее: %+v", u.TrafficBonus)
	}
}

func storageTarget(id int64, b *model.TrafficBonus) storage.TrafficBonusTarget {
	return storage.TrafficBonusTarget{TelegramID: id, Bonus: b}
}

// Учётки в панели больше нет — запись закрываем, иначе она висела бы вечно и
// стоила запроса в панель каждый час.
func TestBonusTraffic_NoPanelAccountClearsRecord(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	t.Cleanup(gone.Close)
	a.panel = testPanel(gone.URL)

	if !a.sweepBonusOne(ctx, fs, a.panel, storageTarget(555, nil)) {
		t.Fatal("запись без учётки в панели не закрыта")
	}
	if u, _ := fs.GetUser(ctx, 555); u.TrafficBonus != nil {
		t.Fatalf("запись осталась: %+v", u.TrafficBonus)
	}
}

// Подписка кончилась давно: запись закрываем без похода в панель за прибавкой.
func TestBonusTraffic_LongExpiredCleared(t *testing.T) {
	p := newBonusPanel(t, 50*gb, 10*gb)
	a, fs := bonusApp(t, p)
	ctx := context.Background()
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "GIGA", Kind: model.PromoKindTraffic, Value: 25, MaxUses: 1})
	if _, ok := a.redeemPromo(ctx, 555, "GIGA"); !ok {
		t.Fatal("подарок не выдан")
	}
	// Срок в панели истёк год назад, период с тех пор не менялся.
	old := time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339)
	u, _ := fs.GetUser(ctx, 555)
	u.TrafficBonus.Expire = old
	_ = fs.SetTrafficBonus(ctx, 555, u.TrafficBonus)
	p.mu.Lock()
	p.expire = old
	before := len(p.patches)
	p.mu.Unlock()

	if n := a.sweepBonusTrafficOnce(ctx); n != 1 {
		t.Fatalf("запись давно ушедшего не закрыта: %d", n)
	}
	p.mu.Lock()
	after := len(p.patches)
	p.mu.Unlock()
	if after != before {
		t.Fatalf("в панель ушёл лишний патч: %d", after-before)
	}
}

// Пустая запись не сохраняется и не читается: иначе проход возился бы с
// подарком в ноль байт и мог бы записать в панель бессмысленное число.
func TestTrafficBonus_EmptyNotStored(t *testing.T) {
	for _, b := range []*model.TrafficBonus{nil, {Bytes: 0, Limit: 50 * gb}, {Bytes: -1}} {
		if got := b.Encode(); got != "" {
			t.Fatalf("пустой подарок закодирован: %q", got)
		}
	}
	if got := model.DecodeTrafficBonus(`{"bytes":0,"limit":100}`); got != nil {
		t.Fatalf("подарок в ноль байт прочитан: %+v", got)
	}
	if got := model.DecodeTrafficBonus("не json"); got != nil {
		t.Fatalf("мусор прочитан как подарок: %+v", got)
	}
}
