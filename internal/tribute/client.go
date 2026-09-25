// Package tribute — тонкий клиент REST API Tribute: только то, что нужно
// админке бота, — список подписок автора с их периодами.
package tribute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BaseURL — адрес API. Переменная, чтобы тесты подставляли свой сервер.
var BaseURL = "https://tribute.tg/api/v1"

// Бот обрабатывает апдейты одним воркером: запрос из админки не должен
// держать его дольше нескольких секунд.
const requestTimeout = 8 * time.Second

// Предел тела ответа: список подписок автора — килобайты.
const maxBody = 1 << 20

// Редиректы не выполняются: Go переносит нестандартные заголовки, в том числе
// Api-Key, на адрес редиректа, а он может вести на чужой хост.
var httpClient = &http.Client{
	Timeout: requestTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Period — один период подписки.
type Period struct {
	ID     int64  `json:"id"`
	Period string `json:"period"`
}

// Subscription — подписка автора с периодами.
type Subscription struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Currency string   `json:"currency"`
	Periods  []Period `json:"periods"`
}

// rawPeriod и rawSubscription принимают оба написания идентификаторов:
// в спеке REST — camelCase (subscriptionId, periodId), в вебхуках — snake_case.
type rawPeriod struct {
	PeriodID  int64  `json:"periodId"`
	PeriodID2 int64  `json:"period_id"`
	ID        int64  `json:"id"`
	Period    string `json:"period"`
}

type rawSubscription struct {
	SubscriptionID  int64       `json:"subscriptionId"`
	SubscriptionID2 int64       `json:"subscription_id"`
	ID              int64       `json:"id"`
	Name            string      `json:"name"`
	Currency        string      `json:"currency"`
	Periods         []rawPeriod `json:"periods"`
}

func firstNonZero(v ...int64) int64 {
	for _, x := range v {
		if x != 0 {
			return x
		}
	}
	return 0
}

// ListSubscriptions возвращает подписки автора. Ключ уходит заголовком
// Api-Key; в текст ошибки он не попадает.
func ListSubscriptions(ctx context.Context, apiKey string) ([]Subscription, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("не задан API-ключ Tribute")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/subscriptions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Api-Key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("нет связи с API Tribute: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("ответ API Tribute не прочитан: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("ключ отклонён API Tribute (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("ошибка API Tribute: HTTP %d", resp.StatusCode)
	}
	return parseSubscriptions(body)
}

// parseSubscriptions разбирает ответ: массив в поле result (как в спеке),
// в поле subscriptions/data или голым массивом.
func parseSubscriptions(body []byte) ([]Subscription, error) {
	var list []rawSubscription
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("ответ API Tribute не разобран: %w", err)
		}
	} else {
		var wrap struct {
			Result        []rawSubscription `json:"result"`
			Subscriptions []rawSubscription `json:"subscriptions"`
			Data          []rawSubscription `json:"data"`
			Error         json.RawMessage   `json:"error"`
			Message       string            `json:"message"`
		}
		if err := json.Unmarshal(body, &wrap); err != nil {
			return nil, fmt.Errorf("ответ API Tribute не разобран: %w", err)
		}
		switch {
		case wrap.Result != nil:
			list = wrap.Result
		case wrap.Subscriptions != nil:
			list = wrap.Subscriptions
		case wrap.Data != nil:
			list = wrap.Data
		default:
			// Ни одного списка в ответе — это не «подписок нет», а отказ.
			msg := strings.TrimSpace(wrap.Message)
			if msg == "" && len(wrap.Error) > 0 {
				msg = strings.Trim(strings.TrimSpace(string(wrap.Error)), `"`)
			}
			if msg == "" {
				msg = "в ответе нет списка подписок"
			}
			if r := []rune(msg); len(r) > 200 {
				msg = string(r[:200])
			}
			return nil, fmt.Errorf("API Tribute: %s", msg)
		}
	}
	out := make([]Subscription, 0, len(list))
	for _, r := range list {
		s := Subscription{
			ID:       firstNonZero(r.SubscriptionID, r.SubscriptionID2, r.ID),
			Name:     strings.TrimSpace(r.Name),
			Currency: r.Currency,
		}
		if s.ID <= 0 {
			continue
		}
		for _, p := range r.Periods {
			s.Periods = append(s.Periods, Period{
				ID:     firstNonZero(p.PeriodID, p.PeriodID2, p.ID),
				Period: strings.ToLower(strings.TrimSpace(p.Period)),
			})
		}
		out = append(out, s)
	}
	return out, nil
}
