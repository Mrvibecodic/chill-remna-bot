package model

import "testing"

func TestTrafficSnapshotFingerprint(t *testing.T) {
	base := PlanSnapshot{Code: "p", Months: 1, TrafficGB: 100, DeviceLimit: 3, Strategy: "MONTH", Price: "100", Currency: "₽"}
	// Срочный снимок кодируется как раньше: новых полей в JSON нет.
	if enc := base.Encode(); enc != `{"code":"p","months":1,"traffic_gb":100,"device_limit":3,"strategy":"MONTH","price":"100","currency":"₽"}` {
		t.Fatalf("срочный снимок изменил форму: %s", enc)
	}
	fp := base.Fingerprint()
	pack := base
	pack.PackID, pack.PackGB = "abc123", 50
	if pack.Fingerprint() != fp {
		t.Fatal("пакет в снимке не должен менять отпечаток условий")
	}
	tr := base
	tr.Kind = PlanKindTraffic
	if tr.Fingerprint() == fp {
		t.Fatal("вид тарифа — условие сделки и обязан входить в отпечаток")
	}
	st := tr
	st.AddSubStrategy = "DAY"
	if st.Fingerprint() == tr.Fingerprint() {
		t.Fatal("период сброса доп-подписки — условие сделки")
	}
}

func TestPlanTrafficHelpers(t *testing.T) {
	var nilPlan *Plan
	if nilPlan.IsTraffic() || nilPlan.KnownKind() || nilPlan.Pack("abc123") != nil {
		t.Fatal("nil-тариф")
	}
	p := &Plan{Kind: "future"}
	if p.KnownKind() || p.IsTraffic() {
		t.Fatal("незнакомый вид не продаётся")
	}
	p.Normalize()
	if p.Kind != "future" {
		t.Fatal("незнакомый вид не переписывается")
	}
	tp := &Plan{Kind: PlanKindTraffic}
	if tp.AddSubStrategyOrDefault() != "MONTH_ROLLING" {
		t.Fatal("сброс доп-подписки по умолчанию — в день подключения")
	}
	if (&Plan{}).AddSubStrategyOrDefault() != "" {
		t.Fatal("у срочного тарифа своего сброса доп-подписки нет")
	}
	for _, c := range []struct {
		gb, wantGB   int
		to, wantTo   string
		refr, invite bool
	}{
		{10, 10, "", RefToReferrer, true, false},
		{10, 10, RefToInvitee, RefToInvitee, false, true},
		{10, 10, RefToBoth, RefToBoth, true, true},
		{10, 10, "x", RefToReferrer, true, false},
		{-5, 0, RefToBoth, RefToBoth, false, false},
		{PlanTrafficMaxGB + 1, PlanTrafficMaxGB, "", RefToReferrer, true, false},
	} {
		p := &Plan{Kind: PlanKindTraffic, Traffic: &PlanTraffic{RefGB: c.gb, RefTo: c.to}}
		p.Normalize()
		if tr := p.Traffic; tr.RefGB != c.wantGB || tr.RefTo != c.wantTo || tr.RefForReferrer() != c.refr || tr.RefForInvitee() != c.invite {
			t.Fatalf("рефералка %+v → %+v", c, tr)
		}
	}
	for id, ok := range map[string]bool{"abc123": true, "ABC123": false, "abc12": false, "abc1234": false, "ab-123": false} {
		if ValidPackID(id) != ok {
			t.Fatalf("ValidPackID(%q)", id)
		}
	}
}
