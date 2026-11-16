package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
	"testing"
)

// currentSchemaBody повторяет форму конфига, которую отдаёт актуальная страница
// подписки: displayName у платформы — локализованный объект, а не строка.
const currentSchemaBody = `{
  "version":"1","locales":["en","ru"],
  "brandingSettings":{"title":"Sub","logoUrl":"","supportUrl":"https://example.com/support"},
  "uiConfig":{"subscriptionInfoBlockType":"cards","installationGuidesBlockType":"cards"},
  "svgLibrary":{},
  "platforms":{
    "ios":{"displayName":{"en":"iOS","ru":"Айфон"},"svgIconKey":"apple","apps":[
      {"name":"App One","featured":true,"svgIconKey":"one","blocks":[
        {"svgIconKey":"d","svgIconColor":"#fff","title":{"en":"Install"},"description":{"en":"block-store"},
         "buttons":[{"link":"https://example.com/store/app-one","type":"external","text":{"en":"store-btn"},"svgIconKey":"s"}]},
        {"svgIconKey":"a","svgIconColor":"#fff","title":{"en":"Add"},"description":{"en":"block-add"},
         "buttons":[{"link":"appone://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"en":"add-btn"},"svgIconKey":"a"}]}
      ]}
    ]},
    "androidTV":{"displayName":{"en":"Android TV"},"svgIconKey":"tv","apps":[
      {"name":"App Two","featured":false,"blocks":[
        {"svgIconKey":"a","svgIconColor":"#000","title":{"en":"Add"},"description":{"en":"d"},
         "buttons":[{"link":"apptwo://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"en":"add-btn"},"svgIconKey":"a"}]}
      ]}
    ]},
    "android":{"displayName":{"en":"Android"},"svgIconKey":"droid","apps":[
      {"name":"App Three","featured":false,"blocks":[
        {"svgIconKey":"a","svgIconColor":"#000","title":{"en":"Add"},"description":{"en":"d"},
         "buttons":[{"link":"appthree://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"en":"add-btn"},"svgIconKey":"a"}]}
      ]}
    ]}
  }
}`

// TestFlexDisplayName: локализованный объект и строка в displayName обязаны
// разбираться одинаково. Строковый вариант писали старые страницы подписки,
// и раньше только он и поддерживался — из-за чего актуальный конфиг ронял
// разбор целиком и приложения не находились.
func TestFlexDisplayName(t *testing.T) {
	var loc appConfigV2
	if err := json.Unmarshal([]byte(currentSchemaBody), &loc); err != nil {
		t.Fatalf("локализованный displayName: %v", err)
	}
	if got := len(loc.Platforms["ios"].Apps); got != 1 {
		t.Fatalf("ios apps = %d, want 1", got)
	}
	if got := loc.Platforms["ios"].DisplayName.pick("ru"); got != "Айфон" {
		t.Errorf("pick(ru) = %q, want Айфон", got)
	}
	if got := loc.Platforms["ios"].DisplayName.pick("fr"); got != "iOS" {
		t.Errorf("pick(fr) = %q, want iOS (фолбэк на en)", got)
	}

	var plain appConfigV2
	if err := json.Unmarshal([]byte(`{"platforms":{"ios":{"displayName":"iPhone","apps":[]}}}`), &plain); err != nil {
		t.Fatalf("строковый displayName: %v", err)
	}
	if got := plain.Platforms["ios"].DisplayName.pick("ru"); got != "iPhone" {
		t.Errorf("pick строки = %q, want iPhone", got)
	}

	// Мусор в поле не должен ронять весь конфиг: имя косметическое.
	var odd appConfigV2
	if err := json.Unmarshal([]byte(`{"platforms":{"ios":{"displayName":42,"apps":[]}}}`), &odd); err != nil {
		t.Fatalf("число в displayName уронило разбор: %v", err)
	}
	if got := odd.Platforms["ios"].DisplayName.pick("ru"); got != "" {
		t.Errorf("pick мусора = %q, want пусто", got)
	}
}

func TestOrderedPlatformKeysIgnoresCase(t *testing.T) {
	got := orderedPlatformKeys(map[string]bool{"androidTV": true, "ios": true, "android": true, "zzz": true})
	want := []string{"android", "ios", "androidTV", "zzz"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestFetchAppConfigCurrentSchema гоняет актуальный конфиг через тот же путь,
// которым ходит бот: страница подписки для cookie, затем известные пути.
func TestFetchAppConfigCurrentSchema(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/assets/.app-config-v2.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, currentSchemaBody)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html></html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ce := a.fetchAppConfig(context.Background(), srv.URL, srv.URL+"/AbCd")
	if ce == nil || ce.v2 == nil {
		t.Fatal("конфиг не распознан")
	}
	sub := "https://sub.example.com/AbCd"
	pl := buildV2Platforms(ce.v2, sub, "tg_99", "ru")
	if len(pl) != 3 {
		t.Fatalf("платформ = %d, want 3", len(pl))
	}
	if pl[0].Key != "android" || pl[1].Key != "ios" || pl[2].Key != "androidTV" {
		t.Fatalf("порядок платформ = %v/%v/%v", pl[0].Key, pl[1].Key, pl[2].Key)
	}
	if pl[1].Label != "Айфон" {
		t.Errorf("label ios = %q, want Айфон", pl[1].Label)
	}
	if len(pl[1].Apps) != 1 || pl[1].Apps[0].Deeplink != "appone://add/"+sub {
		t.Errorf("ios apps = %+v", pl[1].Apps)
	}
	if len(pl[1].Apps[0].Installs) != 1 {
		t.Errorf("installs = %+v", pl[1].Apps[0].Installs)
	}
}

func TestShortUUIDFromSub(t *testing.T) {
	cases := map[string]string{
		"https://sub.example.com/AbCd":      "AbCd",
		"https://sub.example.com/sub/AbCd/": "AbCd",
		"https://sub.example.com/AbCd?x=1":  "AbCd",
		"https://sub.example.com":           "",
		"":                                  "",
	}
	for in, want := range cases {
		if got := shortUUIDFromSub(in); got != want {
			t.Errorf("shortUUIDFromSub(%q) = %q, want %q", in, got, want)
		}
	}
}

// Панель 3.x отдаёт конфиг приложений сама — на страницу подписки ходить не
// надо. Ходов на страницу в этом тесте быть не должно вовсе.
func TestFetchPanelAppConfig(t *testing.T) {
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/subscription-page-configs" {
			_, _ = io.WriteString(w, `{"response":{"total":1,"configs":[{"uuid":"a","name":"Default","viewPosition":1,"config":`+currentSchemaBody+`}]}}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer panelSrv.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	panel := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelSrv.URL, APIToken: "t"})
	ce := a.fetchPanelAppConfig(context.Background(), panel, "sh0rt", "https://sub.example.com/sh0rt")
	if ce == nil || ce.v2 == nil {
		t.Fatal("конфиг из панели не получен")
	}
	if got := v2AppCount(ce.v2); got != 3 {
		t.Fatalf("apps = %d, want 3", got)
	}
}

// Панель без такого API отвечает 404 — бот молча уходит на страницу подписки
// и какое-то время панель об этом больше не спрашивает.
func TestFetchPanelAppConfigNoAPI(t *testing.T) {
	var hits int
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer panelSrv.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	panel := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelSrv.URL, APIToken: "t"})
	for i := 0; i < 3; i++ {
		if ce := a.fetchPanelAppConfig(context.Background(), panel, "sh0rt", ""); ce != nil {
			t.Fatal("ожидался отказ")
		}
	}
	if hits != 1 {
		t.Fatalf("запросов к панели = %d, want 1", hits)
	}
}

// Панель может держать несколько конфигов и назначать разным подпискам разные.
// Кэш обязан различать их: иначе человек увидит чужой набор приложений.
func TestFetchPanelAppConfigPerSubscription(t *testing.T) {
	cfgA := strings.Replace(currentSchemaBody, `"name":"App One"`, `"name":"OnlyA"`, 1)
	cfgB := strings.Replace(currentSchemaBody, `"name":"App One"`, `"name":"OnlyB"`, 1)
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/subscription-page-configs":
			_, _ = io.WriteString(w, `{"response":{"total":2,"configs":[`+
				`{"uuid":"a","name":"A","viewPosition":1,"config":`+cfgA+`},`+
				`{"uuid":"b","name":"B","viewPosition":2,"config":`+cfgB+`}]}}`)
		case strings.HasPrefix(r.URL.Path, "/api/subscriptions/subpage-config/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/subpage-config/")
			_, _ = io.WriteString(w, `{"response":{"subpageConfigUuid":"`+id+`","webpageAllowed":true}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer panelSrv.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	panel := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelSrv.URL, APIToken: "t"})
	iosName := func(short string) string {
		ce := a.fetchPanelAppConfig(context.Background(), panel, short, "")
		if ce == nil || ce.v2 == nil {
			t.Fatalf("%s: конфиг не получен", short)
		}
		return ce.v2.Platforms["ios"].Apps[0].Name
	}
	if got := iosName("a"); got != "OnlyA" {
		t.Fatalf("подписка a получила %q", got)
	}
	if got := iosName("b"); got != "OnlyB" {
		t.Fatalf("подписка b получила чужой конфиг: %q", got)
	}
}

// panelByUUID — панель, какая она на деле: в списке у конфигов содержимого
// нет, оно отдаётся запросом по uuid. get решает, чем ответить на такой запрос.
func panelByUUID(t *testing.T, uuids []string, get func(w http.ResponseWriter, uuid string)) (*remnawave.Client, *int, *int) {
	t.Helper()
	var lists, gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/subscription-page-configs":
			lists++
			items := make([]string, 0, len(uuids))
			for i, u := range uuids {
				items = append(items, fmt.Sprintf(`{"uuid":%q,"name":%q,"viewPosition":%d,"tags":[],"config":null}`, u, u, i+1))
			}
			_, _ = fmt.Fprintf(w, `{"response":{"total":%d,"configs":[%s]}}`, len(uuids), strings.Join(items, ","))
		case strings.HasPrefix(r.URL.Path, "/api/subscription-page-configs/"):
			gets++
			get(w, strings.TrimPrefix(r.URL.Path, "/api/subscription-page-configs/"))
		case strings.HasPrefix(r.URL.Path, "/api/subscriptions/subpage-config/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/subscriptions/subpage-config/")
			_, _ = io.WriteString(w, `{"response":{"subpageConfigUuid":"`+id+`","webpageAllowed":true}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"}), &lists, &gets
}

func panelCfgBody(uuid, iosApp string) string {
	return `{"response":{"uuid":"` + uuid + `","name":"` + uuid + `","viewPosition":1,"tags":[],"config":` +
		strings.Replace(currentSchemaBody, `"name":"App One"`, `"name":"`+iosApp+`"`, 1) + `}}`
}

// Список приходит без содержимого — бот добирает его по uuid и дальше живёт
// на кэше, не дёргая панель на каждый заход.
func TestFetchPanelAppConfigBodyByUUID(t *testing.T) {
	panel, lists, gets := panelByUUID(t, []string{"a", "b"}, func(w http.ResponseWriter, uuid string) {
		_, _ = io.WriteString(w, panelCfgBody(uuid, "Only-"+uuid))
	})
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, short := range []string{"a", "b", "a"} {
		ce := a.fetchPanelAppConfig(context.Background(), panel, short, "")
		if ce == nil || ce.v2 == nil {
			t.Fatalf("%s: конфиг из панели не получен", short)
		}
		if got := ce.v2.Platforms["ios"].Apps[0].Name; got != "Only-"+short {
			t.Fatalf("подписка %s получила %q", short, got)
		}
	}
	if *lists != 1 || *gets != 2 {
		t.Fatalf("запросов: список %d, по uuid %d; want 1 и 2", *lists, *gets)
	}
}

// Подписке ничего не назначено — ей достаётся стандартный конфиг панели, даже
// если в списке он стоит не первым.
func TestFetchPanelAppConfigUnassignedGetsDefault(t *testing.T) {
	var lists int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/subscription-page-configs":
			lists++
			_, _ = fmt.Fprintf(w, `{"response":{"total":2,"configs":[`+
				`{"uuid":"custom","name":"Custom","viewPosition":1,"config":null},`+
				`{"uuid":%q,"name":"Default","viewPosition":2,"config":null}]}}`, remnawave.SubpageDefaultUUID)
		case strings.HasPrefix(r.URL.Path, "/api/subscription-page-configs/"):
			uuid := strings.TrimPrefix(r.URL.Path, "/api/subscription-page-configs/")
			_, _ = io.WriteString(w, panelCfgBody(uuid, "Only-"+uuid))
		case r.URL.Path == "/api/subscriptions/subpage-config/ghost":
			_, _ = io.WriteString(w, `{"response":{"subpageConfigUuid":"ghost","webpageAllowed":true}}`)
		case r.URL.Path == "/api/subscriptions/subpage-config/assigned":
			_, _ = io.WriteString(w, `{"response":{"subpageConfigUuid":"custom","webpageAllowed":true}}`)
		case strings.HasPrefix(r.URL.Path, "/api/subscriptions/subpage-config/"):
			_, _ = io.WriteString(w, `{"response":{"subpageConfigUuid":null,"webpageAllowed":true}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	panel := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// ghost — назначен конфиг, которого в наборе нет (в нём не оказалось приложений).
	def := "Only-" + remnawave.SubpageDefaultUUID
	for short, want := range map[string]string{"plain": def, "assigned": "Only-custom", "ghost": def} {
		ce := a.fetchPanelAppConfig(context.Background(), panel, short, "")
		if ce == nil || ce.v2 == nil {
			t.Fatalf("%s: конфиг не получен", short)
		}
		if got := ce.v2.Platforms["ios"].Apps[0].Name; got != want {
			t.Fatalf("подписка %s получила %q, want %q", short, got, want)
		}
	}
}

// У токена нет права на содержимое: бот уходит на страницу подписки и на
// время перестаёт спрашивать панель.
func TestFetchPanelAppConfigNoGetScope(t *testing.T) {
	panel, lists, gets := panelByUUID(t, []string{"a", "b"}, func(w http.ResponseWriter, uuid string) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Forbidden","statusCode":403}`)
	})
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for i := 0; i < 3; i++ {
		if ce := a.fetchPanelAppConfig(context.Background(), panel, "a", ""); ce != nil {
			t.Fatal("ожидался отказ")
		}
	}
	if *lists != 1 || *gets != 1 {
		t.Fatalf("запросов: список %d, по uuid %d; want 1 и 1", *lists, *gets)
	}
}

// Сменили адрес или токен панели — клиент другой. Пауза и набор, оставшиеся
// от прежнего, на него не действуют: новый токен срабатывает сразу.
func TestFetchPanelAppConfigNewClientStartsClean(t *testing.T) {
	oldPanel, _, _ := panelByUUID(t, []string{"a"}, func(w http.ResponseWriter, uuid string) {
		w.WriteHeader(http.StatusForbidden)
	})
	newPanel, lists, _ := panelByUUID(t, []string{"a"}, func(w http.ResponseWriter, uuid string) {
		_, _ = io.WriteString(w, panelCfgBody(uuid, "Only-new"))
	})
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if ce := a.fetchPanelAppConfig(context.Background(), oldPanel, "a", ""); ce != nil {
		t.Fatal("прежний токен: ожидался отказ")
	}
	ce := a.fetchPanelAppConfig(context.Background(), newPanel, "a", "")
	if ce == nil || ce.v2 == nil || ce.v2.Platforms["ios"].Apps[0].Name != "Only-new" {
		t.Fatalf("новый клиент упёрся в паузу прежнего: %+v", ce)
	}
	// Запрос, начатый ещё с прежним клиентом, закончился отказом уже после
	// смены: новому клиенту это не мешает, и его набор не подменяется.
	if ce := a.fetchPanelAppConfig(context.Background(), oldPanel, "a", ""); ce != nil {
		t.Fatal("прежний клиент получил чужой набор")
	}
	a.pausePanelCfgs(oldPanel, time.Hour)
	if ce := a.fetchPanelAppConfig(context.Background(), newPanel, "a", ""); ce == nil {
		t.Fatal("отказ прежнему клиенту поставил паузу новому")
	}
	if *lists != 1 {
		t.Fatalf("новый клиент ходил за списком %d раз, want 1", *lists)
	}
}

// Один конфиг не прочитался — не отдаём остальные: иначе человек с этим
// конфигом увидел бы чужой набор приложений.
func TestFetchPanelAppConfigAllOrNothing(t *testing.T) {
	panel, _, _ := panelByUUID(t, []string{"a", "b"}, func(w http.ResponseWriter, uuid string) {
		if uuid == "b" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, panelCfgBody(uuid, "Only-"+uuid))
	})
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, short := range []string{"a", "b"} {
		if ce := a.fetchPanelAppConfig(context.Background(), panel, short, ""); ce != nil {
			t.Fatalf("%s: получен неполный набор", short)
		}
	}
}

// Конфиг удалили между списком и запросом по uuid — остальные остаются.
func TestFetchPanelAppConfigDeletedMeanwhile(t *testing.T) {
	panel, _, _ := panelByUUID(t, []string{"gone", "b"}, func(w http.ResponseWriter, uuid string) {
		if uuid == "gone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, panelCfgBody(uuid, "Only-"+uuid))
	})
	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ce := a.fetchPanelAppConfig(context.Background(), panel, "b", "")
	if ce == nil || ce.v2 == nil || ce.v2.Platforms["ios"].Apps[0].Name != "Only-b" {
		t.Fatalf("оставшийся конфиг не получен: %+v", ce)
	}
}

// Человек закрыл мини-апп — запрос отменился. Это не повод на десять минут
// отключать панельный источник для всех остальных.
func TestFetchPanelAppConfigCancelledRequest(t *testing.T) {
	var hits int
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"response":{"total":1,"configs":[{"uuid":"a","name":"A","viewPosition":1,"config":`+currentSchemaBody+`}]}}`)
	}))
	defer panelSrv.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	panel := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelSrv.URL, APIToken: "t"})
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if ce := a.fetchPanelAppConfig(dead, panel, "sh0rt", ""); ce != nil {
		t.Fatal("отменённый запрос не должен давать конфиг")
	}
	if ce := a.fetchPanelAppConfig(context.Background(), panel, "sh0rt", ""); ce == nil {
		t.Fatal("следующий пользователь должен получить конфиг, а не паузу")
	}
	if hits == 0 {
		t.Fatal("панель не опрошена")
	}
}

// Панель API отдала, но конфигов нет — не долбим её на каждом заходе.
func TestFetchPanelAppConfigEmptyListPauses(t *testing.T) {
	var hits int
	panelSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, `{"response":{"total":0,"configs":[]}}`)
	}))
	defer panelSrv.Close()

	a := &App{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	panel := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panelSrv.URL, APIToken: "t"})
	for i := 0; i < 3; i++ {
		if ce := a.fetchPanelAppConfig(context.Background(), panel, "sh0rt", ""); ce != nil {
			t.Fatal("ожидался отказ")
		}
	}
	if hits != 1 {
		t.Fatalf("запросов к панели = %d, want 1", hits)
	}
}

// Битая локализация в имени платформы косметическая: приложения обязаны
// остаться. Именно из-за жёсткого типа тут терялся весь конфиг.
func TestFlexDisplayNameBrokenLocalization(t *testing.T) {
	var c appConfigV2
	body := `{"platforms":{"ios":{"displayName":{"en":1},"apps":[{"name":"A","featured":true,` +
		`"blocks":[{"buttons":[{"link":"a://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"en":"add"}}]}]}]}}}`
	if err := json.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("разбор упал: %v", err)
	}
	if len(c.Platforms["ios"].Apps) != 1 {
		t.Fatalf("приложения потеряны: %+v", c.Platforms)
	}
}
