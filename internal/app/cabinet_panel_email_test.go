package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// Подтверждённая почта дописывается в уже заведённую учётку панели;
// неподтверждённая туда не попадает.
func TestVerifiedEmailFillsPanelUser(t *testing.T) {
	a, _ := cabinetApp(t)
	a.runInline = true
	ctx := context.Background()
	id, err := a.CabinetEmailRegister(ctx, "one@example.com", "password1", "")
	if err != nil {
		t.Fatalf("регистрация: %v", err)
	}
	a.waitMail()

	var (
		mu      sync.Mutex
		patched []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPatch:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			e, _ := body["email"].(string)
			mu.Lock()
			patched = append(patched, e)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-1"}}`))
		case strings.HasPrefix(r.URL.Path, "/api/users/by-username/"):
			_, _ = w.Write([]byte(`{"response":{"uuid":"u-1","id":9,"username":"` + strings.TrimPrefix(r.URL.Path, "/api/users/by-username/") +
				`","tag":"` + remnawave.BotTag + `","email":""}}`))
		default:
			_, _ = w.Write([]byte(`{"response":{"users":[]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	a.panel = a.newPanel(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	if got := a.accountEmailFor(ctx, id); got != "" {
		t.Fatalf("неподтверждённая почта отдана для панели: %q", got)
	}
	tok, err := a.newEmailToken(ctx, id, "one@example.com", model.EmailPurposeVerify, verifyTTL)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CabinetVerifyEmail(ctx, tok); err != nil {
		t.Fatalf("подтверждение: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(patched) != 1 || patched[0] != "one@example.com" {
		t.Fatalf("почта в учётку панели: %v", patched)
	}
}
