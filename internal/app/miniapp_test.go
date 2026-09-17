package app

import (
	"context"
	"slices"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/web"
)

// These cover the early-return validation branches of MiniCheckout that do not
// touch the store or panel (so a bare App is sufficient).
func TestMiniCheckoutInvalidMonths(t *testing.T) {
	a := &App{}
	r := a.MiniCheckout(context.Background(), 1, "", 7, "balance", "", false) // 7 not in PlanMonths
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for invalid months, got %+v", r)
	}
}

func TestMiniCheckoutUnknownMethod(t *testing.T) {
	a := &App{}
	r := a.MiniCheckout(context.Background(), 1, "", 1, "nope", "", false)
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for unknown method, got %+v", r)
	}
}

func TestMiniCheckoutUnconfiguredExternal(t *testing.T) {
	a := &App{}
	// External method with no panel/config configured must fail cleanly
	// (not OK, not a redirect) rather than pretend success.
	r := a.MiniCheckout(context.Background(), 1, "", 1, "yookassa", "", false)
	if r.OK || r.Error == "" {
		t.Fatalf("expected error for unconfigured yookassa, got %+v", r)
	}
}

// Список способов оплаты собирается под a.mu, а проверка валюты сетки для
// Platega раньше брала тот же замок повторно: обработчик замирал навсегда и
// держал главный замок приложения.
func TestMiniMenu_PlategaNoSelfDeadlock(t *testing.T) {
	a, _, fs := planAdminApp(t)
	uid := int64(777)
	_ = fs.UpsertUser(context.Background(), uid)
	a.botCfg.Platega = model.PlategaConfig{Enabled: true, MerchantID: "m", Secret: "s"}

	done := make(chan web.MiniMenuDTO, 1)
	go func() { done <- a.MiniMenu(context.Background(), uid, false) }()
	select {
	case dto := <-done:
		if !slices.Contains(dto.PayMethods, model.PayMethodPlatega) {
			t.Fatalf("Platega не попала в способы оплаты: %+v", dto.PayMethods)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("меню мини-аппа не ответило: замок приложения занят самим же обработчиком")
	}

	// Нерублёвая сетка: способ не показываем — и тоже без зависания.
	a.botCfg.Pricing.Currency = "USD"
	done2 := make(chan web.MiniMenuDTO, 1)
	go func() { done2 <- a.MiniMenu(context.Background(), uid, false) }()
	select {
	case dto := <-done2:
		if slices.Contains(dto.PayMethods, model.PayMethodPlatega) {
			t.Fatalf("при нерублёвой сетке Platega показывать нельзя: %+v", dto.PayMethods)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("меню мини-аппа не ответило при нерублёвой сетке")
	}
}
