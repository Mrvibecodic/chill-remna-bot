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
