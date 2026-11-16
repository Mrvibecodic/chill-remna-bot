package app

import "context"

// Короткие входы, которыми пользуются только тесты.

// waitMail дожидается фоновых отправок: без него проверка идёт наперегонки с
// письмом, которое ушло в фон при регистрации.
func (a *App) waitMail() { a.mailWG.Wait() }

// planCountries — страны и число конфигов «Базового» на срок.
func (a *App) planCountries(ctx context.Context, months int) (countries []country, inbounds int) {
	g := a.squadGeo(ctx, a.planSquadUUIDs(months))
	return g.countries, g.inbounds
}

// prepareP2PCard — заявка на перевод по «Базовому» на срок.
func (a *App) prepareP2PCard(ctx context.Context, chatID int64, months int) (card, price string, reqID int64, err error) {
	return a.prepareP2PCardSale(ctx, chatID, baseSale(months))
}

// buyMonths — выбранный срок в месяцах (0, если выбора нет или он недоступен).
func (a *App) buyMonths(ctx context.Context, chatID int64) int {
	in, err := a.buyIntent(ctx, chatID)
	if err != nil || in == nil {
		return 0
	}
	return in.Months
}
