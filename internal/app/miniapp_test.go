package app

import (
	"context"
	"testing"
)

// These cover the early-return validation branches of MiniCheckout that do not
// touch the store or panel (so a bare App is sufficient).
func TestMiniCheckoutInvalidMonths(t *testing.T) {
	a := &App{}
	r := a.MiniCheckout(context.Background(), 1, 7, "balance") // 7 not in PlanMonths
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for invalid months, got %+v", r)
	}
}

func TestMiniCheckoutNonBalanceRedirects(t *testing.T) {
	a := &App{}
	r := a.MiniCheckout(context.Background(), 1, 1, "yookassa")
	if r.OK || !r.Redirect {
		t.Fatalf("expected redirect for non-balance method, got %+v", r)
	}
}
