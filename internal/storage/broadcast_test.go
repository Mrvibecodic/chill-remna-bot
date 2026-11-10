package storage

import (
	"context"
	"testing"

	"remnabot/internal/model"
)

// Выборка получателей рассылки по группам — на настоящей базе обоих диалектов.
func TestBroadcastAudience(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		const exp = "2030-01-01T00:00:00Z"
		for _, id := range []int64{9101, 9102, 9103, 9104, 1000000001} {
			if err := st.UpsertUser(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.SetSubExpiry(ctx, 9101, exp, "trial"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSubExpiry(ctx, 1000000001, exp, "paid"); err != nil {
			t.Fatal(err)
		}
		if err := st.SetUserSnapshot(ctx, 1000000001, &model.PlanSnapshot{Code: "vip", Months: 1}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetBlocked(ctx, 9103, true); err != nil {
			t.Fatal(err)
		}
		if err := st.SetUnreachable(ctx, 9104, "2020-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}

		users, err := st.BroadcastAudience(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := map[int64]model.User{}
		for _, u := range users {
			got[u.TelegramID] = u
		}
		if len(got) != 3 {
			t.Fatalf("получатели: %v", got)
		}
		if u := got[9101]; u.SubExpireAt != exp || u.NotifyKind != "trial" || u.Snapshot != nil {
			t.Fatalf("триал: %+v", u)
		}
		if u := got[1000000001]; u.SubExpireAt != exp || u.Snapshot == nil || u.Snapshot.Code != "vip" {
			t.Fatalf("тариф: %+v", u)
		}
		if _, ok := got[9102]; !ok {
			t.Fatal("без подписки потерялся")
		}
	})
}
