package app

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// syncStore — подменённое хранилище, пригодное для конкурентных тестов.
//
// Обычное подменённое (fakeStore) для них не годится по двум причинам: его карты
// не защищены замком, и копии в нём поверхностные — тариф, прочитанный из него,
// делит длительности с тем, что лежит «в базе». Второе особенно опасно для
// тестов потерянного обновления: правка, записанная поверх устаревшего чтения,
// на поверхностных копиях НЕ теряется, и тест зеленеет там, где настоящая база
// показала бы потерю. Здесь копии глубокие — ровно как в настоящем хранилище.
type syncStore struct {
	*fakeStore
	mu sync.Mutex
	// delay — задержка записи. Без неё окно обгона слишком узкое, и тест
	// проходит даже без сериализации.
	delay time.Duration
	// savedPrices — цена первого срока в порядке ПРИБЫТИЯ в базу.
	savedPrices []int
	// failSavePlan — столько ближайших записей тарифа упадут.
	failSavePlan int
	// savePlanCalls — сколько раз тариф записывался.
	savePlanCalls int
	failDelete    bool
	failList      bool
}

var errTestStore = errors.New("хранилище недоступно (тест)")

func (s *syncStore) SaveConfig(ctx context.Context, c *model.BotConfig) error {
	// Цена читается ДО задержки: важен порядок, в котором снимки доехали до
	// записи, а не порядок выхода из неё.
	price, _ := strconv.Atoi(c.Pricing.Base[1])
	s.mu.Lock()
	s.savedPrices = append(s.savedPrices, price)
	s.mu.Unlock()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp, err := c.Clone()
	if err != nil {
		return err
	}
	s.fakeStore.cfg = cp
	return nil
}

func (s *syncStore) SavePlan(ctx context.Context, p *model.Plan) error {
	s.mu.Lock()
	s.savePlanCalls++
	if s.failSavePlan > 0 {
		s.failSavePlan--
		s.mu.Unlock()
		return errTestStore
	}
	s.mu.Unlock()
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	cp.IntSquads = append([]string(nil), p.IntSquads...)
	// Круг через сериализацию — как в настоящем хранилище: копия не делит с
	// вызывающим ни длительности, ни то, на что смотрят их указатели.
	cp.Durations = model.DecodeDurations(model.EncodeDurations(p.Durations))
	cp.Normalize()
	if s.fakeStore.plans == nil {
		s.fakeStore.plans = map[string]*model.Plan{}
	}
	s.fakeStore.plans[cp.Code] = &cp
	return nil
}

func (s *syncStore) GetPlan(ctx context.Context, code string) (*model.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.fakeStore.plans[code]
	if p == nil {
		return nil, nil
	}
	cp := *p
	cp.IntSquads = append([]string(nil), p.IntSquads...)
	cp.Durations = model.DecodeDurations(model.EncodeDurations(p.Durations))
	return &cp, nil
}

func (s *syncStore) ListPlans(ctx context.Context) ([]model.Plan, error) {
	if s.failList {
		return nil, errTestStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.fakeStore.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Durations = model.DecodeDurations(model.EncodeDurations(list[i].Durations))
	}
	return list, nil
}

func (s *syncStore) DeletePlan(ctx context.Context, code string) error {
	if s.failDelete {
		return errTestStore
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeStore.DeletePlan(ctx, code)
}

func (s *syncStore) prices() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.savedPrices...)
}

func (s *syncStore) planWrites() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.savePlanCalls
}

// planSyncApp — приложение с потокобезопасным хранилищем.
func planSyncApp(t *testing.T, delay time.Duration) (*App, *syncStore) {
	t.Helper()
	a, fs := planApp(t)
	st := &syncStore{fakeStore: fs, delay: delay}
	a.mu.Lock()
	a.store = st
	a.mu.Unlock()
	return a, st
}

// Снимок конфига без сериализации записи ничего не гарантирует: два сохранения
// доезжают до базы в обратном порядке, и в базе остаётся состояние, снятое
// раньше. Сохранение зовут и фоновые пути (проверка обновлений, ротация карт при
// заявке на перевод), так что «оба сохранения из одного обработчика» неверно.
//
// Инвариант: цены прибывают в базу в неубывающем порядке. Ставим их монотонно
// растущими, поэтому обратный порядок прибытия означает, что снимок обогнал
// запись.
func TestSaveBotConfigDoesNotOvertake(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 300*time.Microsecond)

	// Цена в конфиге обязана расти монотонно, иначе тест ловил бы не обгон
	// записи, а свой собственный порядок правок: номер и запись цены берутся под
	// одним замком, поэтому в конфиге значение никогда не уменьшается.
	var seqMu sync.Mutex
	seq := 0
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				seqMu.Lock()
				seq++
				a.setBasePrice(1, strconv.Itoa(seq))
				seqMu.Unlock()
				if err := a.saveBotConfig(ctx); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got := st.prices()
	if len(got) != 150 {
		t.Fatalf("сохранений дошло %d из 150", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("сохранение обогнало предыдущее: в базу приехало %d после %d (позиция %d)", got[i], got[i-1], i)
		}
	}
}

// Строка тарифа пишется целиком, поэтому «прочитал → изменил → записал» без
// замка теряет чужую правку. Здесь сталкиваются две таких последовательности:
// синхронизация «Базового» от сетки цен (её запускает и фоновая проверка
// обновлений, и заявка покупателя на перевод) и правка имени из карточки.
//
// Инвариант: в конце видны ОБЕ правки — и последняя цена из конфига, и последнее
// имя. Без замка одна из них исчезает.
func TestPlanEditAndConfigSyncKeepBoth(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 50*time.Microsecond)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}

	// Проверка идёт ПО ХОДУ, а не по итогу: та горутина, что закончит позже,
	// доработает в одиночку и приведёт итоговое состояние в порядок, даже если по
	// дороге правки терялись.
	//
	// Инвариант обеих серий — «не назад»: цена в конфиге только растёт, номер
	// имени только растёт, значит и в тарифе они не имеют права уменьшаться.
	const rounds = 80
	var wg sync.WaitGroup

	// Наблюдатель проверяет то, что не видно ни одному из писателей: строка
	// тарифа не имеет права поехать НАЗАД. Цена в конфиге только растёт, номер
	// имени только растёт — значит любое уменьшение любого из них означает, что
	// запись легла поверх устаревшего чтения.
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastPrice, lastName := 0, 0
		for {
			select {
			case <-done:
				return
			default:
			}
			p, err := st.GetPlan(ctx, model.PlanCodeBase)
			if err != nil || p == nil {
				continue
			}
			price := 0
			if d := p.Duration(1); d != nil {
				price, _ = strconv.Atoi(d.Base)
			}
			if price < lastPrice {
				t.Errorf("цена в тарифе поехала назад: %d после %d", price, lastPrice)
				return
			}
			if n := nameIndex(p.Name); n < lastName {
				t.Errorf("имя тарифа поехало назад: %q после номера %d", p.Name, lastName)
				return
			} else {
				lastName = n
			}
			lastPrice = price
		}
	}()
	var writers sync.WaitGroup
	writers.Add(2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer writers.Done()
		lastName := 0
		for i := 1; i <= rounds; i++ {
			a.setBasePrice(1, strconv.Itoa(1000+i))
			if err := a.saveBotConfig(ctx); err != nil {
				t.Error(err)
				return
			}
			p, err := st.GetPlan(ctx, model.PlanCodeBase)
			if err != nil || p == nil {
				t.Error("тариф не прочитан:", err)
				return
			}
			// Цену только что записала синхронизация — значит в тарифе она обязана
			// быть не старше только что заданной. Меньше означает, что поверх
			// легла правка из карточки, начатая до синхронизации: ровно та потеря
			// правки, от которой стоит замок.
			price := 0
			if d := p.Duration(1); d != nil {
				price, _ = strconv.Atoi(d.Base)
			}
			if price < 1000+i {
				t.Errorf("цена из конфига потеряна: в тарифе %d, а в конфиге уже %d", price, 1000+i)
				return
			}
			n := nameIndex(p.Name)
			if n < lastName {
				t.Errorf("переименование откатилось: имя %q после номера %d", p.Name, lastName)
				return
			}
			lastName = n
		}
	}()
	go func() {
		defer wg.Done()
		defer writers.Done()
		lastPrice := 0
		for i := 1; i <= rounds; i++ {
			name := "Имя" + strconv.Itoa(i)
			p, err := a.editPlan(ctx, model.PlanCodeBase, func(p *model.Plan) error {
				p.Name = name
				return nil
			})
			if err != nil {
				t.Error(err)
				return
			}
			price := 0
			if d := p.Duration(1); d != nil {
				price, _ = strconv.Atoi(d.Base)
			}
			if price < lastPrice {
				t.Errorf("цена из конфига откатилась: %d после %d", price, lastPrice)
				return
			}
			lastPrice = price
		}
	}()
	writers.Wait()
	close(done)
	wg.Wait()

	p, err := st.GetPlan(ctx, model.PlanCodeBase)
	if err != nil || p == nil {
		t.Fatalf("тариф не прочитан: %v", err)
	}
	wantName := "Имя" + strconv.Itoa(rounds)
	wantPrice := strconv.Itoa(1000 + rounds)
	if p.Name != wantName {
		t.Fatalf("переименование потеряно: имя %q вместо %q", p.Name, wantName)
	}
	d := p.Duration(1)
	if d == nil || d.Base != wantPrice {
		t.Fatalf("цена из конфига потеряна: %+v, ожидалась %s", p.Durations, wantPrice)
	}
}

// Отказ хранилища на записи тарифа обязан доходить до админа: молчаливый возврат
// в карточку читается как «сохранено».
func TestPlansAdmin_StorageFailureIsReported(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetPlan(ctx, model.PlanCodeBase)

	st.failSavePlan = 1
	planTap(t, a, "pln:toggle:"+model.PlanCodeBase)
	if !containsAny(fm.last(), "Сервис временно недоступен", "temporarily unavailable") {
		t.Fatalf("отказ хранилища не показан админу: %q", fm.last())
	}
	after, _ := st.GetPlan(ctx, model.PlanCodeBase)
	if after.Enabled != before.Enabled {
		t.Fatal("состояние изменилось, хотя запись не удалась")
	}

	// Удаление: тот же принцип.
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := st.ListPlans(ctx)
	code := ""
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			code = list[i].Code
		}
	}
	st.failDelete = true
	planTap(t, a, "pln:delyes:"+code)
	if p, _ := st.GetPlan(ctx, code); p == nil {
		t.Fatal("тариф удалён, хотя хранилище отказало")
	}
	if !containsAny(fm.last(), "Сервис временно недоступен", "temporarily unavailable") {
		t.Fatalf("отказ удаления не показан: %q", fm.last())
	}
}

// Тариф, удалённый пока экран висел в переписке: нажатие обязано сказать, что
// тарифа нет, а не молча показать список или притвориться успехом.
func TestPlansAdmin_GonePlanIsReported(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	planTap(t, a, "pln:dup:"+model.PlanCodeBase)
	list, _ := st.ListPlans(ctx)
	code := ""
	for i := range list {
		if list[i].Code != model.PlanCodeBase {
			code = list[i].Code
		}
	}
	if err := st.DeletePlan(ctx, code); err != nil {
		t.Fatal(err)
	}

	for _, data := range []string{"pln:open:" + code, "pln:toggle:" + code, "pln:dup:" + code,
		"pln:del:" + code, "pln:up:" + code, "pln:down:" + code} {
		planTap(t, a, data)
		if !containsAny(fm.last(), "не найден", "not found") {
			t.Fatalf("%s: об исчезнувшем тарифе не сказано: %q", data, fm.last())
		}
	}
}

// Движение крайнего тарифа не должно ни писать в базу, ни молчать: экран остался
// бы прежним, и кнопка выглядела бы сломанной.
func TestPlansAdmin_MoveAtEdgeWritesNothing(t *testing.T) {
	ctx := context.Background()
	a, st := planSyncApp(t, 0)
	fm := &fakeMsg{}
	a.msg = fm
	for _, code := range []string{"aaa", "bbb"} {
		if err := st.SavePlan(ctx, &model.Plan{Code: code, Name: code, Availability: model.PlanAvailAll}); err != nil {
			t.Fatal(err)
		}
	}
	writes := st.planWrites()

	planTap(t, a, "pln:up:aaa")
	if st.planWrites() != writes {
		t.Fatalf("на краю списка тариф всё равно записан: %d записей", st.planWrites()-writes)
	}
	if !containsAny(fm.last(), "крайний", "at the edge") {
		t.Fatalf("причина не показана: %q", fm.last())
	}
}

// nameIndex — номер из имени вида «ИмяN». Ноль, если номера нет.
func nameIndex(name string) int {
	for i := 0; i < len(name); i++ {
		if name[i] >= '0' && name[i] <= '9' {
			n, _ := strconv.Atoi(name[i:])
			return n
		}
	}
	return 0
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) && stringsContains(s, sub) {
			return true
		}
	}
	return false
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
