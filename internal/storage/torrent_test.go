package storage

import (
	"context"
	"testing"
	"time"

	"remnabot/internal/model"
)

func TestTorrentReports_Roundtrip(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		now := time.Now().UTC()

		r1 := &model.TorrentReport{
			TelegramID: 42, Username: "u42", Node: "n1 (NL)", IP: "203.0.113.7",
			Protocol: "bittorrent", Inbound: "VLESS", Source: "203.0.113.7:1", Destination: "198.51.100.9:2",
			BlockSeconds: 3600, WillUnblockAt: now.Add(-time.Minute).Format(time.RFC3339),
		}
		if err := st.AddTorrentReport(ctx, r1); err != nil {
			t.Fatal(err)
		}
		r2 := &model.TorrentReport{TelegramID: 42, Username: "u42", IP: "203.0.113.8",
			WillUnblockAt: now.Add(time.Hour).Format(time.RFC3339)}
		if err := st.AddTorrentReport(ctx, r2); err != nil {
			t.Fatal(err)
		}
		// Аккаунт без Telegram: учитывается по username, в очередь разблокировок
		// не попадает (нотификация помечена при записи).
		r3 := &model.TorrentReport{Username: "panel_only", IP: "203.0.113.9",
			WillUnblockAt: now.Add(-time.Minute).Format(time.RFC3339), UnblockNotified: true}
		if err := st.AddTorrentReport(ctx, r3); err != nil {
			t.Fatal(err)
		}

		list, total, err := st.TorrentReports(ctx, 2, 0)
		if err != nil || total != 3 || len(list) != 2 {
			t.Fatalf("страница: total=%d len=%d err=%v", total, len(list), err)
		}
		if list[0].ID != r3.ID || list[0].Username != "panel_only" {
			t.Fatalf("порядок не «новые сверху»: %+v", list[0])
		}
		if list[1].ID != r2.ID {
			t.Fatalf("вторая строка не r2: %+v", list[1])
		}
		got := list[1]
		if got.TelegramID != 42 || got.IP != "203.0.113.8" || got.WillUnblockAt != r2.WillUnblockAt {
			t.Fatalf("поля не сохранились: %+v", got)
		}

		since := now.Add(-24 * time.Hour).Format(time.RFC3339)
		if n, _ := st.CountTorrentReports(ctx, 42, "u42", since); n != 2 {
			t.Fatalf("повторы по tg_id: ожидалось 2, got %d", n)
		}
		if n, _ := st.CountTorrentReports(ctx, 0, "panel_only", since); n != 1 {
			t.Fatalf("повторы по username: ожидалось 1, got %d", n)
		}
		if n, _ := st.CountTorrentReports(ctx, 42, "u42", now.Add(time.Hour).Format(time.RFC3339)); n != 0 {
			t.Fatalf("окно since не работает: got %d", n)
		}

		due, err := st.DueTorrentUnblocks(ctx, now.Format(time.RFC3339))
		if err != nil || len(due) != 1 || due[0].ID != r1.ID {
			t.Fatalf("к разблокировке должен быть только r1: %v err=%v", due, err)
		}
		if err := st.MarkTorrentUnblockNotified(ctx, r1.ID); err != nil {
			t.Fatal(err)
		}
		if due, _ := st.DueTorrentUnblocks(ctx, now.Format(time.RFC3339)); len(due) != 0 {
			t.Fatalf("после пометки очередь должна быть пуста: %v", due)
		}

		if err := st.PurgeTorrentReports(ctx, now.Add(time.Hour).Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
		if _, total, _ := st.TorrentReports(ctx, 10, 0); total != 0 {
			t.Fatalf("purge не вычистил журнал: total=%d", total)
		}
	})
}

// Выборка по адресу и отметка автоблокировки — реальный SQL на обеих СУБД.
func TestTorrentUnblocksByIPAndStrikes(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		now := time.Now().UTC()

		open := &model.TorrentReport{TelegramID: 42, IP: "203.0.113.7",
			WillUnblockAt: now.Add(time.Hour).Format(time.RFC3339)}
		closed := &model.TorrentReport{TelegramID: 42, IP: "203.0.113.7",
			WillUnblockAt: now.Add(time.Hour).Format(time.RFC3339), UnblockNotified: true}
		other := &model.TorrentReport{TelegramID: 43, IP: "198.51.100.9",
			WillUnblockAt: now.Add(time.Hour).Format(time.RFC3339)}
		for _, r := range []*model.TorrentReport{open, closed, other} {
			if err := st.AddTorrentReport(ctx, r); err != nil {
				t.Fatal(err)
			}
		}

		got, err := st.PendingTorrentUnblocksByIP(ctx, "203.0.113.7")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != open.ID {
			t.Fatalf("ожидалась одна открытая запись по адресу, получено %+v", got)
		}
		if empty, err := st.PendingTorrentUnblocksByIP(ctx, ""); err != nil || len(empty) != 0 {
			t.Fatalf("пустой адрес не должен ничего выбирать: %+v err=%v", empty, err)
		}

		if at, err := st.TorrentStrikeAt(ctx, 42); err != nil || at != "" {
			t.Fatalf("без отметки ожидалась пустая строка: %q err=%v", at, err)
		}
		first := now.Format(time.RFC3339)
		if err := st.SetTorrentStrike(ctx, 42, first); err != nil {
			t.Fatal(err)
		}
		second := now.Add(time.Hour).Format(time.RFC3339)
		if err := st.SetTorrentStrike(ctx, 42, second); err != nil {
			t.Fatalf("повторная отметка должна перезаписывать: %v", err)
		}
		if at, _ := st.TorrentStrikeAt(ctx, 42); at != second {
			t.Fatalf("отметка не обновилась: %q", at)
		}
		if at, _ := st.TorrentStrikeAt(ctx, 43); at != "" {
			t.Fatalf("чужая отметка: %q", at)
		}
	})
}

// Журнал по одному нарушителю: реальный SQL, обе СУБД, пагинация.
func TestUserTorrentReports(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		for i := 0; i < 3; i++ {
			if err := st.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: 42, Username: "u42", IP: "203.0.113.7"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: 43, Username: "u43"}); err != nil {
			t.Fatal(err)
		}
		// Аккаунт без Telegram — отбирается по username панели.
		if err := st.AddTorrentReport(ctx, &model.TorrentReport{Username: "noTg"}); err != nil {
			t.Fatal(err)
		}

		got, total, err := st.UserTorrentReports(ctx, 42, "", 2, 0)
		if err != nil || total != 3 || len(got) != 2 {
			t.Fatalf("total=%d got=%d err=%v", total, len(got), err)
		}
		if got[0].ID < got[1].ID {
			t.Fatalf("новые записи должны идти первыми: %d, %d", got[0].ID, got[1].ID)
		}
		if page2, _, _ := st.UserTorrentReports(ctx, 42, "", 2, 2); len(page2) != 1 {
			t.Fatalf("вторая страница: %d", len(page2))
		}
		if _, total, _ := st.UserTorrentReports(ctx, 0, "noTg", 10, 0); total != 1 {
			t.Fatalf("отбор по username панели: total=%d", total)
		}
		if _, total, _ := st.UserTorrentReports(ctx, 999, "", 10, 0); total != 0 {
			t.Fatalf("у постороннего не должно быть записей: %d", total)
		}
	})
}

// Регрессия: telegram_id живого аккаунта больше 2^31. Сравнение плейсхолдера
// с литералом в SQL заставляло Postgres выводить для параметра int4, и запрос
// падал с ошибкой кодирования — то есть счётчик нарушений и журнал по
// пользователю на Postgres не работали вовсе.
func TestTorrentReports_BigTelegramID(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		const big = int64(7123456789) // > 2^31

		if err := st.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: big, Username: "big", IP: "203.0.113.7"}); err != nil {
			t.Fatal(err)
		}
		n, err := st.CountTorrentReports(ctx, big, "", "")
		if err != nil || n != 1 {
			t.Fatalf("счётчик за всё время: n=%d err=%v", n, err)
		}
		since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		if n, err := st.CountTorrentReports(ctx, big, "", since); err != nil || n != 1 {
			t.Fatalf("счётчик за окно: n=%d err=%v", n, err)
		}
		reps, total, err := st.UserTorrentReports(ctx, big, "", 10, 0)
		if err != nil || total != 1 || len(reps) != 1 {
			t.Fatalf("журнал по пользователю: total=%d got=%d err=%v", total, len(reps), err)
		}
		if err := st.SetTorrentStrike(ctx, big, since); err != nil {
			t.Fatalf("отметка автоблокировки: %v", err)
		}
		if at, err := st.TorrentStrikeAt(ctx, big); err != nil || at != since {
			t.Fatalf("отметка не прочиталась: %q err=%v", at, err)
		}
		if all, err := st.CountTorrentReportsAll(ctx, ""); err != nil || all < 1 {
			t.Fatalf("общий счётчик: %d err=%v", all, err)
		}
	})
}

// Повторная доставка того же отчёта (панель не дождалась 200 и переслала)
// не должна плодить записи: по журналу считаются страйки.
func TestAddTorrentReport_Idempotent(t *testing.T) {
	eachStore(t, func(t *testing.T, st Storage) {
		ctx := context.Background()
		r := &model.TorrentReport{ID: 777001, TelegramID: 42, IP: "203.0.113.7"}
		for i := 0; i < 3; i++ {
			cp := *r
			if err := st.AddTorrentReport(ctx, &cp); err != nil {
				t.Fatalf("повтор %d: %v", i, err)
			}
		}
		if _, total, _ := st.UserTorrentReports(ctx, 42, "", 10, 0); total != 1 {
			t.Fatalf("ожидалась одна запись, есть %d", total)
		}
		other := &model.TorrentReport{ID: 777002, TelegramID: 42, IP: "203.0.113.7"}
		if err := st.AddTorrentReport(ctx, other); err != nil {
			t.Fatal(err)
		}
		if _, total, _ := st.UserTorrentReports(ctx, 42, "", 10, 0); total != 2 {
			t.Fatalf("разные инциденты склеились: %d", total)
		}
	})
}
