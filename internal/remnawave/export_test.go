package remnawave

import "context"

// Subscription — ссылка и срок подписки одним вызовом; нужен только тестам.
func (c *Client) Subscription(ctx context.Context, telegramID int64) (string, string, bool) {
	u, err := c.findByTelegram(ctx, telegramID)
	if err != nil || u == nil || u.SubscriptionURL == "" {
		return "", "", false
	}
	return u.SubscriptionURL, u.ExpireAt, true
}
