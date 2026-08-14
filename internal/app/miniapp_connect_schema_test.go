package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
