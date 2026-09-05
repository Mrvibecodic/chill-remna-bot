package app

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

func TestPromo_RedeemBalanceOnce(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 300)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "LETO", Kind: model.PromoKindBalance, Value: 100, MaxUses: 2})

	a.applyPromo(ctx, 300, "leto")
	if u, _ := fs.GetUser(ctx, 300); u.Balance != 10000 {
		t.Fatalf("ожидалось 10000 коп, got %d", u.Balance)
	}
	a.applyPromo(ctx, 300, "LETO")
	if u, _ := fs.GetUser(ctx, 300); u.Balance != 10000 {
		t.Fatalf("повторная активация тем же юзером: %d", u.Balance)
	}
	if p, _ := fs.GetPromo(ctx, "LETO"); p.Used != 1 {
		t.Fatalf("ожидалось used=1, got %d", p.Used)
	}
}

func TestPromo_NotFoundNoGrant(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 300)
	a.applyPromo(ctx, 300, "NOPE")
	if u, _ := fs.GetUser(ctx, 300); u.Balance != 0 {
		t.Fatalf("несуществующий код не должен начислять: %d", u.Balance)
	}
}

func TestPromo_Exhausted(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 300)
	_ = fs.UpsertUser(ctx, 301)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "ONE", Kind: model.PromoKindBalance, Value: 50, MaxUses: 1})
	a.applyPromo(ctx, 300, "ONE")
	a.applyPromo(ctx, 301, "ONE")
	if u, _ := fs.GetUser(ctx, 301); u.Balance != 0 {
		t.Fatalf("лимит исчерпан — второму не начисляем: %d", u.Balance)
	}
}

func TestPromo_DaysFailureKeepsCode(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 300)
	_ = fs.CreatePromo(ctx, &model.PromoCode{Code: "DNI", Kind: model.PromoKindDays, Value: 10, MaxUses: 1})

	if _, ok := a.redeemPromo(ctx, 300, "DNI"); ok {
		t.Fatal("без панели дни начислять нечем")
	}
	if p, _ := fs.GetPromo(ctx, "DNI"); p.Used != 0 {
		t.Fatalf("неудачная активация не должна тратить код: used=%d", p.Used)
	}
	if done, _ := fs.PromoRedeemedBy(ctx, "DNI", 300); done {
		t.Fatal("код остался закреплён за юзером после неудачи")
	}
}
