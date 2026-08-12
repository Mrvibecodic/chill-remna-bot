package storage

import (
	"context"
	"errors"
	"testing"

	"remnabot/internal/model"
)

// Round-trip против настоящей базы: расхождение SELECT и Scan в списках
// допущенных не ловится ни компилятором, ни тестами через подменённое
// хранилище.
func TestPlanAccessRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if err := st.SavePlan(ctx, &model.Plan{Code: "vip", Name: "VIP", Availability: model.PlanAvailList}); err != nil {
			t.Fatal(err)
		}

		if err := st.GrantPlanAccess(ctx, "vip", 100, ""); err != nil {
			t.Fatal(err)
		}
		// Почта хранится в нижнем регистре: кабинет сравнивает её именно так.
		if err := st.GrantPlanAccess(ctx, "vip", 0, " User@Example.COM "); err != nil {
			t.Fatal(err)
		}
		// Повторная выдача — не ошибка и не вторая строка.
		if err := st.GrantPlanAccess(ctx, "vip", 100, ""); err != nil {
			t.Fatal(err)
		}

		list, err := st.ListPlanAccess(ctx, "vip")
		if err != nil || len(list) != 2 {
			t.Fatalf("ListPlanAccess: len=%d err=%v %+v", len(list), err, list)
		}
		// Порядок в пределах одной секунды детерминирован, но не «порядок
		// добавления» — ищем записи, а не сверяем позиции.
		var tgRow, emRow *model.PlanAccess
		for i := range list {
			if list[i].TelegramID == 100 {
				tgRow = &list[i]
			}
			if list[i].Email != "" {
				emRow = &list[i]
			}
		}
		if tgRow == nil || tgRow.PlanCode != "vip" || tgRow.Email != "" || tgRow.CreatedAt == "" {
			t.Fatalf("запись Telegram искажена: %+v", list)
		}
		if emRow == nil || emRow.Email != "user@example.com" || emRow.TelegramID != 0 {
			t.Fatalf("запись e-mail искажена: %+v", list)
		}

		// Совпадение по ID и по почте; чужие значения не проходят. Пустая почта
		// в запросе не должна совпадать с записью по Telegram ID (и наоборот).
		if ok, _ := st.HasPlanAccess(ctx, "vip", 100, ""); !ok {
			t.Fatal("допуск по Telegram ID не найден")
		}
		if ok, _ := st.HasPlanAccess(ctx, "vip", -42, "USER@example.com"); !ok {
			t.Fatal("допуск по почте не найден")
		}
		if ok, _ := st.HasPlanAccess(ctx, "vip", 200, ""); ok {
			t.Fatal("чужой Telegram ID прошёл")
		}
		if ok, _ := st.HasPlanAccess(ctx, "vip", 0, ""); ok {
			t.Fatal("пустой запрос прошёл")
		}
		if ok, _ := st.HasPlanAccess(ctx, "other", 100, ""); ok {
			t.Fatal("допуск утёк в чужой тариф")
		}

		// Некорректные записи отклоняются: и пустая, и двойная.
		if err := st.GrantPlanAccess(ctx, "vip", 0, ""); !errors.Is(err, ErrPlanAccessEntry) {
			t.Fatalf("пустая запись должна отклоняться: %v", err)
		}
		if err := st.GrantPlanAccess(ctx, "vip", 5, "x@y.z"); !errors.Is(err, ErrPlanAccessEntry) {
			t.Fatalf("запись с ID и почтой сразу должна отклоняться: %v", err)
		}
		if err := st.GrantPlanAccess(ctx, "не код", 5, ""); !errors.Is(err, ErrPlanCode) {
			t.Fatalf("недопустимый код тарифа должен отклоняться: %v", err)
		}

		if err := st.RevokePlanAccess(ctx, "vip", 100, ""); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasPlanAccess(ctx, "vip", 100, ""); ok {
			t.Fatal("отозванный допуск остался")
		}

		// Удаление тарифа уносит список с собой.
		if err := st.DeletePlan(ctx, "vip"); err != nil {
			t.Fatal(err)
		}
		if left, _ := st.ListPlanAccess(ctx, "vip"); len(left) != 0 {
			t.Fatalf("список пережил удаление тарифа: %+v", left)
		}
	})
}

// Удаление пользователя чистит его допуски; у e-mail-аккаунта кабинета — и
// запись по почте.
func TestPlanAccessGoneWithUser(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if err := st.SavePlan(ctx, &model.Plan{Code: "vip", Name: "VIP", Availability: model.PlanAvailList}); err != nil {
			t.Fatal(err)
		}
		_ = st.UpsertUser(ctx, 300)
		if err := st.GrantPlanAccess(ctx, "vip", 300, ""); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteUser(ctx, 300); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasPlanAccess(ctx, "vip", 300, ""); ok {
			t.Fatal("допуск пережил удаление пользователя")
		}

		if err := st.CreateWebUser(ctx, &model.WebUser{TgID: -77, Email: "web@example.com", PassHash: "x"}); err != nil {
			t.Fatal(err)
		}
		_ = st.UpsertUser(ctx, -77)
		if err := st.GrantPlanAccess(ctx, "vip", 0, "web@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteUser(ctx, -77); err != nil {
			t.Fatal(err)
		}
		if ok, _ := st.HasPlanAccess(ctx, "vip", -77, "web@example.com"); ok {
			t.Fatal("допуск по почте пережил удаление e-mail-аккаунта")
		}
	})
}

// Списки допущенных обязаны переживать переезд базы вместе с тарифами.
func TestPlanAccessInSnapshot(t *testing.T) {
	ctx := context.Background()
	src := openSQLiteTest(t)
	if err := src.SavePlan(ctx, &model.Plan{Code: "vip", Name: "VIP", Availability: model.PlanAvailList}); err != nil {
		t.Fatal(err)
	}
	if err := src.GrantPlanAccess(ctx, "vip", 100, ""); err != nil {
		t.Fatal(err)
	}
	if err := src.GrantPlanAccess(ctx, "vip", 0, "web@example.com"); err != nil {
		t.Fatal(err)
	}

	dst := openSQLiteTest(t)
	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatal(err)
	}
	list, err := dst.ListPlanAccess(ctx, "vip")
	if err != nil || len(list) != 2 {
		t.Fatalf("список не переехал: len=%d err=%v", len(list), err)
	}
	if ok, _ := dst.HasPlanAccess(ctx, "vip", 100, ""); !ok {
		t.Fatal("допуск по ID не переехал")
	}
	if ok, _ := dst.HasPlanAccess(ctx, "vip", -1, "web@example.com"); !ok {
		t.Fatal("допуск по почте не переехал")
	}
	// Повторный перенос идемпотентен.
	if err := Transfer(ctx, src, dst); err != nil {
		t.Fatalf("повторный Transfer упал: %v", err)
	}
}

// CountUsersOnPlan — поиск по подстроке в JSON-снимке: проверяем против
// настоящей базы, что пара `"code":"..."` находится и что похожие коды и
// истёкшие подписки не считаются.
func TestCountUsersOnPlan(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		set := func(id int64, code, expire string) {
			t.Helper()
			if err := st.UpsertUser(ctx, id); err != nil {
				t.Fatal(err)
			}
			if err := st.SetUserSnapshot(ctx, id, &model.PlanSnapshot{Code: code, Months: 1}); err != nil {
				t.Fatal(err)
			}
			if expire != "" {
				if err := st.SetSubExpiry(ctx, id, expire, "sub"); err != nil {
					t.Fatal(err)
				}
			}
		}
		const now = "2026-08-12T00:00:00Z"
		set(1, "vip", "2027-01-01T00:00:00Z")    // живёт на тарифе
		set(2, "vip", "2026-01-01T00:00:00Z")    // подписка истекла
		set(3, "vipvip", "2027-01-01T00:00:00Z") // похожий код — не совпадение
		set(4, "base", "2027-01-01T00:00:00Z")   // другой тариф
		set(5, "vip", "")                        // подписки нет

		n, err := st.CountUsersOnPlan(ctx, "vip", now)
		if err != nil || n != 1 {
			t.Fatalf("CountUsersOnPlan(vip) = %d, %v; ожидался 1", n, err)
		}
		if n, _ := st.CountUsersOnPlan(ctx, "base", now); n != 1 {
			t.Fatalf("CountUsersOnPlan(base) = %d; ожидался 1", n)
		}
		if _, err := st.CountUsersOnPlan(ctx, "нет такого", now); err == nil {
			t.Fatal("недопустимый код должен отклоняться")
		}
		// «_» в коде — не LIKE-джокер: my_plan не должен считать my-plan.
		set(6, "my-plan", "2027-01-01T00:00:00Z")
		if n, err := st.CountUsersOnPlan(ctx, "my_plan", now); err != nil || n != 0 {
			t.Fatalf("подчёркивание сработало джокером: n=%d err=%v", n, err)
		}
		set(7, "my_plan", "2027-01-01T00:00:00Z")
		if n, _ := st.CountUsersOnPlan(ctx, "my_plan", now); n != 1 {
			t.Fatalf("код с подчёркиванием не найден: n=%d", n)
		}
	})
}
