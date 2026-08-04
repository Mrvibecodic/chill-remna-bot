// Package heleket — тонкая обёртка над официальным SDK Heleket
// (github.com/heleket/go-sdk). Обёртка нужна, чтобы остальной бот работал со
// своими типами и не зависел напрямую от SDK версии 0.x: смена или замена SDK
// затрагивает только этот пакет.
//
// Подпись запросов и, что важнее, проверка подписи вебхука вынесены в SDK
// намеренно. Heleket подписывает тело по-PHP-шному (порядок ключей сохраняется,
// «/» экранируется как «\/»), поэтому пересборка payload через map в Go даёт
// другой байтовый вид и подпись не сходится. SDK умеет считать её по сырым
// байтам — см. webhook.Verifier.VerifyRaw.
package heleket

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/heleket/go-sdk"
	"github.com/heleket/go-sdk/webhook"
)

// BaseURL — адрес API. Переопределяется в тестах.
var BaseURL = sdk.DefaultBaseURL

// Client — клиент платёжного API Heleket.
type Client struct {
	pay      *sdk.PaymentClient
	verifier *webhook.Verifier
}

// New создаёт клиента. apiKey — ПЛАТЁЖНЫЙ ключ мерчанта (у выплат он свой,
// перепутанные ключи дают отказ и на запросах, и на проверке вебхука).
func New(merchantID, apiKey string) (*Client, error) {
	pay, err := sdk.NewPaymentClient(merchantID, apiKey, sdk.WithBaseURL(BaseURL))
	if err != nil {
		return nil, err
	}
	return &Client{pay: pay, verifier: webhook.NewVerifier(apiKey)}, nil
}

// InvoiceRequest — параметры счёта. Amount/Currency/OrderID обязательны.
type InvoiceRequest struct {
	Amount   string
	Currency string
	OrderID  string

	ToCurrency     string
	Subtract       int
	Lifetime       int
	CallbackURL    string
	ReturnURL      string
	SuccessURL     string
	AdditionalData string
}

// Invoice — счёт в терминах бота.
type Invoice struct {
	UUID           string
	OrderID        string
	Status         string
	URL            string
	Amount         string
	Currency       string
	PayerAmount    string
	PayerCurrency  string
	AdditionalData string
	IsFinal        bool
}

// Event — проверенный вебхук.
type Event struct {
	UUID    string
	OrderID string
	Status  string
	Type    string
}

// Service — доступная пара «валюта + сеть» с лимитами.
type Service struct {
	Currency    string
	Network     string
	IsAvailable bool
	MinAmount   string
	MaxAmount   string
}

// CreateInvoice выставляет счёт.
func (c *Client) CreateInvoice(ctx context.Context, req InvoiceRequest) (*Invoice, error) {
	inv, err := c.pay.CreateInvoice(ctx, sdk.CreateInvoiceRequest{
		Amount:         req.Amount,
		Currency:       req.Currency,
		OrderID:        req.OrderID,
		ToCurrency:     req.ToCurrency,
		Subtract:       req.Subtract,
		Lifetime:       req.Lifetime,
		URLCallback:    req.CallbackURL,
		URLReturn:      req.ReturnURL,
		URLSuccess:     req.SuccessURL,
		AdditionalData: req.AdditionalData,
	})
	if err != nil {
		return nil, Humanize(err)
	}
	return convert(inv), nil
}

// Info возвращает актуальное состояние счёта по его uuid.
func (c *Client) Info(ctx context.Context, uuid string) (*Invoice, error) {
	inv, err := c.pay.GetInfo(ctx, sdk.InfoOptions{UUID: uuid})
	if err != nil {
		return nil, Humanize(err)
	}
	return convert(inv), nil
}

// Services отдаёт список доступных валют и сетей — этим проверяются ключи в
// админке: неверный или выплатной ключ сразу даёт ошибку.
func (c *Client) Services(ctx context.Context) ([]Service, error) {
	list, err := c.pay.ListServices(ctx)
	if err != nil {
		return nil, Humanize(err)
	}
	out := make([]Service, 0, len(list))
	for _, s := range list {
		out = append(out, Service{
			Currency:    s.Currency,
			Network:     s.Network,
			IsAvailable: s.IsAvailable,
			MinAmount:   s.Limit.MinAmount,
			MaxAmount:   s.Limit.MaxAmount,
		})
	}
	return out, nil
}

// VerifyWebhook проверяет подпись вебхука по СЫРЫМ байтам тела.
func (c *Client) VerifyWebhook(raw []byte) (*Event, error) {
	p, err := c.verifier.VerifyRaw(raw)
	if err != nil {
		return nil, err
	}
	return &Event{UUID: p.UUID, OrderID: p.OrderID, Status: p.Status, Type: p.Type}, nil
}

// Successful — оплата прошла (paid или paid_over).
func Successful(status string) bool { return sdk.PaymentStatus(status).IsSuccessful() }

// Final — статус больше не изменится.
func Final(status string) bool { return sdk.PaymentStatus(status).IsFinal() }

// Статусы, которые бот разбирает отдельно.
const (
	StatusWrongAmount        = string(sdk.PaymentStatusWrongAmount)
	StatusWrongAmountWaiting = string(sdk.PaymentStatusWrongAmountWaiting)
	StatusLocked             = string(sdk.PaymentStatusLocked)
)

// Humanize превращает ошибку SDK в текст, который не стыдно показать
// пользователю: без сырого JSON шлюза, с перечислением полей у 422.
func Humanize(err error) error {
	if err == nil {
		return nil
	}
	var ve *sdk.ValidationError
	if errors.As(err, &ve) {
		keys := make([]string, 0, len(ve.Fields))
		for k := range ve.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+strings.Join(ve.Fields[k], ", "))
		}
		if len(parts) > 0 {
			return fmt.Errorf("запрос отклонён (%s)", strings.Join(parts, "; "))
		}
	}
	var ae *sdk.APIError
	if errors.As(err, &ae) && ae.Message != "" {
		return errors.New(ae.Message)
	}
	if errors.Is(err, sdk.ErrTransport) {
		return errors.New("шлюз недоступен, попробуйте позже")
	}
	return err
}

func convert(inv *sdk.Invoice) *Invoice {
	if inv == nil {
		return nil
	}
	status := string(inv.PaymentStatus)
	if status == "" {
		status = string(inv.Status)
	}
	out := &Invoice{
		UUID:          inv.UUID,
		OrderID:       inv.OrderID,
		Status:        status,
		URL:           inv.URL,
		Amount:        inv.Amount,
		Currency:      inv.Currency,
		PayerAmount:   inv.PayerAmount,
		PayerCurrency: inv.PayerCurrency,
		IsFinal:       inv.IsFinal,
	}
	if inv.AdditionalData != nil {
		out.AdditionalData = *inv.AdditionalData
	}
	if out.PayerAmount == "" && inv.PaymentAmount != nil {
		out.PayerAmount = *inv.PaymentAmount
	}
	return out
}
