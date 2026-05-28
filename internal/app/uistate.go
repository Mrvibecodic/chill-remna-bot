package app

// uiState — рантайм-состояние меню/покупки/админки по chatID (вне мастера
// установки). Не персистируется: при перезапуске бота сбрасывается.
//
// Поля сгруппированы:
//   - покупка/P2P-флоу пользователя;
//   - админский текстовый ввод (одно поле adminInput + при необходимости
//     дополнительный priceMonths для контекста);
//   - редакторы (баннер главной, баннеры разделов, эмодзи);
//   - id сообщений для последующего удаления (single-message UI / cleanup).
type uiState struct {
	// --- покупка / P2P пользователя ---
	buyMonths    int   // выбранный срок плана
	awaitShotReq int64 // id заявки P2P, по которой ждём скриншот
	rejectReq    int64 // id заявки, для которой админ вводит причину отказа

	// --- админский текстовый ввод ---
	adminInput  string // ожидаемый ввод админа: "cards"|"price"|"squad"|...
	priceMonths int    // при adminInput=="price" — для какого срока

	// --- редакторы ---
	welcomeAwait       string // для главного баннера: "img"|"txt"
	awaitSectionBanner string // ждём фото для раздела (ключ assets.Section*)
	awaitEmojiFor      string // ожидаем premium-эмодзи для этой стандартной
	// inputBack — callback родителя (например, "menu:pay" или "prc:base"),
	// чтобы кнопка «◀️ Отмена» на ask-форме могла вернуть админа точно туда.
	inputBack string

	// --- id сообщений для cleanup ---
	// «Скриншот получен…» и сам скриншот — удаляются у юзера после решения
	// админа по заявке (чтобы не висели в чате).
	p2pSubmitMsgID int
	p2pShotMsgID   int
}

// getUI возвращает (или создаёт) uiState для chatID. Потокобезопасен.
func (a *App) getUI(chatID int64) *uiState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ui == nil {
		a.ui = map[int64]*uiState{}
	}
	st := a.ui[chatID]
	if st == nil {
		st = &uiState{}
		a.ui[chatID] = st
	}
	return st
}
