package yoomoney

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Пример из официальной документации: подготовленная строка и секрет
// «secret123» дают именно эту подпись.
func docForm() url.Values {
	return url.Values{
		"notification_type": {"p2p-incoming"},
		"operation_id":      {"441361714955017004"},
		"amount":            {"98.00"},
		"withdraw_amount":   {"100.00"},
		"currency":          {"643"},
		"datetime":          {"2013-12-26T08:28:34Z"},
		"sender":            {"41000000000"},
		"codepro":           {"false"},
		"label":             {"ML23045"},
		"unaccepted":        {"false"},
		"sha1_hash":         {"ac13833bd6ba9eff1fa9e4bed76f3d6ebb57f6c0"},
		"sign":              {"a452af731650e2c5b39abcdc7c28dd27db7b3b654c2230ad2c386e64afb98605"},
	}
}

func TestSignMatchesDocExample(t *testing.T) {
	if got := Sign(docForm(), "secret123"); got != "a452af731650e2c5b39abcdc7c28dd27db7b3b654c2230ad2c386e64afb98605" {
		t.Fatalf("подпись: %s", got)
	}
	if !VerifySign(docForm(), "secret123") {
		t.Fatal("подпись из доки не прошла")
	}
	f := docForm()
	f.Set("amount", "9800.00")
	if VerifySign(f, "secret123") {
		t.Fatal("подменённая сумма прошла проверку")
	}
	if VerifySign(docForm(), "") || VerifySign(docForm(), "other") {
		t.Fatal("чужой или пустой секрет прошёл")
	}
	f = docForm()
	f.Del("sign")
	if VerifySign(f, "secret123") {
		t.Fatal("уведомление без подписи прошло")
	}
	// Пробел и тильда кодируются по RFC 3986, а не по правилам форм.
	if rfc3986("a b~") != "a%20b~" {
		t.Fatalf("кодирование: %q", rfc3986("a b~"))
	}
}

func TestParseNotification(t *testing.T) {
	n, err := ParseNotification(docForm())
	if err != nil {
		t.Fatal(err)
	}
	if n.AmountK != 9800 || n.WithdrawK != 10000 || n.Label != "ML23045" || n.OperationID != "441361714955017004" || n.CodePro || n.Unaccepted || n.Test {
		t.Fatalf("разбор: %+v", n)
	}
	f := docForm()
	f.Set("amount", "98,00")
	if _, err := ParseNotification(f); err == nil {
		t.Fatal("запятая в сумме прошла")
	}
	f = docForm()
	f.Set("test_notification", "true")
	f.Set("codepro", "true")
	if n, _ := ParseNotification(f); !n.Test || !n.CodePro {
		t.Fatalf("флаги: %+v", n)
	}
}

func TestParseKopecks(t *testing.T) {
	cases := map[string]int64{"100": 10000, "98.00": 9800, "1234.5": 123450, "0.07": 7}
	for in, want := range cases {
		if got, ok := ParseKopecks(in); !ok || got != want {
			t.Errorf("%q → %d,%v (ждали %d)", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "1 000", "1e3", "-5", "1.234", "abc", ".5"} {
		if _, ok := ParseKopecks(bad); ok {
			t.Errorf("%q разобралось", bad)
		}
	}
	if FormatSum(123405) != "1234.05" || FormatSum(7) != "0.07" {
		t.Fatalf("формат суммы: %s %s", FormatSum(123405), FormatSum(7))
	}
	// Карта: 3 % + копейка на округление; на 1000 ₽ это 30.01 ₽. Кошелёк: 1 %.
	if MaxFee(100000, PayTypeCard) != 3001 || MaxFee(100000, "") != 3001 || MaxFee(100000, PayTypeWallet) != 1001 {
		t.Fatalf("MaxFee: %d %d", MaxFee(100000, PayTypeCard), MaxFee(100000, PayTypeWallet))
	}
}

func TestFormFields(t *testing.T) {
	f := FormFields("410011000000000", "pc", 79000, "ym:1:abcd", "https://t.me/x")
	want := [][2]string{
		{"receiver", "410011000000000"}, {"quickpay-form", "button"}, {"paymentType", "PC"},
		{"sum", "790.00"}, {"label", "ym:1:abcd"}, {"successURL", "https://t.me/x"},
	}
	if len(f) != len(want) {
		t.Fatalf("полей %d: %v", len(f), f)
	}
	for i := range want {
		if f[i] != want[i] {
			t.Fatalf("поле %d: %v, ждали %v", i, f[i], want[i])
		}
	}
	if PayTypeOrDefault("") != PayTypeCard || PayTypeOrDefault("junk") != PayTypeCard || PayTypeOrDefault("PC") != PayTypeWallet {
		t.Fatal("тип оплаты по умолчанию")
	}
	if n := len(FormFields("1", "AC", 1, "l", "")); n != 5 {
		t.Fatalf("без successURL полей %d", n)
	}
}

func TestClientHistoryAccountToken(t *testing.T) {
	var gotAuth, gotLabel, gotTokenForm string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/operation-history":
			gotLabel = r.PostForm.Get("label")
			_, _ = w.Write([]byte(`{"operations":[{"operation_id":"op1","status":"success","direction":"in","amount":97.00,"label":"ym:1:a","type":"deposition"},{"operation_id":"op0","status":"refused","direction":"in","amount":1.00,"label":"ym:1:a"}]}`))
		case "/api/account-info":
			if gotAuth == "Bearer bad" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"account":"410011000000000","balance":10.5,"currency":"643"}`))
		case "/oauth/token":
			gotTokenForm = r.PostForm.Encode()
			if r.PostForm.Get("code") == "bad" {
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok-1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := APIBaseURL
	APIBaseURL = srv.URL
	defer func() { APIBaseURL = old }()
	ctx := context.Background()

	c := New("tok-1")
	ops, err := c.OperationsByLabel(ctx, "ym:1:a")
	if err != nil || len(ops) != 2 || gotAuth != "Bearer tok-1" || gotLabel != "ym:1:a" {
		t.Fatalf("история: %v %v auth=%q label=%q", ops, err, gotAuth, gotLabel)
	}
	if !ops[0].Paid() || ops[0].AmountK() != 9700 || ops[1].Paid() {
		t.Fatalf("статусы: %+v", ops)
	}
	acc, err := c.AccountInfo(ctx)
	if err != nil || acc != "410011000000000" {
		t.Fatalf("account-info: %q %v", acc, err)
	}
	if _, err := New("bad").AccountInfo(ctx); err != ErrInvalidToken {
		t.Fatalf("401 должен быть ErrInvalidToken: %v", err)
	}
	tok, err := ExchangeCode(ctx, "cid", "", "https://bot.example.test/yoomoney/oauth", "c0de")
	if err != nil || tok != "tok-1" {
		t.Fatalf("обмен кода: %q %v", tok, err)
	}
	want := url.Values{"code": {"c0de"}, "client_id": {"cid"}, "grant_type": {"authorization_code"}, "redirect_uri": {"https://bot.example.test/yoomoney/oauth"}}.Encode()
	if gotTokenForm != want {
		t.Fatalf("тело обмена: %s", gotTokenForm)
	}
	if _, err := ExchangeCode(ctx, "cid", "sec", "u", "bad"); err == nil {
		t.Fatal("ошибка обмена проглочена")
	}
}
