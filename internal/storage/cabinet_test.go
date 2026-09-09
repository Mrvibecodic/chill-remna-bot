package storage

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
)

// Round-trip против настоящей базы. Подтверждение почты, одноразовые ссылки и
// поколение пропусков через подменённое хранилище не проверить: расхождение
// между SELECT и Scan там не всплывает вовсе.
func TestWebUserVerifyRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if err := st.UpsertUser(ctx, -11); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateWebUser(ctx, &model.WebUser{TgID: -11, Email: "a@example.com", PassHash: "h"}); err != nil {
			t.Fatal(err)
		}
		wu, err := st.GetWebUserByEmail(ctx, "a@example.com")
		if err != nil || wu == nil {
			t.Fatalf("чтение: %v", err)
		}
		if wu.VerifiedAt != "" {
			t.Fatal("новый аккаунт обязан быть неподтверждённым")
		}
		if err := st.SetWebUserVerified(ctx, -11, ""); err != nil {
			t.Fatal(err)
		}
		if wu, _ = st.GetWebUserByTgID(ctx, -11); wu == nil || wu.VerifiedAt == "" {
			t.Fatal("подтверждение не сохранилось")
		}
		if err := st.SetWebUserPassword(ctx, -11, "h2"); err != nil {
			t.Fatal(err)
		}
		if wu, _ = st.GetWebUserByTgID(ctx, -11); wu == nil || wu.PassHash != "h2" {
			t.Fatal("пароль не сохранился")
		}
		// Несуществующий аккаунт — ошибка, а не «успешно ничего не сделали»:
		// иначе вызывающий скажет «пароль изменён», не изменив его.
		if err := st.SetWebUserPassword(ctx, -12, "h3"); err == nil {
			t.Fatal("смена пароля у несуществующего аккаунта обязана быть ошибкой")
		}
	})
}

func TestEmailTokenSingleUse(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		now := time.Now().UTC()
		tok := &model.EmailToken{
			Hash: "hash1", TgID: -21, Purpose: model.EmailPurposeVerify, Email: "a@example.com",
			CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
		}
		if err := st.PutEmailToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
		got, err := st.TakeEmailToken(ctx, "hash1", model.EmailPurposeVerify)
		if err != nil || got == nil || got.Email != "a@example.com" {
			t.Fatalf("первое гашение: %+v err=%v", got, err)
		}
		got, err = st.TakeEmailToken(ctx, "hash1", model.EmailPurposeVerify)
		if err != nil || got != nil {
			t.Fatalf("повторное гашение обязано вернуть пусто: %+v err=%v", got, err)
		}

		// Назначение — часть проверки: ссылка сброса не должна подходить для
		// подтверждения.
		if err := st.PutEmailToken(ctx, &model.EmailToken{
			Hash: "hash2", TgID: -21, Purpose: model.EmailPurposeReset, Email: "a@example.com",
			CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.TakeEmailToken(ctx, "hash2", model.EmailPurposeVerify); got != nil {
			t.Fatal("чужое назначение обязано быть отвергнуто")
		}

		// Просроченная не годится.
		if err := st.PutEmailToken(ctx, &model.EmailToken{
			Hash: "hash3", TgID: -21, Purpose: model.EmailPurposeVerify,
			CreatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
			ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339),
		}); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.TakeEmailToken(ctx, "hash3", model.EmailPurposeVerify); got != nil {
			t.Fatal("просроченная ссылка обязана быть отвергнута")
		}

		// Гашение прежних оставляет строки на месте: по ним считается порог
		// отправки писем, и удаление обнуляло бы его при каждом письме.
		n, err := st.CountEmailTokensSince(ctx, -21, model.EmailPurposeVerify, now.Add(-24*time.Hour).Format(time.RFC3339))
		if err != nil || n != 2 {
			t.Fatalf("счётчик писем: n=%d err=%v", n, err)
		}
		if err := st.RevokeEmailTokens(ctx, -21, model.EmailPurposeReset); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.TakeEmailToken(ctx, "hash2", model.EmailPurposeReset); got != nil {
			t.Fatal("погашенная ссылка обязана перестать работать")
		}
		if n, _ := st.CountEmailTokensSince(ctx, -21, model.EmailPurposeReset, now.Add(-24*time.Hour).Format(time.RFC3339)); n != 1 {
			t.Fatalf("гашение не имеет права стирать строку: n=%d", n)
		}
		if err := st.PurgeEmailTokens(ctx, now.Add(-30*time.Minute).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if n, _ := st.CountEmailTokensSince(ctx, -21, model.EmailPurposeVerify, now.Add(-24*time.Hour).Format(time.RFC3339)); n != 1 {
			t.Fatalf("уборка обязана снять только просроченную: n=%d", n)
		}
	})
}

func TestSessEpochRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if err := st.UpsertUser(ctx, 31); err != nil {
			t.Fatal(err)
		}
		if n, err := st.UserSessEpoch(ctx, 31); err != nil || n != 0 {
			t.Fatalf("новое поколение: n=%d err=%v", n, err)
		}
		if n, err := st.BumpSessEpoch(ctx, 31); err != nil || n != 1 {
			t.Fatalf("поднятие: n=%d err=%v", n, err)
		}
		u, err := st.GetUser(ctx, 31)
		if err != nil || u == nil || u.SessEpoch != 1 {
			t.Fatalf("поколение в карточке: %+v err=%v", u, err)
		}
		// Несуществующий аккаунт — ноль, а не ошибка: пропуск такого аккаунта
		// всё равно не пройдёт остальные проверки.
		if n, err := st.UserSessEpoch(ctx, 32); err != nil || n != 0 {
			t.Fatalf("нет строки: n=%d err=%v", n, err)
		}
	})
}

// Перенос аккаунта на телеграмный идентификатор: история обязана переехать
// целиком, иначе привязка Telegram теряет платежи и заявки.
func TestMoveAccount(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		const from, to = int64(-41), int64(41)
		if err := st.UpsertUser(ctx, from); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateWebUser(ctx, &model.WebUser{TgID: from, Email: "m@example.com", PassHash: "h"}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddBalance(ctx, from, 500); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSubExpiry(ctx, from, "2030-01-01T00:00:00Z", "paid"); err != nil {
			t.Fatal(err)
		}
		if err := st.AddPayment(ctx, &model.Payment{TelegramID: from, Method: "yk", Months: 1, Amount: "100", Status: model.PaymentPaid}); err != nil {
			t.Fatal(err)
		}
		// Пустая строка от одного /start на целевом идентификаторе не должна
		// мешать переезду: переносить с неё нечего, а первичный ключ она занимает.
		if err := st.UpsertUser(ctx, to); err != nil {
			t.Fatal(err)
		}

		if err := st.MoveAccount(ctx, from, to); err != nil {
			t.Fatalf("перенос: %v", err)
		}
		if u, _ := st.GetUser(ctx, from); u != nil {
			t.Fatal("прежняя строка обязана исчезнуть")
		}
		u, err := st.GetUser(ctx, to)
		if err != nil || u == nil || u.Balance != 500 || u.SubExpireAt == "" {
			t.Fatalf("аккаунт не переехал: %+v err=%v", u, err)
		}
		wu, _ := st.GetWebUserByEmail(ctx, "m@example.com")
		if wu == nil || wu.TgID != to {
			t.Fatalf("вход по паролю не переехал: %+v", wu)
		}
		if ok, err := st.HasPaidPayment(ctx, to); err != nil || !ok {
			t.Fatalf("платежи не переехали: ok=%v err=%v", ok, err)
		}
	})
}

// Следы аккаунта: по ним решается, свободен ли телеграмный идентификатор.
func TestAccountFootprint(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if fp, err := st.AccountFootprint(ctx, 51); err != nil || fp.HasRow || fp.Active {
			t.Fatalf("незнакомый идентификатор: %+v err=%v", fp, err)
		}
		if err := st.UpsertUser(ctx, 51); err != nil {
			t.Fatal(err)
		}
		fp, err := st.AccountFootprint(ctx, 51)
		if err != nil || !fp.HasRow || fp.Active {
			t.Fatalf("пустая строка от /start аккаунтом не считается: %+v err=%v", fp, err)
		}
		if err := st.AddBalance(ctx, 51, 1); err != nil {
			t.Fatal(err)
		}
		if fp, _ := st.AccountFootprint(ctx, 51); !fp.Active {
			t.Fatal("деньги на балансе — это аккаунт")
		}
		// История переживает удаление строки пользователя, поэтому смотрим и на неё.
		if err := st.AddPayment(ctx, &model.Payment{TelegramID: 52, Method: "yk", Months: 1, Amount: "100", Status: model.PaymentPaid}); err != nil {
			t.Fatal(err)
		}
		if fp, _ := st.AccountFootprint(ctx, 52); !fp.Active {
			t.Fatal("платёж без строки пользователя — это тоже аккаунт")
		}
	})
}

// Аккаунты кабинета обязаны переживать смену движка базы: раньше они в снимок
// не входили вовсе, и переезд стирал и почту, и пароль.
func TestSnapshotCarriesWebUsers(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		if err := st.UpsertUser(ctx, -61); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateWebUser(ctx, &model.WebUser{
			TgID: -61, Email: "snap@example.com", PassHash: "hash", VerifiedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatal(err)
		}
		snap, err := st.Export(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var found *model.WebUser
		for i := range snap.WebUsers {
			if snap.WebUsers[i].Email == "snap@example.com" {
				found = &snap.WebUsers[i]
			}
		}
		if found == nil || found.PassHash != "hash" || found.VerifiedAt == "" {
			t.Fatalf("аккаунт кабинета не попал в снимок: %+v", found)
		}
	})
}
