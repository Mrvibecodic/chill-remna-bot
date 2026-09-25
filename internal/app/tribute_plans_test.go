package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
	"remnabot/internal/tribute"
)

const (
	trbBaseURL = "https://t.me/tribute/app?startapp=sBase"
	trbVipURL  = "https://t.me/tribute/app?startapp=sVip"
)

// trbChatApp — витрина с «Базовым» из сетки и тарифом VIP; Tribute включён,
// ссылка «Базового» задана.
func trbChatApp(t *testing.T) (*App, *fakeMsg, *fakeStore, *model.Plan) {
	t.Helper()
	a, fm, fs := planAdminApp(t)
	if err := a.syncBasePlan(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := vipPlan(t, fs, model.PlanAvailAll)
	a.botCfg.Tribute = model.TributeConfig{Enabled: true, APIKey: "key", PayURL: trbBaseURL}
	return a, fm, fs, p
}

// Корень пропажи: «Базовый» с витрины приходит на экран способов строкой
// тарифа, и проверка «Plan пустой» прятала Tribute всегда.
func TestTributeButton_BaseFromStorefront(t *testing.T) {
	ctx := context.Background()
	a, fm, _, _ := trbChatApp(t)
	const uid int64 = 1000000001

	a.handleCallback(ctx, cb(uid, "plb:"+model.PlanCodeBase+":1"))
	if !hasCB(fm.allCallbackData(), "method:trb") {
		t.Fatalf("у «Базового» нет кнопки Tribute: %v", fm.allCallbackData())
	}
	a.handleCallback(ctx, cb(uid, "method:trb"))
	if !hasCB(fm.allURLs(), trbBaseURL) {
		t.Fatalf("покупателю не ушла ссылка «Базового»: %v (последнее: %q)", fm.allURLs(), fm.last())
	}
	if !strings.Contains(fm.last(), "Базовый") {
		t.Fatalf("в приглашении к оплате нет имени тарифа: %q", fm.last())
	}
}

// Тариф с привязанной подпиской получает свою ссылку; без ID подписки ссылки
// нет — вебхук по такой оплате выдал бы «Базовый».
func TestTributeButton_PlanNeedsBinding(t *testing.T) {
	ctx := context.Background()
	a, fm, _, p := trbChatApp(t)
	const uid int64 = 1000000002

	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if hasCB(fm.allCallbackData(), "method:trb") {
		t.Fatal("у тарифа без привязки не должно быть кнопки Tribute")
	}

	// Только ссылка, без ID подписки — кнопки всё ещё нет.
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, URL: trbVipURL}}
	fm2 := &fakeMsg{}
	a.msg = fm2
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if hasCB(fm2.allCallbackData(), "method:trb") {
		t.Fatal("ссылка без ID подписки не должна давать кнопку Tribute")
	}

	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	fm3 := &fakeMsg{}
	a.msg = fm3
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if !hasCB(fm3.allCallbackData(), "method:trb") {
		t.Fatalf("у привязанного тарифа нет кнопки Tribute: %v", fm3.allCallbackData())
	}
	a.handleCallback(ctx, cb(uid, "method:trb"))
	urls := fm3.allURLs()
	if !hasCB(urls, trbVipURL) || hasCB(urls, trbBaseURL) {
		t.Fatalf("тарифу ушла не его ссылка: %v", urls)
	}
}

// Выключенный Tribute прячет кнопку у всех.
func TestTributeButton_Disabled(t *testing.T) {
	ctx := context.Background()
	a, fm, _, p := trbChatApp(t)
	a.botCfg.Tribute.Enabled = false
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	a.handleCallback(ctx, cb(1000000003, "plb:"+model.PlanCodeBase+":1"))
	a.handleCallback(ctx, cb(1000000003, "plb:"+p.Code+":1"))
	if hasCB(fm.allCallbackData(), "method:trb") {
		t.Fatal("выключенный Tribute не должен показываться")
	}
}

// Мини-апп и кабинет: признак tribute у тарифа и ссылка по коду тарифа;
// аккаунту кабинета без Telegram Tribute не выдать.
func TestTributeMini(t *testing.T) {
	ctx := context.Background()
	a, _, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}

	flags := map[string]bool{}
	for _, pd := range a.MiniPlans(ctx, 1000000004).Plans {
		flags[pd.Code] = pd.Tribute
	}
	if !flags[model.PlanCodeBase] || !flags[p.Code] {
		t.Fatalf("признак tribute у тарифов: %v", flags)
	}
	for _, pd := range a.MiniPlans(ctx, -1000000004).Plans {
		if pd.Tribute {
			t.Fatalf("аккаунту без Telegram показан Tribute у %s", pd.Code)
		}
	}

	url, _, err := a.miniPayURLCore(ctx, 1000000004, &sale{Plan: p, D: p.Duration(1), Months: 1}, model.PayMethodTribute, false)
	if err != nil || url != trbVipURL {
		t.Fatalf("ссылка тарифа: %q, %v", url, err)
	}
	url, _, err = a.miniPayURLCore(ctx, 1000000004, baseSale(1), model.PayMethodTribute, false)
	if err != nil || url != trbBaseURL {
		t.Fatalf("ссылка «Базового»: %q, %v", url, err)
	}
	if _, _, err := a.miniPayURLCore(ctx, -1000000004, baseSale(1), model.PayMethodTribute, true); err == nil {
		t.Fatal("аккаунту без Telegram ссылка Tribute выдаваться не должна")
	}
}

// trbSaleApp — выдача по вебхуку с тарифом VIP в хранилище.
func trbSaleApp(t *testing.T) (*App, *fakeStore, *fakeMsg, *model.Plan, *map[string]any) {
	t.Helper()
	patched := new(map[string]any)
	a, fs := tributeSaleApp(t, patched)
	fm := &fakeMsg{}
	a.msg = fm
	p := vipPlan(t, fs, model.PlanAvailAll)
	return a, fs, fm, p, patched
}

func trbPaid(sub int64, period string, uid int64) map[string]any {
	return map[string]any{
		"subscription_id": sub, "subscription_name": "VIP-канал", "period": period, "type": "regular",
		"price": 99000, "currency": "rub", "telegram_user_id": uid,
		"expires_at": "2030-02-01T00:00:00Z",
	}
}

// Привязанная подписка выдаёт свой тариф с его условиями.
func TestTributeWebhook_BoundPlan(t *testing.T) {
	a, fs, _, p, patched := trbSaleApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	const uid int64 = 555
	if handled, err := tributeEvent(t, a, trbPaid(42, "monthly", uid)); err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if *patched == nil {
		t.Fatal("подписка не выдана")
	}
	if got := (*patched)["hwidDeviceLimit"]; got != float64(7) {
		t.Fatalf("лимит устройств не из тарифа VIP: %v", got)
	}
	u, _ := fs.GetUser(context.Background(), uid)
	if u == nil || u.Snapshot == nil || u.Snapshot.Code != p.Code {
		t.Fatalf("снимок сделки не того тарифа: %+v", u)
	}
}

// Неделя по подписке тарифа: условия месячного срока тарифа, выдача днями.
func TestTributeWebhook_BoundPlanWeekly(t *testing.T) {
	a, fs, _, p, _ := trbSaleApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	const uid int64 = 555
	if _, err := tributeEvent(t, a, trbPaid(42, "weekly", uid)); err != nil {
		t.Fatal(err)
	}
	u, _ := fs.GetUser(context.Background(), uid)
	if u == nil || u.Snapshot == nil || u.Snapshot.Code != p.Code || u.Snapshot.Days != 7 {
		t.Fatalf("неделя тарифа выдана неверно: %+v", u)
	}
}

// Отказы: срока нет в тарифе, тариф удалён, подписка чужая при заданном ID
// «Базового». Подписка не выдаётся, админ получает причину без поломанного
// шаблона.
func TestTributeWebhook_Rejections(t *testing.T) {
	cases := []struct {
		name  string
		links []model.TributeLink
		sub   int64
		per   string
		want  string
	}{
		{"нет срока", []model.TributeLink{{Plan: "vipvipvipvip", SubID: 42, URL: trbVipURL}}, 42, "quarterly", "нет срока 3 мес."},
		{"тариф удалён", []model.TributeLink{{Plan: "gonegonegone", SubID: 42, URL: trbVipURL}}, 42, "monthly", "которого больше нет"},
		{"чужая подписка", []model.TributeLink{{Plan: model.PlanCodeBase, SubID: 7}}, 99, "monthly", "не привязана ни к одному тарифу"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, fm, _, patched := trbSaleApp(t)
			a.botCfg.Tribute.Links = c.links
			handled, err := tributeEvent(t, a, trbPaid(c.sub, c.per, 555))
			if err != nil || !handled {
				t.Fatalf("отказ должен отвечать 200: handled=%v err=%v", handled, err)
			}
			if *patched != nil {
				t.Fatal("подписка выдана вопреки отказу")
			}
			all := strings.Join(fm.texts, "\n")
			if !strings.Contains(all, c.want) {
				t.Fatalf("админ не получил причину %q: %q", c.want, all)
			}
			if strings.Contains(all, "%!") {
				t.Fatalf("сломанный шаблон уведомления: %q", all)
			}
		})
	}
}

// Без ID подписки у «Базового» непривязанная подписка выдаёт «Базовый», как
// до появления привязок.
func TestTributeWebhook_UnboundFallsBackToBase(t *testing.T) {
	a, fs, _, p, patched := trbSaleApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	const uid int64 = 555
	if _, err := tributeEvent(t, a, trbPaid(99, "monthly", uid)); err != nil {
		t.Fatal(err)
	}
	if *patched == nil {
		t.Fatal("подписка не выдана")
	}
	u, _ := fs.GetUser(context.Background(), uid)
	if u == nil || u.Snapshot == nil || u.Snapshot.Code != model.PlanCodeBase {
		t.Fatalf("ожидался «Базовый»: %+v", u)
	}
}

// Повторная доставка уже выданной оплаты молчит, даже если привязки с тех пор
// поменялись и новая доставка была бы отказом.
func TestTributeWebhook_DuplicateBeforeRejection(t *testing.T) {
	a, _, fm, _, _ := trbSaleApp(t)
	ev := trbPaid(99, "monthly", 555)
	if _, err := tributeEvent(t, a, ev); err != nil {
		t.Fatal(err)
	}
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: model.PlanCodeBase, SubID: 7}}
	before := len(fm.texts)
	if handled, err := tributeEvent(t, a, ev); err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if after := strings.Join(fm.texts[before:], "\n"); strings.Contains(after, "не выдана") {
		t.Fatalf("повтор выданной оплаты позвал админа: %q", after)
	}
}

// trbAPI — стаб API Tribute со списком подписок.
func trbAPI(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"result":[
			{"subscriptionId":42,"name":"VIP-канал","currency":"rub","periods":[{"periodId":1,"period":"monthly"},{"periodId":2,"period":"quarterly"}]},
			{"subscriptionId":7,"name":"Базовый канал","currency":"rub","periods":[{"periodId":3,"period":"monthly"}]}
		]}`))
	}))
	t.Cleanup(srv.Close)
	old := tribute.BaseURL
	tribute.BaseURL = srv.URL
	t.Cleanup(func() { tribute.BaseURL = old })
}

// Привязка из админки: список из API, выбор подписки, ссылка, отказ чужой
// подписке, отвязка и освобождение при удалении тарифа.
func TestTributeAdmin_BindFlow(t *testing.T) {
	ctx := context.Background()
	trbAPI(t)
	a, fm, fs, p := trbChatApp(t)

	planTap(t, a, "menu:tribute")
	planTap(t, a, "trb:plans")
	if !hasCB(fm.allCallbackData(), "trb:pl:"+p.Code) || !hasCB(fm.allCallbackData(), "trb:pl:"+model.PlanCodeBase) {
		t.Fatalf("в списке нет тарифов: %v", fm.allCallbackData())
	}
	planTap(t, a, "trb:ls:"+p.Code)
	if !hasCB(fm.allCallbackData(), "trb:ps:"+p.Code+":42") {
		t.Fatalf("нет подписки из API: %v (%q)", fm.allCallbackData(), fm.last())
	}
	planTap(t, a, "trb:ps:"+p.Code+":42")
	l := a.tributeCfg().LinkFor(p.Code)
	if l == nil || l.SubID != 42 || l.Name != "VIP-канал" || len(l.Periods) != 2 {
		t.Fatalf("привязка не сохранилась: %+v", l)
	}
	// Срока на 3 месяца у VIP нет — экран предупреждает.
	if !strings.Contains(fm.last(), "quarterly") || !strings.Contains(fm.last(), "будет отклонена") {
		t.Fatalf("нет предупреждения о непокрытом периоде: %q", fm.last())
	}

	planTap(t, a, "trb:u:"+p.Code)
	a.handleMessage(ctx, msgText(planAdmin, trbVipURL))
	if got := a.tributeURLFor(p.Code); got != trbVipURL {
		t.Fatalf("ссылка тарифа не сохранилась: %q", got)
	}

	// Та же подписка «Базовому» — отказ.
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":42")
	if l := a.tributeCfg().LinkFor(model.PlanCodeBase); l != nil {
		t.Fatalf("одна подписка привязалась к двум тарифам: %+v", l)
	}

	// ID вручную для «Базового» включает строгий режим.
	planTap(t, a, "trb:id:"+model.PlanCodeBase)
	a.handleMessage(ctx, msgText(planAdmin, "7"))
	if !a.tributeCfg().StrictSubs() {
		t.Fatal("ID «Базового» не сохранился")
	}
	planTap(t, a, "trb:rm:"+model.PlanCodeBase)
	if a.tributeCfg().StrictSubs() || a.tributeCfg().PayURL != trbBaseURL {
		t.Fatal("отвязка «Базового» должна снять ID и оставить ссылку")
	}

	// Удаление тарифа привязку НЕ снимает: оплата по его подписке должна
	// отклоняться с уведомлением, а не молча выдавать «Базовый». Подписка
	// видна в списке и забывается явной кнопкой.
	a.deletePlan(ctx, planAdmin, p.Code)
	if got, _ := fs.GetPlan(ctx, p.Code); got != nil {
		t.Fatal("тариф не удалён")
	}
	if l := a.tributeCfg().LinkBySub(42); l == nil {
		t.Fatal("привязка удалённого тарифа пропала")
	}
	planTap(t, a, "trb:plans")
	if !hasCB(fm.allCallbackData(), "trb:fg:42") {
		t.Fatalf("подписки удалённого тарифа нет в списке: %v", fm.allCallbackData())
	}
	planTap(t, a, "trb:fg:42")
	if a.tributeCfg().LinkBySub(42) == nil {
		t.Fatal("подписка забыта без подтверждения")
	}
	planTap(t, a, "trb:fgy:42")
	if a.tributeCfg().LinkBySub(42) != nil {
		t.Fatal("подписка не забыта")
	}
}

// Отказ API не ломает экран: ошибка без ключа и путь к ручному вводу.
func TestTributeAdmin_ListFail(t *testing.T) {
	trbAPI(t)
	a, fm, _, p := trbChatApp(t)
	a.botCfg.Tribute.APIKey = "wrong-secret-key"
	planTap(t, a, "trb:ls:"+p.Code)
	if !strings.Contains(fm.last(), "не получен") || strings.Contains(fm.last(), "wrong-secret-key") {
		t.Fatalf("экран отказа: %q", fm.last())
	}
	if !hasCB(fm.allCallbackData(), "trb:id:"+p.Code) {
		t.Fatal("нет кнопки ручного ввода ID")
	}
}

// Не-админ не управляет привязками.
func TestTributeAdmin_NotForUsers(t *testing.T) {
	a, _, _, p := trbChatApp(t)
	a.handleCallback(context.Background(), cb(1000000010, "trb:rm:"+model.PlanCodeBase))
	a.handleCallback(context.Background(), cb(1000000010, "trb:ps:"+p.Code+":42"))
	if len(a.tributeCfg().Links) != 0 {
		t.Fatal("пользователь изменил привязки")
	}
}

// Битая ссылка не сохраняется: кнопку с ней Telegram отверг бы вместе со всем
// сообщением.
func TestTributeAdmin_BadURL(t *testing.T) {
	a, fm, _, p := trbChatApp(t)
	planTap(t, a, "trb:u:"+p.Code)
	a.handleMessage(context.Background(), msgText(planAdmin, "не ссылка"))
	if l := a.tributeCfg().LinkFor(p.Code); l != nil {
		t.Fatalf("битая ссылка сохранилась: %+v", l)
	}
	if !strings.Contains(fm.last(), "не сохранена") {
		t.Fatalf("нет объяснения отказа: %q", fm.last())
	}
}

// «Отмена» ввода возвращает на экран тарифа, а не в главное меню.
func TestTributeAdmin_CancelInput(t *testing.T) {
	a, fm, _, p := trbChatApp(t)
	planTap(t, a, "trb:id:"+p.Code)
	planTap(t, a, "inp:cancel")
	if !strings.Contains(fm.last(), "Tribute → ") || !strings.Contains(fm.last(), "VIP") {
		t.Fatalf("отмена увела не на экран тарифа: %q", fm.last())
	}
	if ui := a.getUI(planAdmin); ui.adminInput != "" || ui.planCode != "" {
		t.Fatalf("ожидание ввода не снято: %+v", ui)
	}
}

// Перепривязка тарифа: новая подписка — текущая (ссылка прежней к ней не
// переезжает), прежняя остаётся за тарифом и продления по ней выдают его же.
func TestTributeRebind_OldKeepsPlan(t *testing.T) {
	trbAPI(t)
	a, _, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 7, URL: trbVipURL}}
	planTap(t, a, "trb:ps:"+p.Code+":42")
	cfg := a.tributeCfg()
	cur := cfg.LinkFor(p.Code)
	if cur == nil || cur.SubID != 42 || cur.URL != "" {
		t.Fatalf("текущая привязка: %+v", cur)
	}
	if old := cfg.LinkBySub(7); old == nil || !old.Old || old.Plan != p.Code || old.URL != "" {
		t.Fatalf("прежняя подписка: %+v", old)
	}
	if a.tributeURLFor(p.Code) != "" {
		t.Fatal("ссылка прежней подписки не должна продавать новую")
	}

	// Продление по прежней подписке выдаёт тариф, а не «Базовый».
	patched := new(map[string]any)
	srv := snapPanel(t, patched)
	sa, fs := snapApp(t, srv.URL)
	sa.botCfg.Tribute = a.botCfg.Tribute.Clone()
	sa.botCfg.Tribute.Enabled, sa.botCfg.Tribute.APIKey = true, "key"
	sa.msg = &fakeMsg{}
	_ = fs.SavePlan(context.Background(), p)
	if _, err := tributeSigned(t, sa, map[string]any{"name": "renewed_subscription", "payload": trbPaid(7, "monthly", 555)}); err != nil {
		t.Fatal(err)
	}
	u, _ := fs.GetUser(context.Background(), 555)
	if u == nil || u.Snapshot == nil || u.Snapshot.Code != p.Code {
		t.Fatalf("прежняя подписка выдала не свой тариф: %+v", u)
	}
}

// «Отвязать» убирает кнопку, но подписчики подписки продолжают получать тариф.
func TestTributeUnbind_KeepsIssuing(t *testing.T) {
	a, _, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	planTap(t, a, "trb:rm:"+p.Code)
	cfg := a.tributeCfg()
	if cfg.LinkFor(p.Code) != nil || a.tributeURLFor(p.Code) != "" {
		t.Fatal("после отвязки у тарифа осталась кнопка")
	}
	if l := cfg.LinkBySub(42); l == nil || l.Plan != p.Code || !l.Old {
		t.Fatalf("подписка после отвязки: %+v", l)
	}
}

// Кнопка и приглашение учитывают периоды подписки: неподходящие сроки не
// обещаются, а если не подходит ни один — кнопки нет.
func TestTributeOffer_Periods(t *testing.T) {
	ctx := context.Background()
	a, fm, _, p := trbChatApp(t)
	const uid int64 = 1000000011

	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL, Periods: []string{"quarterly", "onetime"}}}
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	if hasCB(fm.allCallbackData(), "method:trb") {
		t.Fatal("кнопка Tribute при подписке, ни один период которой не выдаётся")
	}

	a.botCfg.Tribute.Links[0].Periods = []string{"monthly", "quarterly", "yearly", "weekly"}
	fm2 := &fakeMsg{}
	a.msg = fm2
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	a.handleCallback(ctx, cb(uid, "method:trb"))
	last := fm2.last()
	for _, want := range []string{"1 месяц", "12 месяцев", "неделя"} {
		if !strings.Contains(last, want) {
			t.Fatalf("в приглашении нет срока %q: %q", want, last)
		}
	}
	if strings.Contains(last, "3 месяца") {
		t.Fatalf("обещан срок, которого в тарифе нет: %q", last)
	}
}

// Старая кнопка Tribute с экрана другого тарифа: честное «недоступно» и экран
// способов, а не «Tribute не настроен».
func TestTributeStaleButton(t *testing.T) {
	ctx := context.Background()
	a, fm, _, p := trbChatApp(t)
	const uid int64 = 1000000012
	a.handleCallback(ctx, cb(uid, "plb:"+p.Code+":1"))
	a.handleCallback(ctx, cb(uid, "method:trb"))
	all := strings.Join(fm.texts, "\n")
	if !strings.Contains(all, "недоступна") || strings.Contains(all, "Tribute пока не настроен") {
		t.Fatalf("ответ на старую кнопку: %q", all)
	}
}

// Смена цены бота не гоняет Tribute по кругу «цена изменилась»: платят цену
// Tribute.
func TestTributeIgnoresBotPriceChange(t *testing.T) {
	ctx := context.Background()
	a, fm, _, _ := trbChatApp(t)
	const uid int64 = 1000000013
	a.handleCallback(ctx, cb(uid, "plb:"+model.PlanCodeBase+":1"))
	a.botCfg.Pricing.Base[1] = "999"
	a.handleCallback(ctx, cb(uid, "method:trb"))
	if !hasCB(fm.allURLs(), trbBaseURL) {
		t.Fatalf("ссылка не выдана после смены цены: %q", fm.last())
	}
}

// E-mail-аккаунт кабинета получает понятный отказ, а не «сервис не отвечает».
func TestTributeMiniEmailError(t *testing.T) {
	a, _, _, _ := trbChatApp(t)
	dto := a.MiniCheckout(context.Background(), -1000000014, model.PlanCodeBase, 1, model.PayMethodTribute, "", true)
	if !strings.Contains(dto.Error, "через Telegram") {
		t.Fatalf("ответ кабинету: %+v", dto)
	}
}

// Навигация по тарифам снимает ожидание ввода ID подписки.
func TestTributeInputClearedByPlanNav(t *testing.T) {
	a, _, _, p := trbChatApp(t)
	planTap(t, a, "trb:id:"+p.Code)
	planTap(t, a, "pln:list")
	if ui := a.getUI(planAdmin); ui.adminInput != "" {
		t.Fatalf("ожидание ввода пережило навигацию: %q", ui.adminInput)
	}
}

// Ошибка API экранируется один раз.
func TestTributeListFailEscapedOnce(t *testing.T) {
	a, fm, _, p := trbChatApp(t)
	old := tribute.BaseURL
	tribute.BaseURL = "http://127.0.0.1:1/\"x\""
	t.Cleanup(func() { tribute.BaseURL = old })
	planTap(t, a, "trb:ls:"+p.Code)
	if strings.Contains(fm.last(), "&amp;") {
		t.Fatalf("двойное экранирование: %q", fm.last())
	}
}

// Апгрейд неделей: остаток дешёвого тарифа конвертируется, а не остаётся
// почти целиком под дорогим тарифом. Нижняя граница — «сейчас + неделя».
func TestTributeWeeklyUpgrade_Converts(t *testing.T) {
	ctx := context.Background()
	patched := new(map[string]any)
	in30 := time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339)
	srv := snapPanelExpiry(t, patched, in30)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Tribute = model.TributeConfig{Enabled: true, APIKey: "key"}
	a.msg = &fakeMsg{}
	p := vipPlan(t, fs, model.PlanAvailAll)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	_ = fs.UpsertUser(ctx, 555)
	_ = fs.SetUserSnapshot(ctx, 555, &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1, Price: "150", Currency: "₽", BoughtDays: 30})
	_ = fs.SetSubExpiry(ctx, 555, in30, "paid")

	if _, err := tributeEvent(t, a, trbPaid(42, "weekly", 555)); err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, fmt.Sprint((*patched)["expireAt"]))
	if err != nil {
		t.Fatalf("панель не получила срок: %v", *patched)
	}
	// 30 дней по 150 ₽ = 150 ₽ → по 33 ₽/день VIP ≈ 4.5 дня + неделя ≈ 12.
	days := time.Until(exp).Hours() / 24
	if days < 10.5 || days > 13 {
		t.Fatalf("VIP выдан на %.1f дн., ожидалось ≈12", days)
	}
}

// Неделя за чужую валюту оценивается долей месячной цены, а не месяцем:
// иначе при смене тарифа она печатала бы дни.
func TestTributeWeekly_PaidEstimate(t *testing.T) {
	a, fs, _, p, _ := trbSaleApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	ev := trbPaid(42, "weekly", 555)
	ev["currency"], ev["price"] = "eur", 300
	if _, err := tributeEvent(t, a, ev); err != nil {
		t.Fatal(err)
	}
	u, _ := fs.GetUser(context.Background(), 555)
	if u == nil || u.Snapshot == nil || u.Snapshot.Paid != "231" || u.Snapshot.WindowPaidK != 23100 {
		t.Fatalf("оценка недели: %+v", u.Snapshot)
	}
}

// Рубли, уплаченные Tribute за долларовый тариф, не становятся долларами.
func TestTributePaid_CurrencyMismatch(t *testing.T) {
	a, fs, _, _, _ := trbSaleApp(t)
	usd := &model.Plan{Code: "usdusdusdusd", Name: "USD", Enabled: true, Currency: "$",
		Durations: []model.PlanDuration{{Months: 1, Base: "10"}}}
	_ = fs.SavePlan(context.Background(), usd)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: usd.Code, SubID: 42, URL: trbVipURL}}
	if _, err := tributeEvent(t, a, trbPaid(42, "monthly", 555)); err != nil {
		t.Fatal(err)
	}
	u, _ := fs.GetUser(context.Background(), 555)
	if u == nil || u.Snapshot == nil || u.Snapshot.Code != usd.Code {
		t.Fatalf("выдача: %+v", u)
	}
	if u.Snapshot.Paid != "" || u.Snapshot.WindowPaidK != 1000 {
		t.Fatalf("рубли легли в долларовый снимок: Paid=%q WindowPaidK=%d", u.Snapshot.Paid, u.Snapshot.WindowPaidK)
	}
}

// Гейт «только новым» оценивается до покупки: платившему раньше покупка по
// пересланной ссылке Tribute доходит до админа.
func TestTributeGateBreach_NewOnly(t *testing.T) {
	a, fs, fm, _, _ := trbSaleApp(t)
	p := vipPlan(t, fs, model.PlanAvailNew)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	markPaid(t, fs, 555, model.PlanCodeBase)
	if _, err := tributeEvent(t, a, trbPaid(42, "monthly", 555)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fm.texts, "\n"), "VIP") || !hasBreach(fm) {
		t.Fatalf("админ не узнал о покупке закрытого тарифа: %q", strings.Join(fm.texts, "\n"))
	}

	// Продление того же тарифа — не пробой.
	fm2 := &fakeMsg{}
	a.msg = fm2
	ev := trbPaid(42, "monthly", 555)
	ev["expires_at"] = "2030-03-01T00:00:00Z"
	if _, err := tributeSigned(t, a, map[string]any{"name": "renewed_subscription", "payload": ev}); err != nil {
		t.Fatal(err)
	}
	if hasBreach(fm2) {
		t.Fatalf("продление своего тарифа посчитано пробоем: %q", strings.Join(fm2.texts, "\n"))
	}
}

func hasBreach(fm *fakeMsg) bool {
	return strings.Contains(strings.Join(fm.texts, "\n"), i18n.T("ru", "plans.breach_revoked"))
}

// Оплата Tribute выключает автосписание картой: иначе за один срок
// списывались бы оба.
func TestTributeTurnsOffCardAutoPay(t *testing.T) {
	ctx := context.Background()
	a, fs, fm, _, _ := trbSaleApp(t)
	_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: 555, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: true,
		Snapshot: &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1}})
	if _, err := tributeEvent(t, a, trbPaid(99, "monthly", 555)); err != nil {
		t.Fatal(err)
	}
	ap, _ := fs.GetAutoPay(ctx, 555)
	if ap == nil || ap.Enabled {
		t.Fatalf("автосписание картой осталось включённым: %+v", ap)
	}
	if !strings.Contains(strings.Join(fm.texts, "\n"), "Автопродление картой выключено") {
		t.Fatalf("покупатель не узнал: %q", strings.Join(fm.texts, "\n"))
	}
}

// Недельная оплата обнуляет трафик только в начале периода (первая покупка,
// смена тарифа): лимит месячный, и сброс на каждом продлении выдавал бы
// месячный объём каждую неделю.
func TestTributeWeekly_TrafficReset(t *testing.T) {
	resets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/actions/reset-traffic") {
			resets++
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-06-01T00:00:00Z"}}`))
	}))
	t.Cleanup(srv.Close)
	a, fs := snapApp(t, srv.URL)
	a.botCfg.Tribute = model.TributeConfig{Enabled: true, APIKey: "key"}
	a.msg = &fakeMsg{}
	week := func(sub int64, exp string) {
		t.Helper()
		ev := trbPaid(sub, "weekly", 555)
		ev["expires_at"] = exp
		if _, err := tributeEvent(t, a, ev); err != nil {
			t.Fatal(err)
		}
	}
	week(99, "2030-01-08T00:00:00Z")
	if resets != 1 {
		t.Fatalf("первая покупка: сбросов %d, ожидался 1", resets)
	}
	week(99, "2030-01-15T00:00:00Z")
	if resets != 1 {
		t.Fatalf("продление того же тарифа неделей сбросило трафик (%d)", resets)
	}
	p := vipPlan(t, fs, model.PlanAvailAll)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	week(42, "2030-01-22T00:00:00Z")
	if resets != 2 {
		t.Fatalf("смена тарифа неделей не сбросила трафик (%d)", resets)
	}
}

// Автосписание переезжает на купленный тариф и тогда, когда оно выключено:
// иначе, включив его обратно, человек продлевал бы прежний тариф.
func TestTributeAutoPayFollowsPlan(t *testing.T) {
	ctx := context.Background()
	for _, enabled := range []bool{true, false} {
		a, fs, _, p, _ := trbSaleApp(t)
		a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
		_ = fs.SetAutoPay(ctx, &model.AutoPay{TelegramID: 555, Method: model.PayMethodYooKassa, MethodID: "pm", Months: 1, Enabled: enabled,
			Snapshot: &model.PlanSnapshot{Code: model.PlanCodeBase, Months: 1}})
		if _, err := tributeEvent(t, a, trbPaid(42, "monthly", 555)); err != nil {
			t.Fatal(err)
		}
		ap, _ := fs.GetAutoPay(ctx, 555)
		if ap == nil || ap.Enabled || planCodeOf(ap.Snapshot) != p.Code {
			t.Fatalf("enabled=%v: автосписание после оплаты Tribute: %+v", enabled, ap)
		}
	}
}

// Платил до появления кодов тарифов — был на «Базовом»: продление «Базового»
// «только новым» не пробой.
func TestGateBreach_LegacyBasePayer(t *testing.T) {
	ctx := context.Background()
	a, fs, fm, _, _ := trbSaleApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	bp, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	if bp == nil {
		t.Fatal("нет строки «Базового»")
	}
	bp.Availability = model.PlanAvailNew
	_ = fs.SavePlan(ctx, bp)
	markPaid(t, fs, 555, "")
	if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodYooKassa, "150 ₽", "yk-legacy-1", nil); err != nil {
		t.Fatal(err)
	}
	if hasBreach(fm) {
		t.Fatalf("продление старого плательщика посчитано пробоем: %q", strings.Join(fm.texts, "\n"))
	}
}

// Нерублёвая сетка: сумма других способов подписана «₽», число — в валюте
// сетки; фактически уплаченное записывается, как раньше.
func TestPaid_NonRubGridOtherMethods(t *testing.T) {
	ctx := context.Background()
	a, fs, _, _, _ := trbSaleApp(t)
	snap := a.planSnapshot(1)
	snap.Currency = "UAH"
	if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodP2P, "80 ₽", "p2p-uah-1", snap); err != nil {
		t.Fatal(err)
	}
	u, _ := fs.GetUser(ctx, 555)
	if u == nil || u.Snapshot == nil || u.Snapshot.Paid != "80" {
		t.Fatalf("Paid в нерублёвой сетке: %+v", u.Snapshot)
	}
}

// Смена подписки «Базового» сбрасывает его ссылку; первая привязка ID — нет.
func TestTributeBaseRebindClearsLink(t *testing.T) {
	trbAPI(t)
	a, _, _, _ := trbChatApp(t)
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":7")
	if a.tributeCfg().PayURL != trbBaseURL {
		t.Fatal("первая привязка ID стёрла ссылку «Базового»")
	}
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":42")
	cfg := a.tributeCfg()
	if cfg.PayURL != "" {
		t.Fatalf("ссылка «Базового» пережила смену подписки: %q", cfg.PayURL)
	}
	if l := cfg.LinkBySub(7); l == nil || !l.Old || l.Plan != model.PlanCodeBase {
		t.Fatalf("прежняя подписка «Базового»: %+v", l)
	}
}

// Ссылка, введённая после «Отвязать», становится текущей, а не уходит в
// прежнюю подписку.
func TestTributeURLAfterUnbind(t *testing.T) {
	a, _, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL}}
	planTap(t, a, "trb:rm:"+p.Code)
	planTap(t, a, "trb:u:"+p.Code)
	a.handleMessage(context.Background(), msgText(planAdmin, "https://t.me/tribute/app?startapp=sNew"))
	cfg := a.tributeCfg()
	if l := cfg.LinkFor(p.Code); l == nil || l.URL != "https://t.me/tribute/app?startapp=sNew" || l.SubID != 0 {
		t.Fatalf("текущая привязка: %+v", l)
	}
	if l := cfg.LinkBySub(42); l == nil || !l.Old || l.URL != "" {
		t.Fatalf("прежняя подписка тронута: %+v", l)
	}
}

// Счётчик на главном экране — тарифы с кнопкой, без прежних подписок и
// удалённых тарифов.
func TestTributeTitleCount(t *testing.T) {
	a, fm, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{
		{Plan: p.Code, SubID: 42, URL: trbVipURL},
		{Plan: p.Code, SubID: 43, Old: true},
		{Plan: "gonegonegone", SubID: 44, URL: trbVipURL},
	}
	planTap(t, a, "menu:tribute")
	if !strings.Contains(fm.last(), "Tribute: 1\n") {
		t.Fatalf("счётчик тарифов: %q", fm.last())
	}
}

// Мини-апп: сроки подписки в витрине и без сверки цены бота у Tribute.
func TestTributeMiniTermsAndPrice(t *testing.T) {
	ctx := context.Background()
	a, _, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42, URL: trbVipURL, Periods: []string{"monthly"}}}
	var terms []string
	for _, pd := range a.MiniPlans(ctx, 1000000015).Plans {
		if pd.Code == p.Code {
			terms = pd.TributeTerms
		}
	}
	if len(terms) != 1 || terms[0] != "1 месяц" {
		t.Fatalf("сроки Tribute в витрине: %v", terms)
	}
	dto := a.MiniCheckout(ctx, 1000000015, p.Code, 1, model.PayMethodTribute, "1", false)
	if !dto.OK || dto.PayURL != trbVipURL {
		t.Fatalf("Tribute отказал из-за цены бота: %+v", dto)
	}
}

// Исключение для старых плательщиков — только «только новым»: в режиме «по
// списку» продление без места в списке остаётся пробоем.
func TestGateBreach_LegacyListStillBreach(t *testing.T) {
	ctx := context.Background()
	a, fs, fm, _, _ := trbSaleApp(t)
	if err := a.syncBasePlan(ctx); err != nil {
		t.Fatal(err)
	}
	bp, _ := fs.GetPlan(ctx, model.PlanCodeBase)
	bp.Availability = model.PlanAvailList
	_ = fs.SavePlan(ctx, bp)
	markPaid(t, fs, 555, "")
	if _, _, err := a.finalizePurchase(ctx, 555, 1, model.PayMethodYooKassa, "150 ₽", "yk-legacy-2", nil); err != nil {
		t.Fatal(err)
	}
	if !hasBreach(fm) {
		t.Fatalf("пробой списка скрыт: %q", strings.Join(fm.texts, "\n"))
	}
}

// «Отвязать», затем новая подписка «Базового» — ссылка прежней сбрасывается.
func TestTributeBaseUnbindThenRebind(t *testing.T) {
	trbAPI(t)
	a, _, _, _ := trbChatApp(t)
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":7")
	planTap(t, a, "trb:rm:"+model.PlanCodeBase)
	if a.tributeCfg().PayURL != trbBaseURL {
		t.Fatal("отвязка стёрла ссылку «Базового»")
	}
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":42")
	if got := a.tributeCfg().PayURL; got != "" {
		t.Fatalf("ссылка на прежнюю подписку осталась: %q", got)
	}
}

// Отказ «подписка занята» подсказывает путь: у текущей — «Отвязать», у
// прежней — сразу ♻️.
func TestTributeSubTakenTexts(t *testing.T) {
	trbAPI(t)
	a, fm, _, p := trbChatApp(t)
	a.botCfg.Tribute.Links = []model.TributeLink{{Plan: p.Code, SubID: 42}, {Plan: p.Code, SubID: 7, Old: true}}
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":42")
	if !strings.Contains(fm.last(), "Отвязать") {
		t.Fatalf("текущая подписка: %q", fm.last())
	}
	planTap(t, a, "trb:ps:"+model.PlanCodeBase+":7")
	if !strings.Contains(fm.last(), "прежняя") {
		t.Fatalf("прежняя подписка: %q", fm.last())
	}
}
