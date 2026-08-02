package app

import (
	"context"
	"testing"
)

// These cover the early-return validation branches of MiniCheckout that do not
// touch the store or panel (so a bare App is sufficient).
func TestMiniCheckoutInvalidMonths(t *testing.T) {
	a := &App{}
	r := a.MiniCheckout(context.Background(), 1, 7, "balance", false) // 7 not in PlanMonths
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for invalid months, got %+v", r)
	}
}

func TestMiniCheckoutUnknownMethod(t *testing.T) {
	a := &App{}
	r := a.MiniCheckout(context.Background(), 1, 1, "nope", false)
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for unknown method, got %+v", r)
	}
}

func TestMiniCheckoutUnconfiguredExternal(t *testing.T) {
	a := &App{}
	// External method with no panel/config configured must fail cleanly
	// (not OK, not a redirect) rather than pretend success.
	r := a.MiniCheckout(context.Background(), 1, 1, "yookassa", false)
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for unconfigured yookassa, got %+v", r)
	}
}
