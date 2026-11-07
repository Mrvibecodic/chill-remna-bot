package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type avatarMini struct {
	*fakeMini
	asked int64
}

func (m *avatarMini) MiniAvatar(_ context.Context, id int64) ([]byte, bool) {
	m.asked = id
	if id == 1000000001 {
		return []byte{0xFF, 0xD8, 0xFF, 0xE0}, true
	}
	return nil, false
}

// Аватарка — только своя (id из пропуска), только с пропуском, JPEG, который
// браузер не хранит (адрес у всех один); без фото — 404.
func TestMiniAvatarEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		token string
		code  int
	}{
		{"без пропуска", "", http.StatusUnauthorized},
		{"с фото", webToken(&fakeMini{}, 1000000001, 0), http.StatusOK},
		{"без фото", webToken(&fakeMini{}, 1000000002, 0), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &avatarMini{fakeMini: &fakeMini{}}
			srv := newServer(nil, nil)
			srv.allowPlainHTTP = true
			srv.SetMiniApp(m)
			h := srv.wrap(srv.mux())
			w := httptest.NewRecorder()
			h.ServeHTTP(w, gateReq(http.MethodGet, "/api/miniapp/avatar?id=1000000001", "", tc.token, "203.0.113.70"))
			if w.Code != tc.code {
				t.Fatalf("код %d, ждали %d", w.Code, tc.code)
			}
			if tc.code == http.StatusOK {
				if ct, cc := w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"); ct != "image/jpeg" || cc != "no-store" {
					t.Fatalf("заголовки: %q %q", ct, cc)
				}
			}
			if tc.token != "" && tc.name == "без фото" && m.asked != 1000000002 {
				t.Fatalf("спросили фото чужого id %d", m.asked)
			}
		})
	}
}
