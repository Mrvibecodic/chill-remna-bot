package storage

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

func TestPromoRedeemLimits(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t)

	if err := st.CreatePromo(ctx, &model.PromoCode{Code: "ONE", Kind: model.PromoKindBalance, Value: 50, MaxUses: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.RedeemPromo(ctx, "ONE", 1); err != nil || !ok {
		t.Fatalf("первая активация: ok=%v err=%v", ok, err)
	}
	if ok, err := st.RedeemPromo(ctx, "ONE", 1); err != nil || ok {
		t.Fatalf("повтор тем же юзером: ok=%v err=%v", ok, err)
	}
	if ok, err := st.RedeemPromo(ctx, "ONE", 2); err != nil || ok {
		t.Fatalf("сверх лимита: ok=%v err=%v", ok, err)
	}
	if done, _ := st.PromoRedeemedBy(ctx, "ONE", 2); done {
		t.Fatal("отклонённая активация не должна закрепляться")
	}
	if p, _ := st.GetPromo(ctx, "ONE"); p == nil || p.Used != 1 {
		t.Fatalf("used=%v", p)
	}

	if err := st.ReleasePromo(ctx, "ONE", 1); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.GetPromo(ctx, "ONE"); p == nil || p.Used != 0 {
		t.Fatalf("после отката used=%v", p)
	}
	if done, _ := st.PromoRedeemedBy(ctx, "ONE", 1); done {
		t.Fatal("после отката закрепление должно сниматься")
	}
	if ok, err := st.RedeemPromo(ctx, "ONE", 1); err != nil || !ok {
		t.Fatalf("после отката код снова доступен: ok=%v err=%v", ok, err)
	}
}

func TestPromoUnlimitedUses(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t)

	if err := st.CreatePromo(ctx, &model.PromoCode{Code: "FREE", Kind: model.PromoKindDays, Value: 7}); err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 3; id++ {
		if ok, err := st.RedeemPromo(ctx, "FREE", id); err != nil || !ok {
			t.Fatalf("без лимита активация %d: ok=%v err=%v", id, ok, err)
		}
	}
	if p, _ := st.GetPromo(ctx, "FREE"); p == nil || p.Used != 3 {
		t.Fatalf("used=%v", p)
	}
}
