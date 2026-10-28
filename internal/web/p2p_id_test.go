package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Идентификатор заявки на перевод — наносекунды, больше 2^53: числом JSON
// браузер его округляет и отменяет или подтверждает чужую (несуществующую)
// заявку. Наружу он обязан уходить строкой и приниматься обратно без потерь.
type p2pIDMini struct {
	fakeMini
	reqID     int64
	cancelled []int64
}

func (f *p2pIDMini) MiniCheckout(context.Context, int64, string, int, string, string, bool, bool) MiniActionDTO {
	return MiniActionDTO{OK: true, P2PCard: "2200 0000 0000 0001", P2PAmount: "500 ₽", P2PReqID: f.reqID}
}

func (f *p2pIDMini) MiniP2PCancel(_ context.Context, _, reqID int64) MiniActionDTO {
	f.cancelled = append(f.cancelled, reqID)
	return MiniActionDTO{OK: true}
}

func TestP2PRequestIDSurvivesJSON(t *testing.T) {
	const id = int64(9007199254740993) // 2^53 + 1 — в double не представим
	f := &p2pIDMini{reqID: id}
	s := newServer(nil, nil)
	s.allowPlainHTTP = true
	s.SetMiniApp(f)
	h := s.wrap(s.mux())
	tok := webToken(&f.fakeMini, -7, 0)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, gateReq(http.MethodPost, "/api/miniapp/checkout", `{"months":3,"method":"p2p","p2p_inapp":true}`, tok, "203.0.113.40"))
	if w.Code != http.StatusOK {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"p2p_req_id":"9007199254740993"`) {
		t.Fatalf("id заявки обязан уходить строкой: %s", w.Body.String())
	}
	var dto struct {
		ReqID string `json:"p2p_req_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil || dto.ReqID != "9007199254740993" {
		t.Fatalf("разбор ответа: %v %q", err, dto.ReqID)
	}

	// Строкой — как шлёт обновлённая страница; числом — как слала старая
	// (число в пределах точности double принимается как есть).
	for _, body := range []string{`{"req_id":"9007199254740993"}`, `{"req_id":4503599627370495}`} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, gateReq(http.MethodPost, "/api/miniapp/p2p/cancel", body, tok, "203.0.113.41"))
		if w.Code != http.StatusOK {
			t.Fatalf("cancel %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(f.cancelled) != 2 || f.cancelled[0] != id || f.cancelled[1] != 4503599627370495 {
		t.Fatalf("id дошли с искажением: %v", f.cancelled)
	}

	for i, body := range []string{`{"req_id":""}`, `{"req_id":"abc"}`, `{"req_id":0}`, `{}`, `{"req_id":"+5"}`, `{"req_id":"-5"}`, `{"req_id":-5}`} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, gateReq(http.MethodPost, "/api/miniapp/p2p/cancel", body, tok, fmt.Sprintf("203.0.113.%d", 50+i)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("cancel %s: ожидался 400, получен %d", body, w.Code)
		}
	}
}

// Пустой id в ответе не должен превращаться в "0": поле опускается, как раньше.
func TestP2PRequestIDOmittedWhenZero(t *testing.T) {
	b, err := json.Marshal(MiniActionDTO{OK: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "p2p_req_id") {
		t.Fatalf("нулевой id обязан опускаться: %s", b)
	}
}
