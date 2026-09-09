package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultAPIURL — приёмник по умолчанию. Совместимые шлюзы отличаются только
// адресом, поэтому он вынесен в настройки.
const DefaultAPIURL = "https://api.resend.com/emails"

// sendAPI отправляет письмо через HTTPS-шлюз.
//
// Нужен там, где хостер закрыл исходящие почтовые порты: HTTPS остаётся
// единственным способом отдать письмо наружу. Формат тела — простейший из
// распространённых: from/to/subject/text/html.
func sendAPI(ctx context.Context, cfg Config, msg Message) error {
	url := cfg.APIURL
	if url == "" {
		url = DefaultAPIURL
	}
	if !strings.HasPrefix(url, "https://") {
		// Ключ шлюза уходит в заголовке: по открытому HTTP его отдавать нельзя.
		return fmt.Errorf("адрес шлюза должен начинаться с https://")
	}
	from := cfg.From
	if cfg.FromName != "" {
		from = cfg.FromName + " <" + cfg.From + ">"
	}
	body := map[string]any{
		"from":    from,
		"to":      []string{msg.To},
		"subject": msg.Subject,
	}
	if msg.Text != "" {
		body["text"] = msg.Text
	}
	if msg.HTML != "" {
		body["html"] = msg.HTML
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("почтовый шлюз недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Тело ответа читаем ограниченно и кладём в ошибку: без него админ
		// видит голый код и не понимает, что именно не так с ключом или
		// доменом отправителя.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("почтовый шлюз ответил %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}
