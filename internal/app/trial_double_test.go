package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
)

// emptyPanelCountingGrants — панель, которая не знает ни одного пользователя и
// считает создания/продления учётки. Любой поиск отвечает пустым списком.
func emptyPanelCountingGrants(t *testing.T, grants *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[]}`))
			return
		}
		if (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.HasSuffix(r.URL.Path, "/api/users") {
			*grants++
			_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// noRowCreateStore — хранилище без побочного создания строки в SetSubExpiry:
// настоящая база UPDATE-ом строку не заводит, а фейк заводил, и двойная выдача
// в тесте не воспроизводилась.
type noRowCreateStore struct{ *fakeStore }

func (s noRowCreateStore) SetSubExpiry(ctx context.Context, id int64, expireAt, kind string) error {
	if u, _ := s.fakeStore.GetUser(ctx, id); u == nil {
		return nil
	}
	return s.fakeStore.SetSubExpiry(ctx, id, expireAt, kind)
}

// Триал не выдаётся дважды, даже если человек пришёл из мини-аппа и записи в
// users ещё нет: отметка «триал использован» — это UPDATE, а на несуществующей
// строке он молча ничего не меняет, и каждый повтор добавлял дни.
func TestMiniTrial_NoDoubleGrantWithoutUserRow(t *testing.T) {
	ctx := context.Background()
	grants := 0
	srv := emptyPanelCountingGrants(t, &grants)
	a, fs := snapApp(t, srv.URL)
	a.store = noRowCreateStore{fs}
	const u int64 = 556
	a.botCfg.Trial = model.TrialConfig{Enabled: true, Days: 3}

	if r := a.MiniTrial(ctx, u); r.Error != "" || !r.OK {
		t.Fatalf("первый триал не выдан: %+v", r)
	}
	if r := a.MiniTrial(ctx, u); r.Error == "" {
		t.Fatalf("второй триал выдан повторно: %+v", r)
	}
	if grants != 1 {
		t.Fatalf("панель выдала триал %d раз(а), ожидался один", grants)
	}
	if u2, _ := fs.GetUser(ctx, u); u2 == nil || u2.TrialUsedAt == "" {
		t.Fatalf("триал не отмечен в базе: %+v", u2)
	}
}
