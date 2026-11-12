package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Гостю адрес чата отдаётся, только когда чат разрешён и на экране входа.
func TestCabinetConfigChat(t *testing.T) {
	const src = "https://chat.example.com/w.js"
	for _, tc := range []struct {
		name string
		src  string
		all  bool
		want string
	}{
		{"чата нет", "", true, ""},
		{"после входа", src, false, ""},
		{"везде", src, true, src},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := gateServer(t, &fakeMini{chatSrc: tc.src, chatAll: tc.all})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, gateReq(http.MethodGet, "/api/cabinet/config", "", "", "203.0.113.70"))
			var cfg map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
				t.Fatalf("ответ %d: %v", w.Code, err)
			}
			got, _ := cfg["chat_src"].(string)
			if got != tc.want {
				t.Fatalf("chat_src = %q", got)
			}
			if _, has := cfg["chat_src"]; has != (tc.want != "") {
				t.Fatal("пустой chat_src попал в ответ")
			}
		})
	}
}

// Вошедшему чат приходит с профилем — и только в кабинете, не в мини-аппе.
func TestMiniMeChat(t *testing.T) {
	const src = "https://chat.example.com/w.js"
	f := &fakeMini{chatSrc: src}
	h := gateServer(t, f)
	for _, tc := range []struct {
		name string
		web  bool
		want string
	}{{"кабинет", true, src}, {"мини-апп", false, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			tok := issueJWT(1000000001, tc.web, jwtKey(fakeBotToken), time.Hour, f.SessionVersion(), 0)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, gateReq(http.MethodGet, "/api/miniapp/me", "", tok, "203.0.113.71"))
			var me MiniMeDTO
			if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != http.StatusOK {
				t.Fatalf("ответ %d: %v", w.Code, err)
			}
			if me.TgID != 1000000001 || me.ChatSrc != tc.want {
				t.Fatalf("профиль %d, chat_src = %q", me.TgID, me.ChatSrc)
			}
		})
	}
}

// Встроенные страницы ставят чат на входе, по профилю и на сбросе пароля.
func TestPagesMountChat(t *testing.T) {
	for _, name := range []string{"index.html", "minimal.html"} {
		page, err := miniStaticFS.ReadFile("miniapp_static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"mountChat(cfg.chat_src)",
			"mountChat(m.chat_src)",
			"guestChat();",
			"if(CHAT_ON&&!cfg.chat_src){location.reload();return;}",
		} {
			if !strings.Contains(string(page), want) {
				t.Errorf("%s: нет %q", name, want)
			}
		}
	}
}
