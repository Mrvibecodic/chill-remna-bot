package app

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

// Своя сумма пополнения — как в чате: любая до потолка.
func TestMiniTopUp_CustomAmount(t *testing.T) {
	ctx := context.Background()
	a, _ := planApp(t)
	opts := a.MiniTopUpOptions(ctx, 555)
	if opts.MaxKopecks != 140000 {
		t.Fatalf("потолок своей суммы: %d", opts.MaxKopecks)
	}
	if r := a.MiniTopUp(ctx, 555, 140001, "yk", false); r.Error != "недопустимая сумма" {
		t.Fatalf("сумма выше потолка обязана отклоняться: %+v", r)
	}
	if r := a.MiniTopUp(ctx, 555, 0, "yk", false); r.Error != "недопустимая сумма" {
		t.Fatalf("нулевая сумма обязана отклоняться: %+v", r)
	}
	// Не пресет, но в пределах потолка: проверку суммы проходит (дальше
	// падает на не настроенной платёжке — это уже не отказ по сумме).
	if r := a.MiniTopUp(ctx, 555, 12345, "yk", false); r.Error == "недопустимая сумма" {
		t.Fatalf("своя сумма отклонена: %+v", r)
	}
}

// Тариф «по ссылке»: свой — продлевается из мини-аппа; чужой — только после
// открытия его ссылки.
func TestMiniLinkPlan_OwnAndOpened(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink)
	const own, other int64 = 1000000001, 1000000002
	_ = fs.UpsertUser(ctx, own)
	_ = fs.UpsertUser(ctx, other)
	fs.users[own].Snapshot = &model.PlanSnapshot{Code: p.Code, Months: 1, Price: "990"}

	has := func(uid int64) bool {
		for _, pd := range a.MiniPlans(ctx, uid).Plans {
			if pd.Code == p.Code {
				return pd.Own
			}
		}
		return false
	}
	if !has(own) {
		t.Fatal("свой тариф «по ссылке» обязан быть на витрине с отметкой")
	}
	if has(other) {
		t.Fatal("чужой тариф «по ссылке» на витрине")
	}
	if a.miniSale(ctx, own, p.Code, 1) == nil {
		t.Fatal("продление своего тарифа закрыто")
	}
	if a.miniSale(ctx, other, p.Code, 1) != nil {
		t.Fatal("тариф по ссылке продаётся без открытия ссылки")
	}
	if m := a.MiniMenu(ctx, own, false); m.OwnPlan != p.Code || m.RenewGone {
		t.Fatalf("меню: свой тариф %+v", m)
	}

	dto := a.MiniPlanLink(ctx, other, p.Code)
	if len(dto.Plans) != 1 || dto.Plans[0].Code != p.Code {
		t.Fatalf("ссылка тарифа не открыла его: %+v", dto)
	}
	if a.miniSale(ctx, other, p.Code, 1) == nil {
		t.Fatal("открытый по ссылке тариф не продаётся")
	}
}

// Перебор ссылок упирается в тот же лимит, что в чате, и после него даже
// верный код не открывается.
func TestMiniPlanLink_Throttle(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	p := vipPlan(t, fs, model.PlanAvailLink)
	const uid int64 = 1000000003
	for i := 0; i < planLinkFailLimit; i++ {
		if d := a.MiniPlanLink(ctx, uid, "nosuchplan"+itoa(i)); len(d.Plans) != 0 || d.Notice == "" {
			t.Fatalf("неизвестный код: %+v", d)
		}
	}
	if d := a.MiniPlanLink(ctx, uid, p.Code); len(d.Plans) != 0 {
		t.Fatal("после лимита перебора тариф открылся")
	}
	if a.miniSale(ctx, uid, p.Code, 1) != nil {
		t.Fatal("после лимита перебора тариф продаётся")
	}
}

// Параметр входа: реферал привязывается только новичку, аккаунтам по почте —
// ничего.
func TestMiniStart_Referral(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.NormalizeReferral()
	a.botCfg.Referral.Enabled = true
	const ref, fresh, old int64 = 1000000010, 1000000011, 1000000012
	_ = fs.UpsertUser(ctx, ref)
	_ = fs.UpsertUser(ctx, old)

	a.MiniStart(ctx, fresh, "ref_1000000010", false)
	if u, _ := fs.GetUser(ctx, fresh); u == nil || u.ReferredBy != ref {
		t.Fatalf("новичок не привязан к пригласившему: %+v", u)
	}
	a.MiniStart(ctx, old, "ref_1000000010", false)
	if u, _ := fs.GetUser(ctx, old); u.ReferredBy != 0 {
		t.Fatal("существующий пользователь привязан задним числом")
	}
	a.MiniStart(ctx, -1000000013, "ref_1000000010", false)
	if u, _ := fs.GetUser(ctx, -1000000013); u != nil {
		t.Fatal("аккаунт по почте заведён по ссылке")
	}
}

// Триал в кабинете: вошедшим через Telegram — да, аккаунтам по почте — нет.
func TestMiniMenu_TrialInCabinet(t *testing.T) {
	ctx := context.Background()
	a, _ := planApp(t)
	a.botCfg.Trial.Enabled = true
	a.botCfg.Trial.Days = 3
	if !a.MiniMenu(ctx, 1000000020, true).TrialAvailable {
		t.Fatal("триал вошедшему через Telegram в кабинете не предложен")
	}
	if a.MiniMenu(ctx, -1000000020, true).TrialAvailable {
		t.Fatal("триал предложен аккаунту по почте")
	}
}

// Кошелёк: прогноз по сетке, если своего тарифа нет.
func TestMiniWallet_Forecast(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	const uid int64 = 1000000030
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddBalance(ctx, uid, 45000)
	w := a.MiniWallet(ctx, uid)
	if w.BalanceK != 45000 || len(w.Rows) != 3 {
		t.Fatalf("кошелёк: %+v", w)
	}
	if w.Rows[0].Months != 1 || w.Rows[0].Count != 3 || w.MaxMonths != 3 {
		t.Fatalf("прогноз: %+v", w)
	}
}

// Ручная проверка оплаты добивает счёт и сообщает об оплате; повтор сразу
// же не ходит к платёжкам.
func TestMiniPayCheck(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	const uid int64 = 1000000040
	_ = fs.UpsertUser(ctx, uid)
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay-1", TelegramID: uid, Months: 1})
	_ = fs.AddPayment(ctx, &model.Payment{TelegramID: uid, Method: model.PayMethodYooKassa, Months: 1, Amount: "150", Status: model.PaymentPaid, ExtID: "pay-1"})
	r := a.MiniPayCheck(ctx, uid)
	if !r.OK || !r.Paid || r.Pending != 0 {
		t.Fatalf("оплаченный счёт: %+v", r)
	}
	_ = fs.AddPendingInvoice(ctx, &model.PendingInvoice{Method: model.PayMethodYooKassa, ExtID: "pay-2", TelegramID: uid, Months: 1})
	if r := a.MiniPayCheck(ctx, uid); r.Paid || r.Pending != 1 {
		t.Fatalf("неоплаченный счёт: %+v", r)
	}
	if r := a.MiniPayCheck(ctx, 1000000041); r.Pending != 0 {
		t.Fatalf("чужие счета посчитаны: %+v", r)
	}
}

// Кабинет с одобрением новичков: до одобрения реферал не привязывается.
func TestMiniStart_CabinetApproval(t *testing.T) {
	ctx := context.Background()
	a, fs := planApp(t)
	a.botCfg.NormalizeReferral()
	a.botCfg.Referral.Enabled = true
	a.botCfg.NormalizeCabinet()
	a.botCfg.Cabinet.Approval = model.CabinetApprovalTG
	const ref, fresh int64 = 1000000050, 1000000051
	_ = fs.UpsertUser(ctx, ref)
	a.MiniStart(ctx, fresh, "ref_1000000050", true)
	if u, _ := fs.GetUser(ctx, fresh); u != nil && u.ReferredBy != 0 {
		t.Fatal("реферал привязан до одобрения кабинета")
	}
}
