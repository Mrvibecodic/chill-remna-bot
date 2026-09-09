package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeMini — двойник поставщика данных. Интерфейс ВСТРОЕН намеренно: методы, до
// которых проверка не доходит, остаются нереализованными, и случайный вызов
// такого метода уронит тест с внятной паникой вместо тихого «ноль по
// умолчанию».
type fakeMini struct {
	MiniProvider
	epoch   int
	blocked bool
	forgot  []string
	bound   int64
}

const fakeBotToken = "123456:TESTTOKEN"

func (f *fakeMini) MiniEnabled() bool    { return true }
func (f *fakeMini) CabinetEnabled() bool { return true }
func (f *fakeMini) MiniBotToken() string { return fakeBotToken }
func (f *fakeMini) SessionVersion() int  { return 0 }
func (f *fakeMini) MailReady() bool      { return true }
func (f *fakeMini) CabinetPath() string  { return "/cabinet/" }
func (f *fakeMini) CabinetBotUsername() string {
	return "testbot"
}
func (f *fakeMini) SessionEpoch(context.Context, int64) int         { return f.epoch }
func (f *fakeMini) MiniBlocked(context.Context, int64) bool         { return false }
func (f *fakeMini) MiniAccessDenied(context.Context, int64) bool    { return false }
func (f *fakeMini) MiniLegalRequired(context.Context, int64) bool   { return false }
func (f *fakeMini) CabinetGate(context.Context, int64, bool) error  { return nil }
func (f *fakeMini) CabinetMoneyBlocked(context.Context, int64) bool { return f.blocked }
func (f *fakeMini) CabinetForgotPassword(_ context.Context, e string) {
	f.forgot = append(f.forgot, e)
}
func (f *fakeMini) MiniAutoPay(context.Context, int64) MiniAutoPayDTO { return MiniAutoPayDTO{} }
func (f *fakeMini) MiniCheckout(context.Context, int64, string, int, string, string, bool) MiniActionDTO {
	return MiniActionDTO{OK: true}
}
func (f *fakeMini) CabinetAccount(_ context.Context, id int64) CabinetAccountDTO {
	return CabinetAccountDTO{TgID: id}
}
func (f *fakeMini) CabinetBindTelegram(_ context.Context, _, newID int64) (int64, error) {
	f.bound = newID
	return newID, nil
}
func (f *fakeMini) CabinetBindName(context.Context, int64, string, string) {}

func gateServer(t *testing.T, f *fakeMini) http.Handler {
	t.Helper()
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	s.SetMiniApp(f)
	return s.wrap(s.mux())
}

func gateReq(method, path, body, token, ip string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.RemoteAddr = ip + ":1234"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

func webToken(f *fakeMini, id int64, epoch int) string {
	return issueJWT(id, true, jwtKey(fakeBotToken), time.Hour, f.SessionVersion(), epoch)
}

// Гейт подтверждения закрывает ЗАПИСЬ и не трогает чтение: экран, который и
// объясняет человеку, почему оплата недоступна, обязан открываться.
func TestMoneyGateBlocksWritesOnly(t *testing.T) {
	f := &fakeMini{blocked: true}
	h := gateServer(t, f)
	tok := webToken(f, -7, 0)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/miniapp/checkout", `{"months":1}`, tok, "203.0.113.10"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("покупка без подтверждения почты обязана быть закрыта: %d", w.Code)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodGet, "/api/miniapp/autopay", "", tok, "203.0.113.11"))
	if w.Code != http.StatusOK {
		t.Fatalf("чтение состояния закрывать нельзя: %d", w.Code)
	}

	// Подтверждённому та же ручка открыта.
	f.blocked = false
	w = httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/miniapp/checkout", `{"months":1}`, tok, "203.0.113.12"))
	if w.Code != http.StatusOK {
		t.Fatalf("подтверждённому покупка обязана быть открыта: %d", w.Code)
	}
}

// Поколение пропусков аккаунта: смена пароля и привязка Telegram обязаны
// выбивать прежние сессии немедленно, не трогая посторонних.
func TestSessionEpochRevokesToken(t *testing.T) {
	f := &fakeMini{}
	h := gateServer(t, f)
	tok := webToken(f, -7, 0)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodGet, "/api/cabinet/account", "", tok, "203.0.113.20"))
	if w.Code != http.StatusOK {
		t.Fatalf("свой пропуск обязан приниматься: %d", w.Code)
	}

	f.epoch = 1 // сменили пароль
	w = httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodGet, "/api/cabinet/account", "", tok, "203.0.113.21"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("прежний пропуск обязан быть отвергнут: %d", w.Code)
	}

	// Свежий пропуск нового поколения снова работает.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodGet, "/api/cabinet/account", "", webToken(f, -7, 1), "203.0.113.22"))
	if w.Code != http.StatusOK {
		t.Fatalf("пропуск нового поколения: %d", w.Code)
	}
}

// «Забыли пароль» отвечает одинаково всегда — иначе форма становится
// проверялкой «есть ли у вас аккаунт с таким адресом».
func TestForgotPasswordUniformAnswer(t *testing.T) {
	f := &fakeMini{}
	h := gateServer(t, f)
	for i, email := range []string{"nobody@example.com", "known@example.com", "мусор"} {
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"email": email})
		h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/password/forgot", string(body), "", "203.0.113.30"))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatalf("ответ %d обязан быть одинаковым: код %d тело %s", i, w.Code, w.Body.String())
		}
	}
	if len(f.forgot) != 3 {
		t.Fatalf("адреса обязаны доходить до обработчика: %v", f.forgot)
	}
}

// Ручки аккаунта — только для веб-кабинета: пропуск мини-аппа к ним не подходит.
func TestAccountEndpointsRejectMiniAppToken(t *testing.T) {
	f := &fakeMini{}
	h := gateServer(t, f)
	mini := issueJWT(555, false, jwtKey(fakeBotToken), time.Hour, 0, 0)
	for _, p := range []string{"/api/cabinet/account", "/api/cabinet/tg/bind", "/api/cabinet/password/change"} {
		method := http.MethodPost
		body := "{}"
		if p == "/api/cabinet/account" {
			method, body = http.MethodGet, ""
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, gateReq(method, p, body, mini, "203.0.113.40"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s с пропуском мини-аппа: %d", p, w.Code)
		}
	}
}

// Привязка принимает только подписанные виджетом данные: без верной подписи
// любой залогиненный человек забирал бы себе чужой Telegram.
func TestBindRequiresValidSignature(t *testing.T) {
	f := &fakeMini{}
	h := gateServer(t, f)
	tok := webToken(f, -7, 0)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/tg/bind",
		`{"id":900,"auth_date":1,"hash":"deadbeef"}`, tok, "203.0.113.50"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("подделанная подпись обязана быть отвергнута: %d", w.Code)
	}
	if f.bound != 0 {
		t.Fatal("привязка не имела права выполниться")
	}

	now := time.Now().Unix()
	fields := map[string]string{
		"id":        "900",
		"auth_date": strconv.FormatInt(now, 10),
	}
	fields["hash"] = signLogin(fields, fakeBotToken)
	body, _ := json.Marshal(map[string]any{"id": 900, "auth_date": now, "hash": fields["hash"]})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/cabinet/tg/bind", string(body), tok, "203.0.113.51"))
	if w.Code != http.StatusOK {
		t.Fatalf("верная подпись: %d %s", w.Code, w.Body.String())
	}
	if f.bound != 900 {
		t.Fatalf("привязка не дошла до приложения: %d", f.bound)
	}
}
