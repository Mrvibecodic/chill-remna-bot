package app

import (
	"context"
	"fmt"
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

	_ = fs.UpsertUser(ctx, 777)
	_ = fs.SetSubExpiry(ctx, 777, now.Add(48*time.Hour).Format(time.RFC3339), "paid")
	a.remindOnce(ctx)
	if u, _ := fs.GetUser(ctx, 777); u == nil || !strings.Contains(u.NotifySent, "3") {
		t.Fatalf("paid: окно 3 не отмечено отправленным: %+v", u)
	}
	if !strings.Contains(fm.joined(), "подписка заканчивается") {
		t.Fatalf("paid: напоминание о подписке не отправлено:\n%s", fm.joined())
	}

	before := len(fm.texts)
	a.remindOnce(ctx)
	if len(fm.texts) != before {
		t.Fatalf("paid: повторное напоминание не должно отправляться")
	}

	_ = fs.UpsertUser(ctx, 888)
	_ = fs.SetSubExpiry(ctx, 888, now.Add(12*time.Hour).Format(time.RFC3339), "trial")
	n0 := len(fm.texts)
	a.remindOnce(ctx)
	if len(fm.texts) <= n0 || !strings.Contains(fm.joined(), "триал") {
		t.Fatalf("trial: напоминание не отправлено:\n%s", fm.joined())
	}
}

func rwExpiration(uid int64, hours, n int) []byte {
	return []byte(fmt.Sprintf(`{"scope":"user","event":"user.expiration","data":{"telegramId":%d,"n":%d},"meta":{"expiration":%d}}`, uid, n, hours))
}

// Панель при подключённом вебхуке шлёт «осталось N суток» в тот же момент,
// когда бот шлёт своё напоминание за N дней. Об одном сроке человек получает
// одно сообщение; всё, о чём бот сам не напоминает, доходит как раньше.
func TestPanelExpiryWarningNotDuplicated(t *testing.T) {
	const uid int64 = 1000000001
	newApp := func(rc model.RemindersConfig) (*App, *fakeMsg, *fakeStore) {
		fm := &fakeMsg{}
		fs := &fakeStore{}
		a := &App{
			cfg:   &config.Config{AdminID: 100, DataDir: t.TempDir()},
			log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			msg:   fm,
			store: fs,
			ui:    map[int64]*uiState{},
		}
		a.botCfg = &model.BotConfig{Installed: true, Language: "ru", Reminders: rc}
		return a, fm, fs
	}
	on := model.RemindersConfig{Enabled: true, DaysList: []int{3, 1}, TrialEnabled: true, TrialDaysBefore: 1, Init: true}
	ctx := context.Background()
	hook := func(t *testing.T, a *App, body []byte) {
		t.Helper()
		if handled, err := a.HandleRemnawaveWebhook(ctx, "", body); err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
	}
	in := func(d time.Duration) string { return time.Now().UTC().Add(d).Format(time.RFC3339) }

	t.Run("то же число дней — одно сообщение", func(t *testing.T) {
		a, fm, fs := newApp(on)
		_ = fs.UpsertUser(ctx, uid)
		_ = fs.SetSubExpiry(ctx, uid, in(72*time.Hour), "paid")
		// Вебхук пришёл раньше планировщика: молчим, напомнит бот.
		hook(t, a, rwExpiration(uid, -72, 1))
		if len(fm.texts) != 0 {
			t.Fatalf("предупреждение панели не пропущено: %v", fm.texts)
		}
		a.remindOnce(ctx)
		if len(fm.texts) != 1 || !strings.Contains(fm.texts[0], "подписка заканчивается") {
			t.Fatalf("бот не напомнил сам: %v", fm.texts)
		}
		// Вебхук пришёл позже планировщика — тоже молчим.
		hook(t, a, rwExpiration(uid, -72, 2))
		hook(t, a, []byte(`{"event":"user.expires_in_72_hours","data":{"telegramId":1000000001}}`))
		if len(fm.texts) != 1 {
			t.Fatalf("о тех же трёх днях пришло %d сообщений: %v", len(fm.texts), fm.texts)
		}
	})

	t.Run("интервал, о котором бот не напоминает, доходит и ведёт на продление", func(t *testing.T) {
		a, fm, fs := newApp(on)
		_ = fs.UpsertUser(ctx, uid)
		_ = fs.SetSubExpiry(ctx, uid, in(48*time.Hour), "paid")
		_ = fs.SetUserSnapshot(ctx, uid, &model.PlanSnapshot{Code: "vipvipvipvip", Months: 1})
		hook(t, a, rwExpiration(uid, -48, 1))
		if countContains(fm.texts, "Осталось меньше 2 дн.") != 1 {
			t.Fatalf("предупреждение за 2 дня потеряно: %v", fm.texts)
		}
		renew := false
		for _, d := range fm.allCallbackData() {
			if d == "menu:buy" {
				t.Fatal("кнопка ведёт на общую витрину, а не на продление")
			}
			renew = renew || d == "menu:renew"
		}
		if !renew {
			t.Fatalf("кнопки продления нет: %v", fm.allCallbackData())
		}
		hook(t, a, rwExpiration(uid, -5, 1))
		if countContains(fm.texts, "Осталось меньше 5 ч.") != 1 {
			t.Fatalf("предупреждение в часах потеряно: %v", fm.texts)
		}
	})

	t.Run("без покупок в боте кнопка по-прежнему ведёт на витрину", func(t *testing.T) {
		a, fm, _ := newApp(on)
		hook(t, a, rwExpiration(uid, -48, 1))
		got := strings.Join(fm.allCallbackData(), " ")
		if !strings.Contains(got, "menu:buy") || strings.Contains(got, "menu:renew") {
			t.Fatalf("кнопки: %v", got)
		}
	})

	for _, tc := range []struct {
		name  string
		rc    model.RemindersConfig
		kind  string
		exp   time.Duration
		sent  string
		block bool
		hours int
		want  int
	}{
		{"напоминания бота выключены", model.RemindersConfig{DaysList: []int{3, 1}, Init: true}, "paid", 72 * time.Hour, "", false, -72, 1},
		{"срок в боте позже, чем в панели", on, "paid", 240 * time.Hour, "", false, -72, 1},
		{"срок продлён в панели после напоминаний бота", on, "paid", -24 * time.Hour, "3,1", false, -72, 1},
		{"срок в боте раньше, чем в панели", on, "paid", 10 * time.Hour, "3", false, -72, 1},
		{"бот уже напомнил, панель повторила событие позже", on, "paid", 72*time.Hour - 10*time.Minute, "3", false, -72, 0},
		{"человек заблокирован в боте", on, "paid", 72 * time.Hour, "", true, -72, 1},
		{"триал: окно бота", on, "trial", 24 * time.Hour, "", false, -24, 0},
		{"триал: чужое окно", on, "trial", 72 * time.Hour, "", false, -72, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, fm, fs := newApp(tc.rc)
			_ = fs.UpsertUser(ctx, uid)
			_ = fs.SetSubExpiry(ctx, uid, in(tc.exp), tc.kind)
			if tc.sent != "" {
				_ = fs.MarkNotified(ctx, uid, tc.sent)
			}
			if tc.block {
				_ = fs.SetBlocked(ctx, uid, true)
			}
			hook(t, a, rwExpiration(uid, tc.hours, 1))
			if len(fm.texts) != tc.want {
				t.Fatalf("сообщений %d, нужно %d: %v", len(fm.texts), tc.want, fm.texts)
			}
		})
	}
}
