package remnawave

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
)

func pluginClient(srv *httptest.Server) *Client {
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func TestUnblockIP_SendsExecutorCommand(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/node-plugins/executor" || r.Method != http.MethodPost {
			t.Errorf("неожиданный запрос: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	if err := pluginClient(srv).UnblockIP(context.Background(), "203.0.113.7"); err != nil {
		t.Fatalf("UnblockIP: %v", err)
	}
	cmd, _ := got["command"].(map[string]any)
	if cmd["command"] != "unblockIps" {
		t.Fatalf("не та команда: %+v", got)
	}
	ips, _ := cmd["ips"].([]any)
	if len(ips) != 1 || ips[0] != "203.0.113.7" {
		t.Fatalf("не тот адрес: %+v", cmd)
	}
	tgt, _ := got["targetNodes"].(map[string]any)
	if tgt["target"] != "allNodes" {
		t.Fatalf("ожидались все ноды: %+v", got)
	}
}

// Мусор вместо адреса до панели не доходит: панель ответила бы 400, а админ
// увидел бы невнятную ошибку.
func TestUnblockIP_RejectsBadAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("запрос не должен был уйти: %s", r.URL.Path)
	}))
	defer srv.Close()

	if err := pluginClient(srv).UnblockIP(context.Background(), "не-адрес"); err == nil {
		t.Fatalf("ожидалась ошибка на некорректный IP")
	}
}

// Панели до 2.7.0 отвечают 404 — ошибка должна быть понятной, а не паникой.
func TestUnblockIP_OldPanel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Cannot POST /api/node-plugins/executor"}`))
	}))
	defer srv.Close()

	if err := pluginClient(srv).UnblockIP(context.Background(), "203.0.113.7"); err == nil {
		t.Fatalf("ожидалась ошибка на 404")
	}
}

const pluginListBody = `{"response":{"total":2,"nodePlugins":[
  {"uuid":"p-1","name":"main","pluginConfig":{
    "torrentBlocker":{"enabled":true,"blockDuration":3600,"ignoreLists":{"ip":["10.0.0.1"],"userId":[7]}},
    "ingressFilter":{"enabled":true,"blockedIps":["192.0.2.0/24"]},
    "sharedLists":[{"name":"ext:big","type":"asList","items":[4294967295]}]}},
  {"uuid":"p-2","name":"noblocker","pluginConfig":{"egressFilter":{"enabled":true,"blockedPorts":[25]}}}
]}}`

func TestTorrentIgnoreUser_PatchesOnlyBlockerConfigs(t *testing.T) {
	var patched []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/node-plugins", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(pluginListBody))
		case http.MethodPatch:
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			patched = append(patched, m)
			// Панель валидирует конфиг zod-схемой: числа не должны прийти в
			// экспоненциальной записи, иначе конфиг будет отвергнут.
			if strings.Contains(string(body), "e+") {
				t.Errorf("число уехало в экспоненциальную запись: %s", body)
			}
			_, _ = w.Write([]byte(`{"response":{"uuid":"p-1","name":"main","viewPosition":1}}`))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	changed, already, err := pluginClient(srv).TorrentIgnoreUser(context.Background(), 42)
	if err != nil {
		t.Fatalf("TorrentIgnoreUser: %v", err)
	}
	if changed != 1 || already {
		t.Fatalf("changed=%d already=%v — ожидался ровно один изменённый конфиг", changed, already)
	}
	if len(patched) != 1 || patched[0]["uuid"] != "p-1" {
		t.Fatalf("PATCH ушёл не туда: %+v", patched)
	}

	cfg, _ := patched[0]["pluginConfig"].(map[string]any)
	tb, _ := cfg["torrentBlocker"].(map[string]any)
	lists, _ := tb["ignoreLists"].(map[string]any)
	ids, _ := lists["userId"].([]any)
	if len(ids) != 2 || ids[0].(float64) != 7 || ids[1].(float64) != 42 {
		t.Fatalf("список исключений собран неверно: %+v", ids)
	}
	// Соседний список исключений — по адресам — терять нельзя: именно он
	// пропадал бы при «починке» неожиданного типа подстановкой пустой карты.
	ips, _ := lists["ip"].([]any)
	if len(ips) != 1 || ips[0] != "10.0.0.1" {
		t.Fatalf("ignoreLists.ip потерян: %+v", lists)
	}
	// Чужие поля конфига обязаны пережить правку без потерь.
	if _, ok := cfg["ingressFilter"]; !ok {
		t.Fatalf("ingressFilter потерян: %+v", cfg)
	}
	if _, ok := tb["blockDuration"]; !ok {
		t.Fatalf("blockDuration потерян: %+v", tb)
	}
	sl, _ := cfg["sharedLists"].([]any)
	if len(sl) != 1 {
		t.Fatalf("sharedLists потерян: %+v", cfg)
	}
	items, _ := sl[0].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(float64) != 4294967295 {
		t.Fatalf("большое число в чужом поле испорчено: %+v", items)
	}
}

func TestTorrentIgnoreUser_AlreadyThere(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/node-plugins", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			t.Errorf("повторный PATCH не нужен")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pluginListBody))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	changed, already, err := pluginClient(srv).TorrentIgnoreUser(context.Background(), 7)
	if err != nil || changed != 0 || !already {
		t.Fatalf("changed=%d already=%v err=%v — ожидалось «уже в списке»", changed, already, err)
	}
}

// Ни одного конфига с torrentBlocker — это не ошибка, но и не «добавлено».
func TestTorrentIgnoreUser_NoBlockerConfig(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/node-plugins", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"total":1,"nodePlugins":[{"uuid":"p-2","name":"x","pluginConfig":null}]}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	changed, already, err := pluginClient(srv).TorrentIgnoreUser(context.Background(), 42)
	if err != nil || changed != 0 || already {
		t.Fatalf("changed=%d already=%v err=%v — ожидался пустой результат без ошибки", changed, already, err)
	}
}

// Неожиданный тип в чужом конфиге — это отказ, а не молчаливая перезапись:
// подставив пустой объект, бот стёр бы оператору весь список исключений.
func TestTorrentIgnoreUser_RefusesOnUnexpectedShape(t *testing.T) {
	for _, tc := range []struct{ name, cfg string }{
		{"ignoreLists не объект", `{"torrentBlocker":{"enabled":true,"ignoreLists":"ext:all"}}`},
		{"userId не список", `{"torrentBlocker":{"enabled":true,"ignoreLists":{"userId":42}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/api/node-plugins", func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					t.Errorf("конфиг неожиданной формы править нельзя")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"response":{"total":1,"nodePlugins":[{"uuid":"p-1","name":"main","pluginConfig":` + tc.cfg + `}]}}`))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			if _, _, err := pluginClient(srv).TorrentIgnoreUser(context.Background(), 42); err == nil {
				t.Fatalf("ожидалась ошибка вместо молчаливой перезаписи")
			}
		})
	}
}
