package model

import "testing"

func TestTributeLinks(t *testing.T) {
	c := TributeConfig{Links: []TributeLink{
		{Plan: "vip", SubID: 42, Periods: []string{"monthly"}},
		{Plan: PlanCodeBase},
	}}
	if l := c.LinkBySub(42); l == nil || l.Plan != "vip" {
		t.Fatalf("LinkBySub(42) = %+v", l)
	}
	// Нулевой ID не находит «Базовый» без ID подписки.
	if l := c.LinkBySub(0); l != nil {
		t.Fatalf("LinkBySub(0) = %+v", l)
	}
	if c.StrictSubs() {
		t.Fatal("«Базовый» без ID не включает строгий режим")
	}
	c.Links[1].SubID = 7
	if !c.StrictSubs() {
		t.Fatal("«Базовый» с ID включает строгий режим")
	}
}

// Копия не делит память с оригиналом: конфиг читают без замка.
func TestTributeClone(t *testing.T) {
	c := TributeConfig{Links: []TributeLink{{Plan: "vip", SubID: 42, Periods: []string{"monthly"}}}}
	d := c.Clone()
	d.Links[0].SubID = 1
	d.Links[0].Periods[0] = "yearly"
	if c.Links[0].SubID != 42 || c.Links[0].Periods[0] != "monthly" {
		t.Fatalf("копия изменила оригинал: %+v", c.Links[0])
	}
}
