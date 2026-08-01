package app

type uiState struct {
	buyMonths    int
	topUpKopecks int64
	awaitTopUp   bool
	awaitPromo   bool
	awaitShotReq int64
	rejectReq    int64

	adminInput  string
	priceMonths int
	linkUID     int64

	panelSyncDone bool

	welcomeAwait       string
	awaitSectionBanner string
	awaitEmojiFor      string

	inputBack string

	broadcastText string

	p2pSubmitMsgID int
	p2pShotMsgID   int

	// Мастер переезда с remnashop: ждём файл дампа.
	awaitRSDump bool
}

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
