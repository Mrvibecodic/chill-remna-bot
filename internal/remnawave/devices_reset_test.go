package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"remnabot/internal/model"
)

// resetOpts configures the fake panel used by the device-reset tests and records
// how many times each endpoint was hit.
type resetOpts struct {
	mu sync.Mutex

	uuid         string
	reportTG     int  // telegramId the by-telegram-id endpoint reports (owns => 42)
	preTotal     int  // HWID pre-count
	revokeStatus int  // status for /actions/revoke (default 200)
	deleteFailN  int  // fail the first N delete-all calls, then succeed
	deleteAlways bool // always fail delete-all
	userNotFound bool // by-telegram-id returns an empty list

	revokeCalls int
	deleteCalls int
	countCalls  int
}

func (o *resetOpts) bumpDelete() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.deleteCalls++
	return o.deleteCalls
}
func (o *resetOpts) bumpRevoke() { o.mu.Lock(); o.revokeCalls++; o.mu.Unlock() }
func (o *resetOpts) bumpCount()  { o.mu.Lock(); o.countCalls++; o.mu.Unlock() }
func (o *resetOpts) counts() (rev, del, cnt int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.revokeCalls, o.deleteCalls, o.countCalls
}

func newResetServer(t *testing.T, opt *resetOpts) *httptest.Server {
	t.Helper()
	if opt.uuid == "" {
		opt.uuid = "u-1"
	}
	if opt.revokeStatus == 0 {
		opt.revokeStatus = http.StatusOK
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if opt.userNotFound {
			w.Write([]byte(`{"response":[]}`))
			return
		}
		w.Write([]byte(`{"response":[{"uuid":"` + opt.uuid + `","telegramId":` + itoa(opt.reportTG) + `,"status":"ACTIVE","hwidDeviceLimit":3}]}`))
	})
	mux.HandleFunc("/api/hwid/devices/"+opt.uuid, func(w http.ResponseWriter, r *http.Request) {
		opt.bumpCount()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":` + itoa(opt.preTotal) + `,"devices":[]}}`))
	})
	mux.HandleFunc("/api/users/"+opt.uuid+"/actions/revoke", func(w http.ResponseWriter, r *http.Request) {
		opt.bumpRevoke()
		w.WriteHeader(opt.revokeStatus)
	})
	mux.HandleFunc("/api/hwid/devices/delete-all", func(w http.ResponseWriter, r *http.Request) {
		n := opt.bumpDelete()
		if opt.deleteAlways || n <= opt.deleteFailN {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

// fastResetClient points a client at base with tiny retry backoff so the
// synchronous HWID retries run instantly in tests.
func fastResetClient(base string) *Client {
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: base, APIToken: "t"})
	c.hwidRetryBase = time.Millisecond
	c.hwidRetryMax = 2 * time.Millisecond
	return c
}

func TestResetDevices_Success(t *testing.T) {
	opt := &resetOpts{reportTG: 42, preTotal: 2}
	srv := newResetServer(t, opt)
	defer srv.Close()
	res, found, err := fastResetClient(srv.URL).ResetDevicesByTelegramID(context.Background(), 42)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !res.KeysRotated || !res.HwidCleared || res.HwidErr != nil {
		t.Fatalf("res=%+v", res)
	}
	if res.Removed != 2 {
		t.Fatalf("removed=%d, want 2", res.Removed)
	}
	if res.UUID != "u-1" {
		t.Fatalf("uuid=%q", res.UUID)
	}
	rev, del, _ := opt.counts()
	if rev != 1 || del != 1 {
		t.Fatalf("revoke=%d delete=%d, want 1/1", rev, del)
	}
}

func TestResetDevices_HwidRetrySucceeds(t *testing.T) {
	opt := &resetOpts{reportTG: 42, preTotal: 1, deleteFailN: 2}
	srv := newResetServer(t, opt)
	defer srv.Close()
	res, found, err := fastResetClient(srv.URL).ResetDevicesByTelegramID(context.Background(), 42)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !res.HwidCleared || res.HwidErr != nil {
		t.Fatalf("expected HWID cleared after retries, res=%+v", res)
	}
	if _, del, _ := opt.counts(); del != 3 {
		t.Fatalf("delete attempts=%d, want 3 (2 fail + 1 ok)", del)
	}
}

func TestResetDevices_HwidExhausted(t *testing.T) {
	opt := &resetOpts{reportTG: 42, preTotal: 1, deleteAlways: true}
	srv := newResetServer(t, opt)
	defer srv.Close()
	res, found, err := fastResetClient(srv.URL).ResetDevicesByTelegramID(context.Background(), 42)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !res.KeysRotated {
		t.Fatalf("keys must still be rotated even when HWID fails: %+v", res)
	}
	if res.HwidCleared || res.HwidErr == nil {
		t.Fatalf("expected HwidErr and not-cleared, res=%+v", res)
	}
	if res.UUID != "u-1" {
		t.Fatalf("uuid must be set for background retry, got %q", res.UUID)
	}
	if _, del, _ := opt.counts(); del != hwidSyncAttempts {
		t.Fatalf("delete attempts=%d, want %d", del, hwidSyncAttempts)
	}
}

func TestResetDevices_RevokeFails(t *testing.T) {
	opt := &resetOpts{reportTG: 42, revokeStatus: http.StatusInternalServerError}
	srv := newResetServer(t, opt)
	defer srv.Close()
	res, found, err := fastResetClient(srv.URL).ResetDevicesByTelegramID(context.Background(), 42)
	if !found || err == nil {
		t.Fatalf("expected found+error on revoke failure, found=%v err=%v", found, err)
	}
	if res.KeysRotated {
		t.Fatalf("keys must not be reported rotated when revoke failed")
	}
	if _, del, _ := opt.counts(); del != 0 {
		t.Fatalf("delete-all must not run when revoke failed, got %d", del)
	}
}

func TestResetDevices_NotFound(t *testing.T) {
	opt := &resetOpts{userNotFound: true}
	srv := newResetServer(t, opt)
	defer srv.Close()
	_, found, err := fastResetClient(srv.URL).ResetDevicesByTelegramID(context.Background(), 42)
	if found || err != nil {
		t.Fatalf("expected not-found with no error, found=%v err=%v", found, err)
	}
}

func TestResetDevices_NotOwned(t *testing.T) {
	opt := &resetOpts{reportTG: 99} // different telegramId, username won't match either
	srv := newResetServer(t, opt)
	defer srv.Close()
	_, found, err := fastResetClient(srv.URL).ResetDevicesByTelegramID(context.Background(), 42)
	if found || err == nil {
		t.Fatalf("expected ownership error, found=%v err=%v", found, err)
	}
}

func TestDeleteAllHwidUntil_CtxCancel(t *testing.T) {
	opt := &resetOpts{reportTG: 42, deleteAlways: true}
	srv := newResetServer(t, opt)
	defer srv.Close()
	c := fastResetClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := c.DeleteAllHwidUntil(ctx, "u-1"); err == nil {
		t.Fatal("expected an error once ctx expires while delete-all keeps failing")
	}
}

func TestHwidCount(t *testing.T) {
	srv := deviceServer(t, 3, 4) // total=4
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	if n := c.hwidCount(context.Background(), "u-1"); n != 4 {
		t.Fatalf("count=%d, want 4", n)
	}
	if n := c.hwidCount(context.Background(), "missing"); n != -1 {
		t.Fatalf("count for unknown path=%d, want -1", n)
	}
}

func TestHwidCount_DevicesFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":{"total":0,"devices":[{},{},{}]}}`))
	}))
	defer srv.Close()
	c := New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	if n := c.hwidCount(context.Background(), "anything"); n != 3 {
		t.Fatalf("count=%d, want 3 (devices fallback)", n)
	}
}
