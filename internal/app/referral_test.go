package app

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

func refTestApp(t *testing.T) (*App, *fakeStore) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeReferral()
	a.botCfg.Referral.Enabled = true
	a.botCfg.Referral.BonusKind = model.ReferralBonusBalance
	a.botCfg.Referral.BonusValue = 50
	a.botCfg.Referral.OnFirstPay = true
	return a, fs
}

func TestReferral_BindAndBonusOnce(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 200)

	a.bindReferrer(ctx, 300, "ref_200")
	if u, _ := fs.GetUser(ctx, 300); u == nil || u.ReferredBy != 200 {
		t.Fatalf("referred_by не привязан: %+v", u)
	}

	a.payReferralBonus(ctx, 300)
	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 5000 {
		t.Fatalf("ожидался бонус 5000 коп, got %d", ref.Balance)
	}
	a.payReferralBonus(ctx, 300)
	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 5000 {
		t.Fatalf("двойное начисление бонуса: %d", ref.Balance)
	}
	if n, _ := fs.CountReferrals(ctx, 200); n != 1 {
		t.Fatalf("ожидался 1 реферал, got %d", n)
	}
}

func TestReferral_SelfAndExistingRejected(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()

	_ = fs.UpsertUser(ctx, 300)
	a.bindReferrer(ctx, 300, "ref_200")
	if u, _ := fs.GetUser(ctx, 300); u.ReferredBy != 0 {
		t.Fatal("существующего пользователя привязывать нельзя")
	}

	_ = fs.UpsertUser(ctx, 400)
	a.bindReferrer(ctx, 500, "ref_500")
	if u, _ := fs.GetUser(ctx, 500); u != nil && u.ReferredBy != 0 {
		t.Fatal("самоприглашение запрещено")
	}
}

func TestReferral_DisabledNoBind(t *testing.T) {
	a, fs := refTestApp(t)
	a.botCfg.Referral.Enabled = false
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 200)
	a.bindReferrer(ctx, 300, "ref_200")
	if u, _ := fs.GetUser(ctx, 300); u != nil && u.ReferredBy != 0 {
		t.Fatal("при выключенной рефералке привязки быть не должно")
	}
}
