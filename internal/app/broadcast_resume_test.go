package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

// «chat not found» — такой же окончательный отказ, как блокировка бота:
// человек помечается и не тратит следующие рассылки. Сбой сети — нет.
func TestBroadcast_ChatNotFound(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	for _, id := range []int64{1000000001, 1000000002, 1000000003} {
		_ = fs.UpsertUser(ctx, id)
	}
	fm.sendErrs = map[int64]error{
		1000000002: fmt.Errorf("%w, Bad Request: chat not found", bot.ErrorBadRequest),
		1000000003: errors.New("connection reset"),
	}
	a.getUI(planAdmin).broadcastText = "msg {id}"
	planTap(t, a, "bc:send")
	waitFor(t, func() bool { return !a.bcastRunning.Load() })

	if fs.unreachable[1000000002] == "" {
		t.Fatal("чат, которого нет, не помечен")
	}
	if fs.unreachable[1000000003] != "" {
		t.Fatal("сбой сети принят за недоступный чат")
	}
	r := fm.last()
	if !strings.Contains(r, "Доставлено: 1\n") || !strings.Contains(r, "Не доставлено: 1\n") ||
		!strings.Contains(r, "Недоступны (заблокировали бота или нет чата): 1") {
		t.Fatalf("итог:\n%s", r)
	}
}

func bcastFilesExist(a *App) bool {
	for _, n := range []string{bcastJobFile, bcastPosFile} {
		if _, err := os.Stat(a.bcastPath(n)); err == nil {
			return true
		}
	}
	return false
}

// Рассылка, прерванная перезапуском, продолжается с того места, где
// остановилась, а итог считает всю рассылку целиком.
func TestBroadcast_Resume(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	ids := []int64{1000000001, 1000000002, 1000000003}
	for _, id := range ids {
		_ = fs.UpsertUser(ctx, id)
	}
	fm.editOK = true
	status := fm.SendKB(ctx, planAdmin, "🚀 запущена", nil)
	a.saveBcastJob(&bcastJob{Admin: planAdmin, Text: "msg {id}", Label: "VIP", IDs: ids,
		StatusID: status, Started: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
	a.saveBcastPos(&bcastPos{Next: 1, Sent: 1})

	a.resumeBroadcast(ctx)
	waitFor(t, func() bool { return !a.bcastRunning.Load() })

	if got := bcastSent(fm); !slices.Equal(got, ids[1:]) {
		t.Fatalf("после перезапуска ушло %v", got)
	}
	// И «продолжена», и итог — правкой прежнего статуса, без новых сообщений.
	fm.mu.Lock()
	edited := strings.Join(fm.edited, "\n---\n")
	fm.mu.Unlock()
	if strings.Contains(fm.joined(), "Рассылка продолжена") || !strings.Contains(edited, "Рассылка продолжена") ||
		!strings.Contains(edited, "Осталось отправить: 2") {
		t.Fatalf("о продолжении сказали не в статусе:\n%s\n===\n%s", fm.joined(), edited)
	}
	r := fm.edited[len(fm.edited)-1]
	if !strings.Contains(r, "Кому: VIP\n") || !strings.Contains(r, "Получателей: 3\n") || !strings.Contains(r, "Доставлено: 3\n") {
		t.Fatalf("итог:\n%s", r)
	}
	if bcastFilesExist(a) {
		t.Fatal("задание не убрано после окончания")
	}
}

// Через сутки рассылка не продолжается сама, но админ узнаёт, сколько не ушло.
func TestBroadcast_ResumeStale(t *testing.T) {
	ctx := context.Background()
	a, fm, _ := planAdminApp(t)
	a.saveBcastJob(&bcastJob{Admin: planAdmin, Text: "msg {id}", Label: "Всем", IDs: []int64{1000000001, 1000000002, 1000000003},
		Started: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)})
	a.saveBcastPos(&bcastPos{Next: 1, Sent: 1})

	a.resumeBroadcast(ctx)

	if got := bcastSent(fm); len(got) != 0 {
		t.Fatalf("устаревшая рассылка ушла: %v", got)
	}
	if r := fm.last(); !strings.Contains(r, "прервана") || !strings.Contains(r, "Не отправлено: 2") {
		t.Fatalf("итог:\n%s", r)
	}
	if a.bcastRunning.Load() || bcastFilesExist(a) {
		t.Fatal("задание осталось висеть")
	}
}

// Прогресс не читается — сколько сообщений ушло, неизвестно: рассылка не
// продолжается, иначе уже получившие получили бы её второй раз. Без файла
// прогресса она идёт с начала: до первого сообщения дело не дошло.
func TestBroadcast_ResumeLostProgress(t *testing.T) {
	ids := []int64{1000000001, 1000000002, 1000000003}
	for _, tc := range []struct {
		name     string
		pos      string
		wantSent int
	}{
		{"файл прогресса испорчен", "{", 0},
		{"файл прогресса пуст", "", 0},
		{"файла прогресса нет", "-", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a, fm, fs := planAdminApp(t)
			for _, id := range ids {
				_ = fs.UpsertUser(ctx, id)
			}
			a.saveBcastJob(&bcastJob{Admin: planAdmin, Text: "msg {id}", Label: "Всем", IDs: ids,
				Started: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
			if tc.pos != "-" {
				if err := os.WriteFile(a.bcastPath(bcastPosFile), []byte(tc.pos), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			a.resumeBroadcast(ctx)
			waitFor(t, func() bool { return !a.bcastRunning.Load() })

			if got := bcastSent(fm); len(got) != tc.wantSent {
				t.Fatalf("ушло %v, ожидалось сообщений: %d", got, tc.wantSent)
			}
			if tc.wantSent == 0 {
				if r := fm.last(); !strings.Contains(r, "не удалось прочитать") || !strings.Contains(r, "Получателей: 3") {
					t.Fatalf("итог:\n%s", r)
				}
			}
			if bcastFilesExist(a) {
				t.Fatal("задание осталось висеть")
			}
		})
	}
}

// Остановка бота посреди рассылки оставляет задание на диске.
func TestBroadcast_ShutdownKeepsJob(t *testing.T) {
	a, fm, _ := planAdminApp(t)
	job := &bcastJob{Admin: planAdmin, Text: "msg {id}", IDs: []int64{1000000001}, Started: time.Now().UTC().Format(time.RFC3339)}
	a.saveBcastJob(job)
	pos := &bcastPos{}
	a.saveBcastPos(pos)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.bcastLoop(ctx, job, pos)
	if len(bcastSent(fm)) != 0 || pos.Next != 0 || !bcastFilesExist(a) {
		t.Fatalf("задание потеряно: next=%d", pos.Next)
	}
}

// Рассылка повторяет отправку без разметки, только когда Telegram отверг
// разметку: после сбоя сети сообщение могло уже дойти, и повтор дал бы дубль.
// Обычные уведомления ведут себя как раньше.
func TestSendErr_NoRetryAfterAmbiguousError(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		desc       string
		sendErr    int32
		sendKB     int32
		wantErrOut bool
	}{
		{"5xx", http.StatusInternalServerError, "Internal Server Error", 1, 2, true},
		{"разметка", http.StatusBadRequest, "Bad Request: can't parse entities: unsupported start tag", 2, 2, true},
		{"нет чата", http.StatusBadRequest, "Bad Request: chat not found", 1, 2, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":%q}`, tc.status, tc.desc)
			}))
			defer srv.Close()
			b, err := bot.New("123:test", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
			if err != nil {
				t.Fatal(err)
			}
			m := botMessenger{b: b, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
			ctx := context.Background()

			if _, err := m.SendErr(ctx, 1, "<b>привет</b>"); (err != nil) != tc.wantErrOut {
				t.Fatalf("SendErr: err=%v", err)
			}
			if n := calls.Swap(0); n != tc.sendErr {
				t.Fatalf("SendErr: запросов %d, ждали %d", n, tc.sendErr)
			}
			m.SendKB(ctx, 1, "<b>привет</b>", nil)
			if n := calls.Swap(0); n != tc.sendKB {
				t.Fatalf("SendKB: запросов %d, ждали %d", n, tc.sendKB)
			}
		})
	}
}

// Прогресс пишется на диск после каждого сообщения, а не только в конце:
// иначе падение бота откатило бы рассылку к началу.
func TestBroadcast_ProgressSavedEachMessage(t *testing.T) {
	ctx := context.Background()
	a, fm, fs := planAdminApp(t)
	ids := []int64{1000000001, 1000000002}
	for _, id := range ids {
		_ = fs.UpsertUser(ctx, id)
	}
	var seen bcastPos
	sends := 0
	fm.onSendErr = func(id int64) {
		// Порядок получателей хранилище не обещает: снимок берётся на втором
		// по счёту сообщении, кому бы оно ни шло.
		if !slices.Contains(ids, id) {
			return
		}
		if sends++; sends != 2 {
			return
		}
		raw, err := os.ReadFile(a.bcastPath(bcastPosFile))
		if err == nil {
			err = json.Unmarshal(raw, &seen)
		}
		if err != nil {
			t.Errorf("прогресс: %v", err)
		}
	}
	a.getUI(planAdmin).broadcastText = "msg {id}"
	planTap(t, a, "bc:send")
	waitFor(t, func() bool { return !a.bcastRunning.Load() })
	if seen.Next != 1 || seen.Sent != 1 {
		t.Fatalf("на втором сообщении прогресс на диске %+v", seen)
	}
}

// Отправку оборвала остановка бота — этот человек не засчитан и не пропущен:
// после запуска рассылка начнёт с него.
func TestBroadcast_ShutdownMidSend(t *testing.T) {
	a, fm, _ := planAdminApp(t)
	ids := []int64{1000000001, 1000000002, 1000000003}
	job := &bcastJob{Admin: planAdmin, Text: "msg {id}", IDs: ids, Started: time.Now().UTC().Format(time.RFC3339)}
	a.saveBcastJob(job)
	pos := &bcastPos{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fm.sendErrs = map[int64]error{ids[1]: context.Canceled}
	fm.onSendErr = func(id int64) {
		if id == ids[1] {
			cancel()
		}
	}
	a.bcastLoop(ctx, job, pos)
	if pos.Next != 1 || pos.Sent != 1 || pos.Failed != 0 || !bcastFilesExist(a) {
		t.Fatalf("прогресс после остановки %+v", *pos)
	}
}
