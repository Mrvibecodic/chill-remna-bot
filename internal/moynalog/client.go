package moynalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	// Гарантирует наличие базы часовых поясов даже в контейнере без tzdata:
	// чек обязан уходить по московскому времени, а не по TZ контейнера (UTC),
	// иначе ночные платежи конца месяца уезжают в предыдущий налоговый период.
	_ "time/tzdata"
)

var BaseURL = "https://lknpd.nalog.ru/api/v1"

// moscow — зона времени чека. ФНС ждёт местное время самозанятого; бот
// ориентирован на РФ, поэтому Москва (при недоступной базе зон — UTC+3).
var moscow = func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*60*60)
}()

type Client struct {
	http  *http.Client
	login string
	pass  string

	mu    sync.Mutex
	token string
	// authMu сериализует сам логин: без него все горутины фискализации после
	// истечения токена устраивали бы залп параллельных парольных логинов.
	authMu sync.Mutex
}

func New(login, pass string) *Client {
	return &Client{
		http:  &http.Client{Timeout: 25 * time.Second},
		login: login,
		pass:  pass,
	}
}

type deviceInfo struct {
	SourceDeviceID string `json:"sourceDeviceId"`
	SourceType     string `json:"sourceType"`
	AppVersion     string `json:"appVersion"`
}

type authRequest struct {
	Username   string     `json:"username"`
	Password   string     `json:"password"`
	DeviceInfo deviceInfo `json:"deviceInfo"`
}

type authResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refreshToken"`
}

func (c *Client) authenticate(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	body, _ := json.Marshal(authRequest{
		Username:   c.login,
		Password:   c.pass,
		DeviceInfo: deviceInfo{SourceDeviceID: "remnabot", SourceType: "WEB", AppVersion: "1.0.0"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/auth/lkfl", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("moynalog auth: status %d: %s", resp.StatusCode, b)
	}
	var ar authResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return err
	}
	if ar.Token == "" {
		return fmt.Errorf("moynalog auth: empty token")
	}
	c.mu.Lock()
	c.token = ar.Token
	c.mu.Unlock()
	return nil
}

type incomeService struct {
	Name     string  `json:"name"`
	Amount   float64 `json:"amount"`
	Quantity int     `json:"quantity"`
}

type incomeClient struct {
	IncomeType string `json:"incomeType"`
}

type incomeRequest struct {
	OperationTime                   string          `json:"operationTime"`
	RequestTime                     string          `json:"requestTime"`
	Services                        []incomeService `json:"services"`
	TotalAmount                     string          `json:"totalAmount"`
	Client                          incomeClient    `json:"client"`
	PaymentType                     string          `json:"paymentType"`
	IgnoreMaxTotalIncomeRestriction bool            `json:"ignoreMaxTotalIncomeRestriction"`
}

type incomeResponse struct {
	ApprovedReceiptUUID string `json:"approvedReceiptUuid"`
	ID                  string `json:"id"`
}

func (c *Client) CreateIncome(ctx context.Context, amount float64, name string) (string, error) {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok == "" {
		if err := c.authenticate(ctx); err != nil {
			return "", err
		}
	}
	// Сетевые сбои и 5xx ретраим: чек — юридическая обязанность, а одна
	// оборванная попытка иначе терялась без следа.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		id, status, errBody, err := c.createOnce(ctx, amount, name)
		if err != nil {
			lastErr = err
			continue
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if err := c.authenticate(ctx); err != nil {
				return "", err
			}
			id, status, errBody, err = c.createOnce(ctx, amount, name)
			if err != nil {
				lastErr = err
				continue
			}
		}
		switch {
		case status == http.StatusOK || status == http.StatusCreated:
			if id == "" {
				// 200 без идентификатора чека — не успех: считать такой ответ
				// зарегистрированным доходом нельзя.
				return "", fmt.Errorf("moynalog income: ответ %d без идентификатора чека", status)
			}
			return id, nil
		case status >= 500:
			lastErr = fmt.Errorf("moynalog income: status %d: %s", status, errBody)
			continue
		default:
			// 4xx ретраить бессмысленно; тело содержит код причины
			// (validation.failed, превышение лимита и т.п.).
			return "", fmt.Errorf("moynalog income: status %d: %s", status, errBody)
		}
	}
	return "", lastErr
}

func (c *Client) createOnce(ctx context.Context, amount float64, name string) (id string, status int, errBody string, err error) {
	now := time.Now().In(moscow).Format("2006-01-02T15:04:05-07:00")
	body, _ := json.Marshal(incomeRequest{
		OperationTime:                   now,
		RequestTime:                     now,
		Services:                        []incomeService{{Name: name, Amount: amount, Quantity: 1}},
		TotalAmount:                     fmt.Sprintf("%.2f", amount),
		Client:                          incomeClient{IncomeType: "FROM_INDIVIDUAL"},
		PaymentType:                     "CASH",
		IgnoreMaxTotalIncomeRestriction: false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/income", bytes.NewReader(body))
	if err != nil {
		return "", 0, "", err
	}
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", resp.StatusCode, "", nil
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var ir incomeResponse
	_ = json.Unmarshal(raw, &ir)
	id = ir.ApprovedReceiptUUID
	if id == "" {
		id = ir.ID
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		errBody = strings.TrimSpace(string(raw))
	}
	return id, resp.StatusCode, errBody, nil
}
