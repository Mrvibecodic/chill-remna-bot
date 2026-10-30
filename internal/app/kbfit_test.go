package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func kbLabels(rows [][]models.InlineKeyboardButton) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		for _, b := range r {
			out[i] = append(out[i], b.Text)
		}
	}
	return out
}

func cbRow(labels ...string) []models.InlineKeyboardButton {
	row := make([]models.InlineKeyboardButton, len(labels))
	for i, l := range labels {
		row[i] = btn(l, "x:"+l)
	}
	return row
}

func TestFitKeyboard(t *testing.T) {
	long := "🛟 Изменить ссылку поддержки"
	many := cbRow("1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15", "16", "17", "18", "19", "20 Очень длинная подпись")
	tests := []struct {
		name string
		in   [][]models.InlineKeyboardButton
		want [][]string
	}{
		{"меню с подпиской", [][]models.InlineKeyboardButton{cbRow("📲 Мои подписки", "🔄 Продлить", "👛 Кошелёк")},
			[][]string{{"📲 Мои подписки"}, {"🔄 Продлить", "👛 Кошелёк"}}},
		{"меню на английском", [][]models.InlineKeyboardButton{cbRow("📲 My subscriptions", "🔄 Renew", "👛 Wallet")},
			[][]string{{"📲 My subscriptions"}, {"🔄 Renew", "👛 Wallet"}}},
		{"меню без подписки влезает", [][]models.InlineKeyboardButton{cbRow("🎁 Триал", "💳 Купить", "👛 Кошелёк")},
			[][]string{{"🎁 Триал", "💳 Купить", "👛 Кошелёк"}}},
		{"назад и главная", [][]models.InlineKeyboardButton{cbRow("⬅️ Назад", "🏠 Главная")},
			[][]string{{"⬅️ Назад", "🏠 Главная"}}},
		{"две длинные", [][]models.InlineKeyboardButton{cbRow("👥 Изменить ссылку группы", long), cbRow("📜 Документы")},
			[][]string{{"👥 Изменить ссылку группы"}, {long}, {"📜 Документы"}}},
		{"одна длинная остаётся", [][]models.InlineKeyboardButton{cbRow(strings.Repeat("Очень длинное название ", 4))},
			[][]string{{strings.Repeat("Очень длинное название ", 4)}}},
		{"четыре коротких", [][]models.InlineKeyboardButton{cbRow("⬜ 7д", "⬜ 3д", "⬜ 1д", "✅ 0д")},
			[][]string{{"⬜ 7д", "⬜ 3д", "⬜ 1д", "✅ 0д"}}},
		{"три по одной", [][]models.InlineKeyboardButton{cbRow("📱 1 устройство", "📱📱📱 3 устройства", "✏️ Свой лимит")},
			[][]string{{"📱 1 устройство"}, {"📱📱📱 3 устройства"}, {"✏️ Свой лимит"}}},
		{"длинный ряд жадно", [][]models.InlineKeyboardButton{many},
			[][]string{{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, {"11", "12", "13", "14", "15", "16", "17", "18", "19"}, {"20 Очень длинная подпись"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := kbLabels(fitKeyboard(tc.in))
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(tc.want)
			if string(gj) != string(wj) {
				t.Fatalf("\nхотели %s\nполучили %s", wj, gj)
			}
		})
	}
}

func TestFitKeyboardKeepsSliceWhenFits(t *testing.T) {
	in := [][]models.InlineKeyboardButton{cbRow("⬅️ Назад", "🏠 Главная")}
	if out := fitKeyboard(in); &out[0] != &in[0] {
		t.Fatal("клавиатура, которая влезает, пересобрана")
	}
}

func TestBtnWidth(t *testing.T) {
	if w1, w2 := btnWidth(btn("👨‍👩‍👧 A", "")), btnWidth(btn("👨 A", "")); w1 != w2 {
		t.Fatalf("составной эмодзи посчитан не как один значок: %v против %v", w1, w2)
	}
	plain := btnWidth(btn("💬 Группа", ""))
	link := btnWidth(models.InlineKeyboardButton{Text: "💬 Группа", URL: "https://example.com"})
	if link <= plain {
		t.Fatal("значок ссылки в углу не учтён")
	}
	// Контакты стоят в одном ряду и должны там остаться.
	row := []models.InlineKeyboardButton{
		{Text: "💬 Группа", URL: "https://example.com/g"},
		{Text: "🛟 Поддержка", URL: "https://example.com/s"},
	}
	if !rowFits(row) {
		t.Fatal("ряд контактов разбит")
	}
}

// Клавиатура раскладывается на пути в Telegram: проверяем, что ушло в запросе.
func TestMessengerSendsFittedKeyboard(t *testing.T) {
	var (
		mu   sync.Mutex
		sent = map[string]string{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		mu.Lock()
		sent[method] = r.FormValue("reply_markup")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1,"chat":{"id":1}}}`)
	}))
	defer srv.Close()
	b, err := bot.New("123:test", bot.WithServerURL(srv.URL), bot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	m := botMessenger{b: b, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx := context.Background()
	rows := [][]models.InlineKeyboardButton{cbRow("📲 Мои подписки", "🔄 Продлить", "👛 Кошелёк")}

	m.SendKB(ctx, 1, "меню", rows)
	m.EditCaption(ctx, 1, 1, "меню", rows)
	_, _ = m.SendBanner(ctx, 1, &models.InputFileString{Data: "file"}, "меню", nil, models.InlineKeyboardMarkup{InlineKeyboard: rows})

	want := `[["📲 Мои подписки"],["🔄 Продлить","👛 Кошелёк"]]`
	for _, method := range []string{"sendMessage", "editMessageCaption", "sendPhoto"} {
		var kb models.InlineKeyboardMarkup
		mu.Lock()
		raw := sent[method]
		mu.Unlock()
		if err := json.Unmarshal([]byte(raw), &kb); err != nil {
			t.Fatalf("%s: разметка не ушла: %q", method, raw)
		}
		got, _ := json.Marshal(kbLabels(kb.InlineKeyboard))
		if string(got) != want {
			t.Fatalf("%s: %s", method, got)
		}
	}
	if len(rows) != 1 || len(rows[0]) != 3 {
		t.Fatal("исходные ряды экрана изменены")
	}
}

// Способы оплаты: название и «Описание» стоят парой, поэтому при нехватке
// места столбиком встают все пары, а не только длинные.
func TestPayMethodTextsStackUniformly(t *testing.T) {
	a, fm, _ := newTextsApp(t)
	a.handleCallback(context.Background(), cb(100, "tx:pm:t"))
	rows := fm.lastKB
	if len(rows) < 3 {
		t.Fatalf("экран не отрисован: %v", kbLabels(rows))
	}
	for _, r := range rows[:len(rows)-1] {
		if len(r) != 1 {
			t.Fatalf("пары не встали столбиком: %v", kbLabels(rows))
		}
	}
	if !strings.HasPrefix(rows[1][0].CallbackData, "tx:k:") || rows[0][0].CallbackData == rows[1][0].CallbackData {
		t.Fatalf("за названием не идёт его описание: %v", kbLabels(rows))
	}
}
