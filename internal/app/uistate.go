package app

import "remnabot/internal/model"

type uiState struct {
	topUpKopecks int64
	awaitTopUp   bool
	awaitPromo   bool
	awaitShotReq int64
	rejectReq    int64

	adminInput  string
	priceMonths int
	linkUID     int64

	// planCode — тариф, поле которого админ вводит прямо сейчас. В
	// callback-данные ответа код не положить, а «тот тариф, что открыт» — не
	// ответ: пока ждём ввод, админ может открыть другой.
	planCode string
	// planEdit — тариф, чей редактор цен открыт. Нужен тумблерам сквадов: в их
	// callback-данные код тарифа не влезает рядом с UUID, поэтому код живёт
	// здесь, а callback несёт его отпечаток (см. planEditHash).
	planEdit string
	// plansPage — страница списка тарифов, на которой админ был последний раз.
	// Без неё любое действие возвращало бы его на первую страницу, и протащить
	// тариф вверх с третьей страницы значило бы листать заново после каждого
	// нажатия.
	plansPage int

	// Мастер создания приглашения: срок жизни в днях (шаг 1 из 2).
	inviteDays int

	panelSyncDone bool

	welcomeAwait       string
	awaitSectionBanner string
	awaitEmojiFor      string
	// torAwait — админ вводит текст сообщения о снятии торрент-блокировки
	// (сохраняется вместе с entities, поэтому не через adminInput).
	torAwait bool

	inputBack string

	broadcastText string

	p2pSubmitMsgID int
	p2pShotMsgID   int

	// Мастер переезда с remnashop: ждём файл дампа.
	awaitRSDump bool

	// Импорт тарифа: ждём JSON-файл; разобранный тариф держится здесь до
	// подтверждения (pln:impok).
	awaitPlanImport  bool
	planImport       *model.Plan
	planImportAccess []model.PlanAccess
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
