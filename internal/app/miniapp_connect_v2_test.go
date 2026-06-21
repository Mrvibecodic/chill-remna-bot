package app

import (
	"encoding/json"
	"testing"
)

func TestParseV2Sample(t *testing.T) {
	body := []byte(`{
	  "version":"1","locales":["en"],
	  "platforms":{
	    "ios":{"apps":[
	      {"name":"App One","featured":true,"blocks":[
	        {"description":{"en":"block-store"},"buttons":[{"link":"https://example.com/store/app-one","type":"external","text":{"en":"store-btn"}}]},
	        {"description":{"en":"block-add"},"buttons":[{"link":"appone://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"en":"add-btn"}}]}
	      ]},
	      {"name":"App Two","featured":false,"blocks":[
	        {"buttons":[{"link":"apptwo://add/{{SUBSCRIPTION_LINK}}#{{USERNAME}}","type":"subscriptionLink","text":{"en":"add-btn"}}]}
	      ]}
	    ]},
	    "android":{"apps":[]}
	  }
	}`)
	var v2 appConfigV2
	if err := json.Unmarshal(body, &v2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(v2.Platforms.IOS.Apps) != 2 {
		t.Fatalf("ios apps = %d, want 2", len(v2.Platforms.IOS.Apps))
	}
	sub := "https://sub.example.com/AbCd"
	out := acBuildV2(v2.Platforms.IOS.Apps, sub, "tg_99", "en")
	if len(out) != 2 {
		t.Fatalf("built %d apps, want 2", len(out))
	}
	if !out[0].Featured || out[0].Name != "App One" {
		t.Errorf("featured-first failed: %+v", out[0])
	}
	if out[0].Deeplink != "appone://add/"+sub {
		t.Errorf("deeplink = %q", out[0].Deeplink)
	}
	if len(out[0].Installs) != 1 || out[0].Installs[0].Text != "store-btn" {
		t.Errorf("installs = %+v", out[0].Installs)
	}
	if out[0].AddDesc != "block-add" {
		t.Errorf("add_desc = %q", out[0].AddDesc)
	}
	if out[1].Deeplink != "apptwo://add/"+sub+"#tg_99" {
		t.Errorf("deeplink2 = %q", out[1].Deeplink)
	}
}
