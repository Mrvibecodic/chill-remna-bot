package app

import "testing"

func TestBuildDeeplink(t *testing.T) {
	sub := "https://sub.example.com/abc?x=1"
	cases := []struct{ scheme, want string }{
		{"appone://add/", "appone://add/" + sub},
		{"apptwo://import/", "apptwo://import/" + sub},
		{"appthree://install-config?url=", "appthree://install-config?url=https%3A%2F%2Fsub.example.com%2Fabc%3Fx%3D1"},
		{"", ""},
	}
	for _, c := range cases {
		if got := buildDeeplink(c.scheme, sub); got != c.want {
			t.Errorf("buildDeeplink(%q) = %q, want %q", c.scheme, got, c.want)
		}
	}
	if got := buildDeeplink("appone://add/", ""); got != "" {
		t.Errorf("empty sub should yield empty, got %q", got)
	}
}

func TestSubstituteV2(t *testing.T) {
	sub := "https://sub.example.com/abcDEF"
	user := "tg_12345"
	cases := []struct{ tmpl, want string }{
		{"appone://add/{{SUBSCRIPTION_LINK}}", "appone://add/" + sub},
		{"apptwo://add/{{SUBSCRIPTION_LINK}}#{{USERNAME}}", "apptwo://add/" + sub + "#" + user},
		{"appthree://install-config?url={{SUBSCRIPTION_LINK}}", "appthree://install-config?url=" + sub},
		{"no-placeholder", "no-placeholder"},
	}
	for _, c := range cases {
		if got := substituteV2(c.tmpl, sub, user); got != c.want {
			t.Errorf("substituteV2(%q) = %q, want %q", c.tmpl, got, c.want)
		}
	}
}

func TestAppConfigBase(t *testing.T) {
	cases := map[string]string{
		"https://sub.example.com/u/token": "https://sub.example.com",
		"https://sub.example.com:8443/x":  "https://sub.example.com:8443",
		"http://1.2.3.4/sub/abc":          "http://1.2.3.4",
		"not a url":                       "",
		"":                                "",
	}
	for in, want := range cases {
		if got := appConfigBase(in); got != want {
			t.Errorf("appConfigBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLocalize(t *testing.T) {
	m := acLocalized{"en": "Install", "ru": "Установить"}
	if got := localize(m, "ru"); got != "Установить" {
		t.Errorf("ru: got %q", got)
	}
	if got := localize(m, "fr"); got != "Install" {
		t.Errorf("fr fallback: got %q", got)
	}
	if got := localize(nil, "en"); got != "" {
		t.Errorf("nil: got %q", got)
	}
	only := acLocalized{"ru": "Установить"}
	if got := localize(only, "en"); got != "Установить" {
		t.Errorf("any fallback: got %q", got)
	}
}
