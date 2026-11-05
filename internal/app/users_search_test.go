package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func searchApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, msg, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	return a, msg, fs
}

// Поиск по нику: админ жмёт кнопку, присылает запрос, получает выдачу с
// кнопкой на карточку найденного.
func TestUserSearch_ByUsername(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 1001)
	_ = fs.SetUserInfo(ctx, 1001, "PetrovIvan", "Иван")
	_ = fs.UpsertUser(ctx, 1002)
	_ = fs.SetUserInfo(ctx, 1002, "sidorov", "Пётр")

	a.handleCallback(ctx, cb(100, "usr:find"))
	if a.getUI(100).adminInput != "user_find" {
		t.Fatal("ввод поискового запроса не запрошен")
	}
	a.handleMessage(ctx, msgText(100, "petrov"))

	var found, extra bool
	for _, d := range msg.cbData {
		switch d {
		case "usr:view:1001":
			found = true
		case "usr:view:1002":
			extra = true
		}
	}
	if !found {
		t.Fatalf("совпадение не показано: %v", msg.cbData)
	}
	if extra {
		t.Fatalf("в выдачу попал посторонний: %v", msg.cbData)
	}
}

// Чистый Telegram ID существующего пользователя открывает карточку сразу.
func TestUserSearch_ExactIDOpensCard(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 2002)

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "2002"))

	var hasBlock bool
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "usr:block:2002") {
			hasBlock = true
		}
	}
	if !hasBlock {
		t.Fatalf("карточка не открыта: %v", msg.cbData)
	}
}

// «Назад» из карточки возвращает в выдачу поиска, а не на первую страницу
// полного списка — иначе найденный человек стоил бы нового поиска.
func TestUserSearch_BackReturnsToResults(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 3003)
	_ = fs.SetUserInfo(ctx, 3003, "targetnick", "Цель")

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "targetnick"))
	msg.cbData = nil
	a.handleCallback(ctx, cb(100, "usr:view:3003"))

	var back string
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "usr:fpage:") || d == "usr:list" {
			back = d
		}
	}
	if back != "usr:fpage:0" {
		t.Fatalf("«Назад» ведёт в %q, ожидалась выдача поиска", back)
	}
}

// Пустая выдача не молчит: админ видит, что не нашлось, и может искать снова.
func TestUserSearch_EmptyResult(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 4004)

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "такогонет"))

	var again bool
	for _, d := range msg.cbData {
		if d == "usr:find" {
			again = true
		}
	}
	if !again {
		t.Fatalf("на пустой выдаче нет кнопки повторного поиска: %v", msg.cbData)
	}
}

// Один символ «@» — не запрос: раньше он превращался в пустую подстроку и
// выдавал всю базу.
func TestUserSearch_AtSignIsNotEveryone(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	for _, id := range []int64{5001, 5002} {
		_ = fs.UpsertUser(ctx, id)
	}
	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "@"))
	for _, d := range msg.cbData {
		if strings.HasPrefix(d, "usr:view:") {
			t.Fatalf("пустой запрос выдал пользователей: %v", msg.cbData)
		}
	}
}

// Запрос уходит в HTML-сообщение: спецсимволы обязаны экранироваться, иначе
// Telegram отвергает сообщение целиком и экран не появляется.
func TestUserSearch_QueryIsEscaped(t *testing.T) {
	a, msg, fs := searchApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 5003)

	a.handleCallback(ctx, cb(100, "usr:find"))
	a.handleMessage(ctx, msgText(100, "R&D <тест>"))
	var seen bool
	for _, txt := range msg.texts {
		if strings.Contains(txt, "R&amp;D") {
			seen = true
		}
		if strings.Contains(txt, "<тест>") {
			t.Fatalf("сырой ввод попал в HTML: %q", txt)
		}
	}
	if !seen {
		t.Fatalf("экран поиска не показан: %v", msg.texts)
	}
}

// «Снять доступ у всех» обязано чистить и предзаполненные ID: иначе человек
// вернёт себе доступ при первом же входе.
func TestWhitelist_ClearAlsoDropsPrefilledIDs(t *testing.T) {
	a, _, fs := searchApp(t)
	ctx := context.Background()
	if err := fs.AddWhitelistID(ctx, 6001); err != nil {
		t.Fatal(err)
	}
	a.handleCallback(ctx, cb(100, "usr:wlclearok"))
	if ok, _ := fs.IsWhitelistID(ctx, 6001); ok {
		t.Fatal("предзаполненный ID пережил снятие доступа у всех")
	}
}

// В боте никого, учётка нашлась в панели: подсказка зависит от того, есть ли
// у учётки Telegram ID и заходил ли человек в бота. Кнопка на карточку —
// только когда карточка существует.
func TestUserSearch_PanelHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		tg := map[string]string{"freeacc": "null", "newacc": "1000000001", "knownacc": "1000000002", "knownacc_addsub": "null"}
		if name, ok := strings.CutPrefix(r.URL.Path, "/api/users/by-username/"); ok {
			if id, found := tg[name]; found {
				tag := "null"
				if strings.HasSuffix(name, "_addsub") {
					tag = `"` + remnawave.BotTagAdd + `"`
				}
				_, _ = w.Write([]byte(`{"response":{"uuid":"u-` + name + `","username":"` + name + `","telegramId":` + id + `,"tag":` + tag + `,"expireAt":"2099-01-01T00:00:00Z"}}`))
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"User not found"}`))
	}))
	t.Cleanup(srv.Close)

	tests := []struct {
		query, mode, want, card string
	}{
		{"freeacc", model.AccessPublic, "Telegram ID у неё не указан", ""},
		{"newacc", model.AccessPublic, "ещё не заходил в бота", ""},
		{"newacc", model.AccessWhitelist, "Добавить ID в вайтлист", ""},
		{"knownacc", model.AccessPublic, "по кнопке ниже", "usr:view:1000000002"},
		{"knownacc_addsub", model.AccessPublic, "доп-подписка", ""},
	}
	for _, tt := range tests {
		t.Run(tt.query+"/"+tt.mode, func(t *testing.T) {
			a, msg, fs := searchApp(t)
			a.botCfg.AccessMode = tt.mode
			ctx := context.Background()
			a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
			_ = fs.UpsertUser(ctx, 1000000002)

			a.handleCallback(ctx, cb(100, "usr:find"))
			a.handleMessage(ctx, msgText(100, tt.query))

			if countContains(msg.texts, tt.want) == 0 {
				t.Fatalf("нет подсказки %q: %v", tt.want, msg.texts)
			}
			closed := countContains(msg.texts, "открыт не для всех") > 0
			if closed != (tt.mode != model.AccessPublic) {
				t.Fatalf("строка про закрытый бот: %v, режим %s", closed, tt.mode)
			}
			if strings.HasSuffix(tt.query, "_addsub") && countContains(msg.texts, "Впишите") > 0 {
				t.Fatalf("доп-подписке советуют вписать Telegram ID: %v", msg.texts)
			}
			var cards []string
			for _, d := range msg.cbData {
				if strings.HasPrefix(d, "usr:view:") {
					cards = append(cards, d)
				}
			}
			switch {
			case tt.card == "" && len(cards) > 0:
				t.Fatalf("кнопка на несуществующую карточку: %v", cards)
			case tt.card != "" && (len(cards) != 1 || cards[0] != tt.card):
				t.Fatalf("карточка %q, получили %v", tt.card, cards)
			}
		})
	}
}
