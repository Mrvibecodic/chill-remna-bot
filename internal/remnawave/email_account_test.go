package remnawave

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// Аккаунт кабинета по почте: id — отрицательная метка времени в наносекундах.
// Фейк повторяет валидацию контракта панели (create-user.command.ts):
// username ^[a-zA-Z0-9_-]+$ длиной 3–36, telegramId — число не меньше нуля,
// фильтр по telegramId тоже только неотрицательный. Мягкая заглушка здесь
// пропустила бы 400 мимо зелёных тестов.
func TestEmailAccountCreatedAndExtendedWithinContract(t *testing.T) {
	const id int64 = -(1<<60 + 1) // 19 знаков, больше 2^53 по модулю
	userRe := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	var (
		mu       sync.Mutex
		created  map[string]any
		posts    int
		patches  int
		badCalls []string
	)
	bad := func(w http.ResponseWriter, what string) {
		mu.Lock()
		badCalls = append(badCalls, what)
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Validation failed","errorCode":"A000"}`))
	}
	exp := time.Now().UTC().Add(720 * time.Hour).Format(time.RFC3339)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/stream", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Query().Get("telegramId"), "-") {
			bad(w, "stream с отрицательным telegramId")
			return
		}
		_, _ = w.Write([]byte(`{"response":[]}`))
	})
	mux.HandleFunc("/api/users/tg/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/tg/-") {
			bad(w, "поиск по отрицательному telegramId")
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
	})
	mux.HandleFunc("/api/users/by-username/", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := created
		mu.Unlock()
		if c == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"User not found","errorCode":"A063"}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","id":7,"username":"` + c["username"].(string) +
			`","tag":"` + BotTag + `","status":"ACTIVE","expireAt":"` + exp + `","subscriptionUrl":"https://example.test/s"}}`))
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if tgRaw, ok := body["telegramId"]; ok && tgRaw != nil {
			tg, isNum := tgRaw.(float64)
			if !isNum || tg < 0 || tg != float64(int64(tg)) {
				bad(w, "telegramId не целое неотрицательное число")
				return
			}
		}
		if r.Method == http.MethodPost {
			name, _ := body["username"].(string)
			if !userRe.MatchString(name) || len(name) < 3 || len(name) > 36 {
				bad(w, "username вне контракта: "+name)
				return
			}
			mu.Lock()
			created = body
			posts++
			mu.Unlock()
		} else {
			mu.Lock()
			patches++
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","subscriptionUrl":"https://example.test/s","expireAt":"` + exp + `"}}`))
	})
	mux.HandleFunc("/api/internal-squads", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"total":0,"internalSquads":[]}}`))
	})
	mux.HandleFunc("/api/external-squads", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{"total":0,"externalSquads":[]}}`))
	})
	mux.HandleFunc("/api/users/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"response":{}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()

	// Создание.
	link, _, err := c.CreateOrUpdateUserDays(ctx, id, 30, UserLimits{})
	if err != nil {
		t.Fatalf("создание: %v (отказы панели: %v)", err, badCalls)
	}
	if link != "https://example.test/s" {
		t.Fatalf("ссылка: %q", link)
	}
	mu.Lock()
	c0, p0, pt0, bc := created, posts, patches, badCalls
	mu.Unlock()
	if len(bc) != 0 || p0 != 1 || pt0 != 0 {
		t.Fatalf("панель отвергла запросы: %v (post=%d patch=%d)", bc, p0, pt0)
	}
	if c0["username"] != botUsername(id) {
		t.Fatalf("имя аккаунта: %v", c0["username"])
	}
	if _, has := c0["telegramId"]; has {
		t.Fatalf("отрицательный telegramId ушёл в панель: %v", c0["telegramId"])
	}

	// Продление: аккаунт уже есть — найден по имени и обновлён, не заведён второй.
	if _, _, err := c.CreateOrUpdateUserDays(ctx, id, 30, UserLimits{}); err != nil {
		t.Fatalf("продление: %v (отказы панели: %v)", err, badCalls)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(badCalls) != 0 || posts != 1 || patches != 1 {
		t.Fatalf("продление: отказы=%v post=%d patch=%d", badCalls, posts, patches)
	}
}
