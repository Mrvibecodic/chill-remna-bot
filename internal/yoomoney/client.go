// Package yoomoney — приём переводов на кошелёк ЮMoney: форма quickpay,
// проверка подписи HTTP-уведомления, API кошелька (история операций,
// сведения о счёте) и обмен OAuth-кода на токен. Всё по официальной
// документации yoomoney.ru/docs; сторонних библиотек нет.
package yoomoney

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Адреса вынесены в переменные ради тестов.
var (
	// FormURL — куда уходит POST-форма quickpay.
	FormURL = "https://yoomoney.ru/quickpay/confirm"
	// AuthorizeURL — страница ЮMoney, где владелец кошелька разрешает доступ.
	AuthorizeURL = "https://yoomoney.ru/oauth/authorize"
	// APIBaseURL — корень API кошелька и обмена токена.
	APIBaseURL = "https://yoomoney.ru"
)

const (
	// PayTypeCard — оплата банковской картой (комиссия 3 % с получателя).
	PayTypeCard = "AC"
	// PayTypeWallet — оплата с кошелька ЮMoney (комиссия 1 % с получателя).
	PayTypeWallet = "PC"
	// LabelMaxLen — предел длины метки платежа по доке формы.
	LabelMaxLen = 64
	// Scope — права токена: номер кошелька (сверка с получателем) и история
	// операций (проверка оплаты по метке). Лишних прав не просим.
	Scope = "account-info operation-history"
	// CurrencyRUB — код валюты в уведомлениях (ISO 4217).
	CurrencyRUB = "643"
	// CardFeePermille / WalletFeePermille — потолок комиссии получателя в
	// промилле: 3 % с карты, 1 % с кошелька ЮMoney. Используется там, где
	// известна только зачисленная сумма.
	CardFeePermille   = 30
	WalletFeePermille = 10
)

// ErrInvalidToken — API ответил 401: токен отозван, истёк или чужой.
var ErrInvalidToken = errors.New("yoomoney: токен недействителен")

// PayTypeOrDefault приводит значение из конфига к допустимому.
func PayTypeOrDefault(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), PayTypeWallet) {
		return PayTypeWallet
	}
	return PayTypeCard
}

// Kopecks → строка суммы формы и сравнений: «123.45».
func FormatSum(kopecks int64) string {
	if kopecks < 0 {
		kopecks = 0
	}
	return fmt.Sprintf("%d.%02d", kopecks/100, kopecks%100)
}

// ParseKopecks разбирает сумму ЮMoney («98.00», «1234.5», «100») в копейки.
// Строгий разбор: лишние символы, пробелы и больше двух знаков после точки —
// отказ, а не округление.
func ParseKopecks(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " +-eE") {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > 2 {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	var f int64
	if frac != "" {
		f, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, false
		}
		if len(frac) == 1 {
			f *= 10
		}
	}
	if w > (1<<62)/100 {
		return 0, false
	}
	return w*100 + f, true
}

// MaxFee — потолок комиссии получателя для суммы счёта при данном способе
// оплаты (с округлением вверх плюс копейка на округление самой ЮMoney).
func MaxFee(kopecks int64, payType string) int64 {
	permille := int64(CardFeePermille)
	if PayTypeOrDefault(payType) == PayTypeWallet {
		permille = WalletFeePermille
	}
	return (kopecks*permille+999)/1000 + 1
}

// Sign считает подпись уведомления: все параметры, кроме sign, сортируются по
// имени, значения кодируются по RFC 3986, пары склеиваются через «&», затем
// HMAC-SHA256 с секретом в нижнем hex.
func Sign(form url.Values, secret string) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		if k == "sign" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(rfc3986(form.Get(k)))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(b.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySign — подпись уведомления сходится с секретом (сравнение за
// постоянное время). Пустой секрет или пустая подпись — всегда отказ.
func VerifySign(form url.Values, secret string) bool {
	got := strings.ToLower(strings.TrimSpace(form.Get("sign")))
	if secret == "" || got == "" {
		return false
	}
	want := Sign(form, secret)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// rfc3986 — url.QueryEscape, но пробел как %20 и «~» без кодирования.
func rfc3986(s string) string {
	s = url.QueryEscape(s)
	s = strings.ReplaceAll(s, "+", "%20")
	return strings.ReplaceAll(s, "%7E", "~")
}

// Notification — разобранное HTTP-уведомление о переводе.
type Notification struct {
	Type        string
	OperationID string
	// AmountK — зачислено на кошелёк (за вычетом комиссии), копейки.
	AmountK int64
	// WithdrawK — сколько заплатил отправитель, копейки.
	WithdrawK  int64
	Currency   string
	Datetime   string
	Sender     string
	Label      string
	CodePro    bool
	Unaccepted bool
	// Test — тестовое уведомление с кнопки «Протестировать» в настройках.
	Test bool
}

// ParseNotification разбирает поля уведомления. Ошибка — суммы не разобрались.
func ParseNotification(form url.Values) (Notification, error) {
	n := Notification{
		Type:        form.Get("notification_type"),
		OperationID: strings.TrimSpace(form.Get("operation_id")),
		Currency:    strings.TrimSpace(form.Get("currency")),
		Datetime:    strings.TrimSpace(form.Get("datetime")),
		Sender:      strings.TrimSpace(form.Get("sender")),
		Label:       strings.TrimSpace(form.Get("label")),
		CodePro:     isTrue(form.Get("codepro")),
		Unaccepted:  isTrue(form.Get("unaccepted")),
		Test:        isTrue(form.Get("test_notification")),
	}
	var ok bool
	if n.AmountK, ok = ParseKopecks(form.Get("amount")); !ok {
		return n, fmt.Errorf("yoomoney: сумма amount=%q не разобрана", form.Get("amount"))
	}
	if w := form.Get("withdraw_amount"); w != "" {
		if n.WithdrawK, ok = ParseKopecks(w); !ok {
			return n, fmt.Errorf("yoomoney: сумма withdraw_amount=%q не разобрана", w)
		}
	}
	return n, nil
}

func isTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// FormFields — поля POST-формы quickpay в порядке отправки.
func FormFields(receiver, payType string, kopecks int64, label, successURL string) [][2]string {
	f := [][2]string{
		{"receiver", receiver},
		{"quickpay-form", "button"},
		{"paymentType", PayTypeOrDefault(payType)},
		{"sum", FormatSum(kopecks)},
		{"label", label},
	}
	if successURL != "" {
		f = append(f, [2]string{"successURL", successURL})
	}
	return f
}

// AuthorizeFields — поля POST-формы запроса разрешения на доступ к кошельку.
func AuthorizeFields(clientID, redirectURI string) [][2]string {
	return [][2]string{
		{"client_id", clientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURI},
		{"scope", Scope},
	}
}

// Client — API кошелька под токеном владельца.
type Client struct {
	http  *http.Client
	token string
}

// New — клиент API; пустой токен допустим только для ExchangeCode.
func New(token string) *Client {
	return &Client{
		// Страховка от зависшего соединения; обычный предел задаёт контекст
		// вызывающего (вебхук — 15 с, кнопка и сверка — свой).
		http:  &http.Client{Timeout: 20 * time.Second},
		token: token,
	}
}

// Operation — запись истории кошелька.
type Operation struct {
	ID        string  `json:"operation_id"`
	Status    string  `json:"status"`
	Datetime  string  `json:"datetime"`
	Title     string  `json:"title"`
	Direction string  `json:"direction"`
	Amount    float64 `json:"amount"`
	Label     string  `json:"label"`
	Type      string  `json:"type"`
}

// Paid — операция завершена и деньги пришли НА кошелёк.
func (o Operation) Paid() bool {
	return o.Status == "success" && o.Direction == "in"
}

// AmountK — зачисленная сумма в копейках.
func (o Operation) AmountK() int64 {
	return int64(o.Amount*100 + 0.5)
}

type historyResponse struct {
	Error      string      `json:"error"`
	Operations []Operation `json:"operations"`
}

// OperationsByLabel — операции кошелька с этой меткой (новые первыми).
func (c *Client) OperationsByLabel(ctx context.Context, label string) ([]Operation, error) {
	form := url.Values{"label": {label}, "records": {"30"}}
	var out historyResponse
	if err := c.post(ctx, "/api/operation-history", form, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("yoomoney: operation-history: %s", out.Error)
	}
	return out.Operations, nil
}

type accountResponse struct {
	Error   string `json:"error"`
	Account string `json:"account"`
}

// AccountInfo — номер кошелька, которому принадлежит токен.
func (c *Client) AccountInfo(ctx context.Context) (string, error) {
	var out accountResponse
	if err := c.post(ctx, "/api/account-info", url.Values{}, &out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("yoomoney: account-info: %s", out.Error)
	}
	if out.Account == "" {
		return "", errors.New("yoomoney: account-info без номера кошелька")
	}
	return out.Account, nil
}

func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("yoomoney: %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("yoomoney: %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrInvalidToken
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("yoomoney: %s: HTTP %d: %s", path, resp.StatusCode, trim(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("yoomoney: %s: ответ не JSON: %w", path, err)
	}
	return nil
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
}

// ExchangeCode меняет временный код авторизации на токен. clientSecret —
// только у приложений, зарегистрированных «с проверкой подлинности».
func ExchangeCode(ctx context.Context, clientID, clientSecret, redirectURI, code string) (string, error) {
	form := url.Values{
		"code":         {code},
		"client_id":    {clientID},
		"grant_type":   {"authorization_code"},
		"redirect_uri": {redirectURI},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBaseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("yoomoney: oauth/token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("yoomoney: oauth/token: %w", err)
	}
	var out tokenResponse
	if jsonErr := json.Unmarshal(body, &out); jsonErr != nil {
		return "", fmt.Errorf("yoomoney: oauth/token: HTTP %d: %s", resp.StatusCode, trim(body))
	}
	if out.Error != "" {
		return "", fmt.Errorf("yoomoney: oauth/token: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("yoomoney: oauth/token: HTTP %d без access_token", resp.StatusCode)
	}
	return out.AccessToken, nil
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
