package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// placeMini запоминает, каким местом (сайт или Telegram) назвал запрос слой
// веба: от этого зависят списки способов оплаты.
type placeMini struct {
	*fakeMini
	menuWeb, topUpWeb, walletWeb []bool
}

func (p *placeMini) MiniWallet(_ context.Context, _ int64, web bool) MiniWalletDTO {
	p.walletWeb = append(p.walletWeb, web)
	return MiniWalletDTO{}
}

func (p *placeMini) MiniMenu(_ context.Context, _ int64, web bool) MiniMenuDTO {
	p.menuWeb = append(p.menuWeb, web)
	return MiniMenuDTO{}
}

func (p *placeMini) MiniTopUpOptions(_ context.Context, _ int64, web bool) MiniTopUpOptionsDTO {
	p.topUpWeb = append(p.topUpWeb, web)
	return MiniTopUpOptionsDTO{}
}

func TestPayPlacePassedFromSession(t *testing.T) {
	p := &placeMini{fakeMini: &fakeMini{}}
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	s.SetMiniApp(p)
	h := s.wrap(s.mux())

	for _, web := range []bool{true, false} {
		tok := issueJWT(1000000001, web, jwtKey(fakeBotToken), time.Hour, 0, 0)
		for _, path := range []string{"/api/miniapp/menu", "/api/miniapp/topup/options", "/api/miniapp/wallet"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, gateReq(http.MethodGet, path, "", tok, "203.0.113.10"))
			if w.Code != http.StatusOK {
				t.Fatalf("%s (сайт=%v): код %d", path, web, w.Code)
			}
		}
		if n := len(p.menuWeb); n == 0 || p.menuWeb[n-1] != web {
			t.Fatalf("меню получило место %v, ждали сайт=%v", p.menuWeb, web)
		}
		if n := len(p.topUpWeb); n == 0 || p.topUpWeb[n-1] != web {
			t.Fatalf("пополнение получило место %v, ждали сайт=%v", p.topUpWeb, web)
		}
		if n := len(p.walletWeb); n == 0 || p.walletWeb[n-1] != web {
			t.Fatalf("кошелёк получил место %v, ждали сайт=%v", p.walletWeb, web)
		}
	}
}
