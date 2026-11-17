package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

// Дамп remnashop: текущие подписки разных планов, пробная, без плана,
// удалённый план, истёкшая и отключённая.
const rsLinkDump = `COPY public.users (id, telegram_id, username, name, is_blocked, current_subscription_id, is_trial_available, created_at) FROM stdin;
1	1000000011	a	A	f	11	f	2026-01-01 10:00:00+00
2	1000000012	b	B	f	22	f	2026-01-01 10:00:00+00
3	1000000013	c	C	f	33	f	2026-01-01 10:00:00+00
4	1000000014	d	D	f	44	f	2026-01-01 10:00:00+00
5	1000000015	e	E	f	55	f	2026-01-01 10:00:00+00
6	1000000016	f	F	f	66	f	2026-01-01 10:00:00+00
9	1000000019	i	I	f	99	f	2026-01-01 10:00:00+00
\.

COPY public.subscriptions (id, status, is_trial, traffic_limit, device_limit, internal_squads, expire_at, plan_snapshot, external_squad, traffic_limit_strategy, user_id) FROM stdin;
11	ACTIVE	f	100	10	{11111111-1111-4111-8111-111111111111}	2030-12-17 11:46:25+00	{"id": 1, "name": "Старт"}	\N	NO_RESET	1
22	ACTIVE	f	0	5	{11111111-1111-4111-8111-111111111111,22222222-2222-4222-8222-222222222222}	2030-12-17 00:00:00+00	{"id": 2, "name": "Про"}	\N	NO_RESET	2
33	ACTIVE	t	10	0	{33333333-3333-4333-8333-333333333333}	2030-11-20 00:00:00+00	{"id": 4, "name": "Пробный", "is_trial": true}	\N	NO_RESET	3
44	ACTIVE	f	0	2	{11111111-1111-4111-8111-111111111111}	2030-12-07 00:00:00+00	{"id": -1, "name": "IMPORTED"}	\N	NO_RESET	4
55	EXPIRED	f	0	5	{}	2025-01-01 00:00:00+00	{"id": 9, "name": "Старый"}	\N	NO_RESET	5
66	DISABLED	f	100	3	{11111111-1111-4111-8111-111111111111}	2025-03-01 00:00:00+00	{"id": 1, "name": "Старт"}	\N	MONTH	6
99	ACTIVE	f	0	5	{11111111-1111-4111-8111-111111111111,22222222-2222-4222-8222-222222222222}	2030-12-17 00:00:00+00	{"id": 2, "name": "Про"}	\N	NO_RESET	9
\.

COPY public.plans (id, name, traffic_limit, device_limit, internal_squads, external_squad, traffic_limit_strategy) FROM stdin;
1	Старт	100	3	{11111111-1111-4111-8111-111111111111}	\N	NO_RESET
2	Про	0	5	{11111111-1111-4111-8111-111111111111,22222222-2222-4222-8222-222222222222}	\N	NO_RESET
4	Пробный	10	0	{33333333-3333-4333-8333-333333333333}	\N	NO_RESET
\.
`

func rsLinkApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	ctx := context.Background()
	a, fm, fs := rsTestApp(t)
	fm.downloads["dump-link"] = []byte(rsLinkDump)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []model.Plan{
		{Code: "std", Name: "Стандарт", Enabled: true, IntSquads: []string{"11111111-1111-4111-8111-111111111111"}, TrafficGB: 150, DeviceLimit: 3},
		{Code: "prem", Name: "Премиум", Enabled: true, Availability: model.PlanAvailList, DeviceLimit: 6},
	} {
		if err := fs.SavePlan(ctx, &p); err != nil {
			t.Fatal(err)
		}
	}
	// Купил в боте — его тариф связывание не трогает.
	_ = fs.UpsertUser(ctx, 1000000012)
	fs.users[1000000012].Snapshot = &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1, Price: "100"}
	return a, fm, fs
}

func rsTap(t *testing.T, a *App, data string) {
	t.Helper()
	a.handleCallback(context.Background(), cb(100, data))
}

func rsUpload(t *testing.T, a *App) {
	t.Helper()
	rsTap(t, a, "rsimp:up")
	a.handleDocument(context.Background(), docMsg(100, "dump-link", int64(len(rsLinkDump))))
}

func TestRSLink_EndToEnd(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := rsLinkApp(t)
	rsUpload(t, a)
	if last := fm.last(); !strings.Contains(last, "Планов remnashop с подписками: <b>3</b>, связано с тарифами: <b>0</b>") ||
		!hasCB(fm.allCallbackData(), "rsimp:lk") {
		t.Fatalf("предпросмотр:\n%s", last)
	}

	rsTap(t, a, "rsimp:lk")
	last := fm.last()
	for _, want := range []string{"<b>Старт</b> · 2 подп. → не выбран", "<b>Про</b> · 2 подп. → не выбран",
		"<b>Старый</b> (удалён в remnashop) · 1 подп.", "🎁 Пробные · 1 подп.", "Без плана · 1 подп."} {
		if !strings.Contains(last, want) {
			t.Fatalf("нет %q:\n%s", want, last)
		}
	}

	rsTap(t, a, "rsimp:lp:1")
	if last := fm.last(); !strings.Contains(last, "Тариф для плана «Старт»") || !strings.Contains(last, "Стандарт") ||
		!hasCB(fm.allCallbackData(), "rsimp:ls:1:std") {
		t.Fatalf("выбор тарифа:\n%s", last)
	}
	rsTap(t, a, "rsimp:ls:1:std")
	rsTap(t, a, "rsimp:ls:2:prem")
	rsTap(t, a, "rsimp:ls:9:-")
	rsTap(t, a, "rsimp:ls:1:gone") // тарифа нет — выбор прежний
	if last := fm.last(); !strings.Contains(last, "Тариф для плана «Старт»") {
		t.Fatalf("кнопка несуществующего тарифа:\n%s", last)
	}
	rsTap(t, a, "rsimp:ls:777:std") // плана нет в дампе
	rsTap(t, a, "rsimp:lp:777")
	rsTap(t, a, "rsimp:lp:x")
	rsTap(t, a, "rsimp:lk")
	for _, want := range []string{"rsimp:lp:1", "rsimp:lp:2", "rsimp:lp:9"} {
		if !hasCB(fm.allCallbackData(), want) {
			t.Fatalf("нет кнопки %s", want)
		}
	}
	if fm.lastKBButton("rsimp:lp:1") != "✅ Старт" || fm.lastKBButton("rsimp:lp:9") != "✖️ Старый" {
		t.Fatalf("подписи кнопок планов: %q %q", fm.lastKBButton("rsimp:lp:1"), fm.lastKBButton("rsimp:lp:9"))
	}
	if last := fm.last(); !strings.Contains(last, "<b>Старт</b> · 2 подп. → Стандарт") ||
		!strings.Contains(last, "<b>Про</b> · 2 подп. → Премиум") || !strings.Contains(last, "· 1 подп. → не связывать") {
		t.Fatalf("выбор не запомнился:\n%s", last)
	}
	rsTap(t, a, "rsimp:pv")
	if last := fm.last(); !strings.Contains(last, "связано с тарифами: <b>2</b>") {
		t.Fatalf("предпросмотр после выбора:\n%s", last)
	}

	rsTap(t, a, "rsimp:apply")
	report := fm.last()
	if !strings.Contains(report, "Тариф записан: <b>3</b>") || !strings.Contains(report, "Добавлено в списки тарифов: <b>1</b>") {
		t.Fatalf("отчёт:\n%s", report)
	}

	snap := func(id int64) *model.PlanSnapshot {
		u, _ := fs.GetUser(ctx, id)
		if u == nil {
			t.Fatalf("%d: нет пользователя", id)
		}
		return u.Snapshot
	}
	// Условия — из подписки remnashop, а не из тарифа бота.
	if s := snap(1000000011); s == nil || s.Code != "std" || s.Name != "Стандарт" || s.DeviceLimit != 10 || s.TrafficGB != 100 ||
		len(s.IntSquads) != 1 || s.Price != "" || s.Months != 0 {
		t.Fatalf("11: %+v", s)
	}
	if s := snap(1000000016); s == nil || s.Code != "std" || s.Strategy != "MONTH" { // отключённая и истёкшая
		t.Fatalf("16: %+v", s)
	}
	if s := snap(1000000019); s == nil || s.Code != "prem" {
		t.Fatalf("19: %+v", s)
	}
	if ok, _ := fs.HasPlanAccess(ctx, "prem", 1000000019, ""); !ok {
		t.Fatal("19 не добавлен в список тарифа")
	}
	if s := snap(1000000012); s == nil || s.Code != model.PlanCodeBase || s.Price != "100" {
		t.Fatalf("купленный в боте тариф изменён: %+v", s)
	}
	if ok, _ := fs.HasPlanAccess(ctx, "prem", 1000000012, ""); ok {
		t.Fatal("в список добавлен тот, кому тариф не записан")
	}
	for _, id := range []int64{1000000013, 1000000014, 1000000015} {
		if s := snap(id); s != nil {
			t.Fatalf("%d: записан тариф %+v", id, s)
		}
	}
	// Новый перенос ставит пробной подписке тип «пробный».
	if u, _ := fs.GetUser(ctx, 1000000013); u.NotifyKind != "trial" {
		t.Fatalf("тип пробной подписки: %q", u.NotifyKind)
	}
	if u, _ := fs.GetUser(ctx, 1000000011); u.NotifyKind != "paid" {
		t.Fatalf("тип платной подписки: %q", u.NotifyKind)
	}

	// Повторно, с другим выбором: записанный тариф не меняется.
	rsUpload(t, a)
	rsTap(t, a, "rsimp:ls:1:prem")
	rsTap(t, a, "rsimp:apply")
	if !strings.Contains(fm.last(), "Тариф записан: <b>0</b>") {
		t.Fatalf("повтор:\n%s", fm.last())
	}
	if s := snap(1000000011); s.Code != "std" {
		t.Fatalf("повтор перезаписал тариф: %+v", s)
	}
}

// Без выбора импорт тарифов не пишет, а дамп без планов не показывает кнопку.
func TestRSLink_NothingChosen(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := rsLinkApp(t)
	rsUpload(t, a)
	rsTap(t, a, "rsimp:apply")
	if !strings.Contains(fm.last(), "Тариф записан: <b>0</b>") {
		t.Fatalf("отчёт:\n%s", fm.last())
	}
	for _, id := range []int64{1000000011, 1000000016, 1000000019} {
		if u, _ := fs.GetUser(ctx, id); u == nil || u.Snapshot != nil {
			t.Fatalf("%d: %+v", id, u)
		}
	}

	b, fm2, _ := rsTestApp(t)
	rsTap(t, b, "rsimp:up")
	b.handleDocument(ctx, docMsg(100, "dump-1", int64(len(rsTestDump))))
	if hasCB(fm2.allCallbackData(), "rsimp:lk") || strings.Contains(fm2.last(), "Планов remnashop") {
		t.Fatalf("кнопка связывания без планов:\n%s", fm2.last())
	}
	rsTap(t, b, "rsimp:apply")
	if strings.Contains(fm2.last(), "Тариф записан") {
		t.Fatalf("строка тарифов в отчёте без планов:\n%s", fm2.last())
	}
	// Кнопки связывания без дампа ведут на экран переезда.
	rsTap(t, b, "rsimp:ls:1:base")
	if !strings.Contains(fm2.last(), "Переезд с remnashop") {
		t.Fatalf("без дампа:\n%s", fm2.last())
	}
}

// rsFailStore — база, которая не отдаёт или не записывает конкретных людей.
type rsFailStore struct {
	*fakeStore
	failGet, failSnap int64
	// failPlans — тарифы не читаются (меняется по ходу теста).
	failPlans *bool
}

func (f rsFailStore) ListPlans(ctx context.Context) ([]model.Plan, error) {
	if f.failPlans != nil && *f.failPlans {
		return nil, errors.New("база недоступна")
	}
	return f.fakeStore.ListPlans(ctx)
}

func (f rsFailStore) GetUser(ctx context.Context, id int64) (*model.User, error) {
	if id == f.failGet {
		return nil, errors.New("база недоступна")
	}
	return f.fakeStore.GetUser(ctx, id)
}

func (f rsFailStore) SetUserSnapshot(ctx context.Context, id int64, s *model.PlanSnapshot) error {
	if id == f.failSnap {
		return errors.New("база недоступна")
	}
	return f.fakeStore.SetUserSnapshot(ctx, id, s)
}

func (f rsFailStore) GrantPlanAccess(context.Context, string, int64, string) error {
	return errors.New("база недоступна")
}

// Сбои записи тарифа и допуска к списку попадают в отчёт.
func TestRSLink_FailuresReported(t *testing.T) {
	a, fm, fs := rsLinkApp(t)
	a.store = rsFailStore{fakeStore: fs, failGet: 1000000011, failSnap: 1000000016}
	rsUpload(t, a)
	rsTap(t, a, "rsimp:ls:1:std")
	rsTap(t, a, "rsimp:ls:2:prem")
	rsTap(t, a, "rsimp:apply")
	// 11 не прочитан, 16 не записан, 19 записан, но в список не попал.
	report := fm.last()
	if !strings.Contains(report, "Тариф записан: <b>1</b>") || !strings.Contains(report, "Ошибок при записи: 3") ||
		strings.Contains(report, "Добавлено в списки") {
		t.Fatalf("отчёт:\n%s", report)
	}
}

// Старому пользователю, которому импорт продлевает срок пробной подпиской,
// ставится тип «пробный»; кому срок не меняется — тип прежний.
func TestRSImport_TrialKindExisting(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := rsLinkApp(t)
	_ = fs.UpsertUser(ctx, 1000000013)
	fs.users[1000000013].SubExpireAt = "2026-01-01T00:00:00Z"
	fs.users[1000000013].NotifyKind = "paid"
	_ = fs.UpsertUser(ctx, 1000000011)
	fs.users[1000000011].SubExpireAt = "2030-12-17T11:46:25Z"
	fs.users[1000000011].NotifyKind = "paid"
	rsUpload(t, a)
	rsTap(t, a, "rsimp:apply")
	if u, _ := fs.GetUser(ctx, 1000000013); u.NotifyKind != "trial" || u.SubExpireAt != "2030-11-20T00:00:00Z" {
		t.Fatalf("13: %q %s\n%s", u.NotifyKind, u.SubExpireAt, fm.last())
	}
	if u, _ := fs.GetUser(ctx, 1000000011); u.NotifyKind != "paid" {
		t.Fatalf("11: %q", u.NotifyKind)
	}
}

// Кнопка плана с длинным именем из дампа влезает в ширину телефона.
func TestRSLink_ButtonFitsPhone(t *testing.T) {
	long := strings.Repeat("Безлимитный семейный ", 6)
	for _, name := range []string{"Старт", long} {
		got := rsBtnLabel("❓ ", name)
		if btnWidth(models.InlineKeyboardButton{Text: got}) > rowCap(1) || !strings.HasPrefix(got, "❓ ") {
			t.Fatalf("%q не влезает", got)
		}
		if name == long && !strings.HasSuffix(got, "…") {
			t.Fatalf("длинное имя не обрезано: %q", got)
		}
		if name == "Старт" && got != "❓ Старт" {
			t.Fatalf("короткое имя изменено: %q", got)
		}
	}
}

// База не отдала тарифы: выбор не теряется молча — экраны говорят о сбое,
// а при импорте каждый, кому выбран тариф, попадает в ошибки.
func TestRSLink_PlansUnreadable(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := rsLinkApp(t)
	fail := false
	a.store = rsFailStore{fakeStore: fs, failPlans: &fail}
	rsUpload(t, a)
	rsTap(t, a, "rsimp:ls:1:std")
	rsTap(t, a, "rsimp:ls:2:prem")
	rsTap(t, a, "rsimp:ls:9:-")

	fail = true
	for _, tap := range []string{"rsimp:lk", "rsimp:lp:1", "rsimp:ls:1:prem"} {
		rsTap(t, a, tap)
		if last := fm.last(); !strings.Contains(last, "Сервис временно недоступен") || !hasCB(fm.allCallbackData(), "rsimp:pv") {
			t.Fatalf("%s при сбое:\n%s", tap, last)
		}
	}
	rsTap(t, a, "rsimp:pv")
	if last := fm.last(); !strings.Contains(last, "Сервис временно недоступен") || fm.lastKBButton("rsimp:lk") != "🔗 Тарифы" {
		t.Fatalf("предпросмотр при сбое:\n%s", last)
	}

	// Нажатие во время сбоя выбор не поменяло.
	fail = false
	rsTap(t, a, "rsimp:lk")
	if last := fm.last(); !strings.Contains(last, "<b>Старт</b> · 2 подп. → Стандарт") {
		t.Fatalf("выбор после сбоя:\n%s", last)
	}

	fail = true
	rsTap(t, a, "rsimp:apply")
	// Тариф записался бы 11, 16 (Старт) и 19 (Про); 12 купил в боте — его
	// тариф не меняется и при исправной базе, это не ошибка.
	if report := fm.last(); !strings.Contains(report, "Тариф записан: <b>0</b>") || !strings.Contains(report, "Ошибок при записи: 3") {
		t.Fatalf("отчёт:\n%s", report)
	}
	for _, id := range []int64{1000000011, 1000000016, 1000000019} {
		if u, _ := fs.GetUser(ctx, id); u.Snapshot != nil {
			t.Fatalf("%d: тариф записан при сбое", id)
		}
	}
}
