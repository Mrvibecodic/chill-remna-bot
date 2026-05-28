package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"remnabot/internal/config"
	"remnabot/internal/model"
)

func TestReminders_SubAndTrial(t *testing.T) {
	fm := &fakeMsg{}
	fs := &fakeStore{}
	a := &App{
		cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		msg:   fm,
		store: fs,
		ui:    map[int64]*uiState{},
	}
	a.botCfg = &model.BotConfig{
		Installed: true, Language: "ru",
		Reminders: model.RemindersConfig{Enabled: true, DaysList: []int{3, 1}, TrialEnabled: true, TrialDaysBefore: 1, Init: true},
	}
	ctx := context.Background()
	now := time.Now().UTC()

	// Платный юзер, до конца ~2 дня → окно «3» срабатывает.
	_ = fs.UpsertUser(ctx, 777)
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")
	a.remindOnce(ctx)
	if u, _ := fs.GetUser(ctx, 777); u == nil || !strings.Contains(u.NotifySent, "3") {
		t.Fatalf("paid: окно 3 не отмечено отправленным: %+v", u)
	}
	if !strings.Contains(fm.joined(), "подписка заканчивается") {
		t.Fatalf("paid: напоминание о подписке не отправлено:\n%s", fm.joined())
	}
	// Повторный прогон не должен слать то же окно снова.
	before := len(fm.texts)
	a.remindOnce(ctx)
	if len(fm.texts) != before {
		t.Fatalf("paid: повторное напоминание не должно отправляться")
	}

	// Триал-юзер, до конца ~12 часов → окно за 1 день срабатывает.
	_ = fs.UpsertUser(ctx, 888)
	_ = fs.SetSubExpiry(ctx, 888, now.Add(12*time.Hour).Format(time.RFC3339), "trial")
	n0 := len(fm.texts)
	a.remindOnce(ctx)
	if len(fm.texts) <= n0 || !strings.Contains(fm.joined(), "триал") {
		t.Fatalf("trial: напоминание не отправлено:\n%s", fm.joined())
	}
}
