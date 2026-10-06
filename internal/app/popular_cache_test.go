package app

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
)

func TestMostPopularPlanCached(t *testing.T) {
	ctx := context.Background()
	fs := &fakeStore{pays: map[int64]*model.Payment{}}
	a := &App{store: fs}
	for i := int64(1); i <= 3; i++ {
		fs.pays[i] = &model.Payment{ID: i, TelegramID: 555, Months: 1, Status: model.PaymentPaid}
	}
	if m, n := a.mostPopularPlan(ctx); m != 1 || n != 3 {
		t.Fatalf("первый подсчёт: %d/%d", m, n)
	}
	for i := int64(4); i <= 10; i++ {
		fs.pays[i] = &model.Payment{ID: i, TelegramID: 777, Months: 3, Status: model.PaymentPaid}
	}
	if m, n := a.mostPopularPlan(ctx); m != 1 || n != 3 {
		t.Fatalf("в пределах срока кэша: %d/%d", m, n)
	}
	a.popularAt = time.Now().Add(-popularTTL - time.Second)
	if m, n := a.mostPopularPlan(ctx); m != 3 || n != 10 {
		t.Fatalf("после срока кэша: %d/%d", m, n)
	}
}
