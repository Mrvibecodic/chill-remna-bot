package tribute

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, status int, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subscriptions" || r.Header.Get("Api-Key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
}

func TestListSubscriptions_Shapes(t *testing.T) {
	for name, body := range map[string]string{
		"result":     `{"result":[{"subscriptionId":42,"name":" VIP ","currency":"rub","periods":[{"periodId":1,"period":"Monthly"}]}]}`,
		"snake":      `{"subscriptions":[{"subscription_id":42,"name":"VIP","periods":[{"period_id":1,"period":"monthly"}]}]}`,
		"bare array": `[{"id":42,"name":"VIP","periods":[{"id":1,"period":"monthly"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			serve(t, http.StatusOK, body)
			subs, err := ListSubscriptions(context.Background(), "k")
			if err != nil {
				t.Fatal(err)
			}
			if len(subs) != 1 || subs[0].ID != 42 || subs[0].Name != "VIP" {
				t.Fatalf("подписки: %+v", subs)
			}
			if len(subs[0].Periods) != 1 || subs[0].Periods[0].ID != 1 || subs[0].Periods[0].Period != "monthly" {
				t.Fatalf("периоды: %+v", subs[0].Periods)
			}
		})
	}
}

// Подписка без ID бесполезна для привязки — отбрасывается.
func TestListSubscriptions_SkipsWithoutID(t *testing.T) {
	serve(t, http.StatusOK, `{"result":[{"name":"x"},{"subscriptionId":5,"name":"y"}]}`)
	subs, err := ListSubscriptions(context.Background(), "k")
	if err != nil || len(subs) != 1 || subs[0].ID != 5 {
		t.Fatalf("subs=%+v err=%v", subs, err)
	}
}

func TestListSubscriptions_Errors(t *testing.T) {
	serve(t, http.StatusOK, `[]`)
	if _, err := ListSubscriptions(context.Background(), ""); err == nil {
		t.Fatal("пустой ключ должен давать ошибку")
	}
	_, err := ListSubscriptions(context.Background(), "secret-key-value")
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret-key-value") {
		t.Fatalf("отказ по ключу: %v", err)
	}
	serve(t, http.StatusBadGateway, `oops`)
	if _, err := ListSubscriptions(context.Background(), "k"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("502: %v", err)
	}
	serve(t, http.StatusOK, `{not json`)
	if _, err := ListSubscriptions(context.Background(), "k"); err == nil {
		t.Fatal("битый JSON должен давать ошибку")
	}
}

// Ответ без списка — отказ, а не «подписок нет».
func TestListSubscriptions_ErrorBody(t *testing.T) {
	serve(t, http.StatusOK, `{"error":"invalid api key"}`)
	if _, err := ListSubscriptions(context.Background(), "k"); err == nil || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("ошибка в теле: %v", err)
	}
	serve(t, http.StatusOK, `{"result":[]}`)
	if subs, err := ListSubscriptions(context.Background(), "k"); err != nil || len(subs) != 0 {
		t.Fatalf("пустой список: %v %v", subs, err)
	}
}

// Ключ не уходит на адрес редиректа.
func TestListSubscriptions_NoRedirect(t *testing.T) {
	var leaked string
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Api-Key")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(dst.Close)
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+"/subscriptions", http.StatusFound)
	}))
	t.Cleanup(src.Close)
	old := BaseURL
	BaseURL = src.URL
	t.Cleanup(func() { BaseURL = old })
	if _, err := ListSubscriptions(context.Background(), "SECRET"); err == nil {
		t.Fatal("редирект должен давать ошибку")
	}
	if leaked != "" {
		t.Fatalf("ключ ушёл на адрес редиректа: %q", leaked)
	}
}
