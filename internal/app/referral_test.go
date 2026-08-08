package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

func refTestApp(t *testing.T) (*App, *fakeStore) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	a.botCfg.NormalizeReferral()
	a.botCfg.Referral.Enabled = true
	a.botCfg.Referral.BonusKind = model.ReferralBonusBalance
	a.botCfg.Referral.BonusValue = 50
	a.botCfg.Referral.OnFirstPay = true
	return a, fs
}

func TestReferral_BindAndBonusOnce(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 200)

	a.bindReferrer(ctx, 300, "ref_200")
	if u, _ := fs.GetUser(ctx, 300); u == nil || u.ReferredBy != 200 {
		t.Fatalf("referred_by не привязан: %+v", u)
	}

	a.payReferralBonus(ctx, 300)
	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 5000 {
		t.Fatalf("ожидался бонус 5000 коп, got %d", ref.Balance)
	}
	a.payReferralBonus(ctx, 300)
	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 5000 {
		t.Fatalf("двойное начисление бонуса: %d", ref.Balance)
	}
	if n, _ := fs.CountReferrals(ctx, 200); n != 1 {
		t.Fatalf("ожидался 1 реферал, got %d", n)
	}
}

func TestReferral_SelfAndExistingRejected(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()

	_ = fs.UpsertUser(ctx, 300)
	a.bindReferrer(ctx, 300, "ref_200")
	if u, _ := fs.GetUser(ctx, 300); u.ReferredBy != 0 {
		t.Fatal("существующего пользователя привязывать нельзя")
	}

	_ = fs.UpsertUser(ctx, 400)
	a.bindReferrer(ctx, 500, "ref_500")
	if u, _ := fs.GetUser(ctx, 500); u != nil && u.ReferredBy != 0 {
		t.Fatal("самоприглашение запрещено")
	}
}

func TestReferral_DisabledNoBind(t *testing.T) {
	a, fs := refTestApp(t)
	a.botCfg.Referral.Enabled = false
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 200)
	a.bindReferrer(ctx, 300, "ref_200")
	if u, _ := fs.GetUser(ctx, 300); u != nil && u.ReferredBy != 0 {
		t.Fatal("при выключенной рефералке привязки быть не должно")
	}
}

func TestReferral_InviteeBonusAndEarned(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.botCfg.Referral.InviteeKind = model.ReferralBonusBalance
	a.botCfg.Referral.InviteeValue = 30

	_ = fs.UpsertUser(ctx, 200)
	a.bindReferrer(ctx, 300, "ref_200")
	a.payReferralBonus(ctx, 300)

	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 5000 || ref.RefEarned != 5000 {
		t.Fatalf("referrer bonus/earned: bal=%d earned=%d", ref.Balance, ref.RefEarned)
	}
	if inv, _ := fs.GetUser(ctx, 300); inv.Balance != 3000 {
		t.Fatalf("invitee welcome bonus: bal=%d want 3000", inv.Balance)
	}
	// once only
	a.payReferralBonus(ctx, 300)
	if inv, _ := fs.GetUser(ctx, 300); inv.Balance != 3000 {
		t.Fatalf("invitee double-paid: bal=%d", inv.Balance)
	}
}

func TestReferral_Percent(t *testing.T) {
	a, fs := refTestApp(t)
	ctx := context.Background()
	a.botCfg.Referral.BonusValue = 0
	a.botCfg.Referral.Percent = 10

	_ = fs.UpsertUser(ctx, 200)
	a.bindReferrer(ctx, 300, "ref_200")

	a.creditReferralPercent(ctx, 300, "500 ₽")
	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 5000 || ref.RefEarned != 5000 {
		t.Fatalf("percent: bal=%d earned=%d want 5000/5000", ref.Balance, ref.RefEarned)
	}
	a.creditReferralPercent(ctx, 300, "500 ₽")
	if ref, _ := fs.GetUser(ctx, 200); ref.Balance != 10000 {
		t.Fatalf("percent recurring: bal=%d want 10000", ref.Balance)
	}
}

// Бонусные дни (реферальные и промокод kind=days) идут в addReferralDays.
// Они обязаны только сдвигать срок: если подставить в панель глобальные
// Plan/Pricing, у человека слетит купленный набор сквадов — в том числе
// внутри самой покупки, сразу после применения условий оплаченного срока.
func TestReferralDays_DoesNotOverwriteLimits(t *testing.T) {
	var patched map[string]any
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/by-telegram-id/") {
			_, _ = w.Write([]byte(`{"response":[{"uuid":"u1","tag":"CHILLBOT","username":"tg_555","subscriptionUrl":"https://sub/x","expireAt":"2030-01-01T00:00:00Z"}]}`))
			return
		}
		if r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patched = body
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u1","subscriptionUrl":"https://sub/x","expireAt":"2030-01-08T00:00:00Z"}}`))
	}))
	defer panel.Close()

	a, fs := refTestApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 555)
	a.botCfg.Plan.ActiveInternalSquads = []string{"squad-global"}
	a.botCfg.Plan.ExternalSquadUUID = "ext-global"
	a.botCfg.Pricing.TrafficStrategy = "WEEK"
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: panel.URL, APIToken: "t"})

	ok, found := a.addReferralDays(ctx, 555, 7)
	if !ok || !found {
		t.Fatalf("бонусные дни не начислены: ok=%v found=%v", ok, found)
	}
	if patched == nil {
		t.Fatal("панель не получила PATCH")
	}
	if _, has := patched["activeInternalSquads"]; has {
		t.Fatalf("бонусные дни перезаписали сквады: %+v", patched)
	}
	if _, has := patched["externalSquadUuid"]; has {
		t.Fatalf("бонусные дни перезаписали внешний сквад: %+v", patched)
	}
	if _, has := patched["trafficLimitStrategy"]; has {
		t.Fatalf("бонусные дни перезаписали стратегию сброса: %+v", patched)
	}
	if patched["expireAt"] == nil {
		t.Fatalf("срок не сдвинут: %+v", patched)
	}
}
