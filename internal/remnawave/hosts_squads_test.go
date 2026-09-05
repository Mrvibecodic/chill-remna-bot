package remnawave

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"remnabot/internal/model"
)

func hostsPanel(t *testing.T, body string) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
}

func set(ids ...string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// Панель 3.4.0 отдаёт доступ хоста объектом internalSquads{mode,squads};
// плоского excludedInternalSquads в ответе больше нет.
func TestHostsSquadsModern(t *testing.T) {
	c := hostsPanel(t, `{"response":[
		{"remark":"h-excl","inbound":{"configProfileInboundUuid":"ib"},
		 "internalSquads":{"mode":"EXCLUDE","squads":["s1"]},"isDisabled":false,"isHidden":false},
		{"remark":"h-allow","inbound":{"configProfileInboundUuid":"ib"},
		 "internalSquads":{"mode":"ALLOW_ONLY","squads":["s2"]},"isDisabled":false,"isHidden":false}
	]}`)
	hosts, err := c.ListHosts(context.Background())
	if err != nil {
		t.Fatalf("список хостов: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("ожидали 2 хоста, получили %d", len(hosts))
	}
	excl, allow := hosts[0], hosts[1]
	if excl.SquadsMode != SquadsModeExclude || len(excl.Squads) != 1 {
		t.Fatalf("режим доступа не разобран: %+v", excl)
	}
	if excl.ServesAny(set("s1")) {
		t.Fatal("исключённый сквад всё равно получает хост")
	}
	if !excl.ServesAny(set("s2")) {
		t.Fatal("не исключённый сквад потерял хост")
	}
	if !allow.ServesAny(set("s2")) {
		t.Fatal("разрешённый сквад не получил хост в режиме ALLOW_ONLY")
	}
	if allow.ServesAny(set("s1")) {
		t.Fatal("режим ALLOW_ONLY отдал хост постороннему скваду")
	}
	if allow.ServesAny(nil) {
		t.Fatal("без сквадов хост не может быть доступен")
	}
}

// Панели до 3.4.0 отдают плоский список исключений — он обязан работать
// по-прежнему.
func TestHostsSquadsLegacy(t *testing.T) {
	c := hostsPanel(t, `{"response":[
		{"remark":"h","inbound":{"configProfileInboundUuid":"ib"},
		 "excludedInternalSquads":["s1"],"isDisabled":false,"isHidden":false}
	]}`)
	hosts, err := c.ListHosts(context.Background())
	if err != nil {
		t.Fatalf("список хостов: %v", err)
	}
	h := hosts[0]
	if h.SquadsMode != "" {
		t.Fatalf("на старой панели режим должен быть пустым, получили %q", h.SquadsMode)
	}
	if h.ServesAny(set("s1")) {
		t.Fatal("исключённый сквад получает хост")
	}
	if !h.ServesAny(set("s2")) {
		t.Fatal("не исключённый сквад потерял хост")
	}
}

// Тариф с несколькими сквадами: хост достаётся подписке, если доступен хотя бы
// через один из них — пользователю выдаются все сквады тарифа сразу.
func TestHostsSquadsUnion(t *testing.T) {
	excl := Host{SquadsMode: SquadsModeExclude, Squads: []string{"s1"}}
	if !excl.ServesAny(set("s1", "s2")) {
		t.Fatal("хост потерян, хотя второй сквад тарифа не исключён")
	}
	if excl.ServesAny(set("s1")) {
		t.Fatal("единственный исключённый сквад всё же получил хост")
	}
	allow := Host{SquadsMode: SquadsModeAllowOnly, Squads: []string{"s2"}}
	if !allow.ServesAny(set("s1", "s2")) {
		t.Fatal("хост потерян, хотя один из сквадов тарифа разрешён")
	}
	legacy := Host{ExcludedSquads: []string{"s1"}}
	if !legacy.ServesAny(set("s1", "s2")) {
		t.Fatal("старый формат: хост потерян при частичном исключении")
	}
}
