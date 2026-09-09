package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"remnabot/internal/mailer"
	"remnabot/internal/model"
	"remnabot/internal/web"
)

// Аккаунт кабинета: подтверждение почты, смена и восстановление пароля,
// поколение пропусков.

const minCabinetPassword = 8

// longEnoughPassword считает СИМВОЛЫ, а не байты. Байтовая длина принимала
// «пароль» из четырёх кириллических букв: в UTF-8 их восемь байт, и порог
// проходил, хотя перебрать такое — дело секунд.
func longEnoughPassword(p string) bool {
	return utf8.RuneCountInString(p) >= minCabinetPassword
}

var (
	errNotEmailAccount = errors.New("у аккаунта нет входа по паролю")
	errBadLink         = errors.New("ссылка недействительна или устарела")
	errShortPassword   = errors.New("пароль слишком короткий (мин. 8 символов)")
	errSamePassword    = errors.New("новый пароль совпадает со старым")
	errWrongPassword   = errors.New("текущий пароль неверен")
)

// webAccount — кабинетная учётка по идентификатору, какого бы знака он ни был.
//
// Знак раньше был признаком «это почтовый аккаунт», но после привязки Telegram
// у аккаунта положительный идентификатор И почта одновременно. Проверка знака в
// таких местах молча теряла бы почту: пропадали бы и подпись в карточке
// пользователя, и допуск к тарифам по списку.
func (a *App) webAccount(ctx context.Context, tgID int64) *model.WebUser {
	if a.store == nil || tgID == 0 {
		return nil
	}
	wu, _ := a.store.GetWebUserByTgID(ctx, tgID)
	return wu
}

// emailVerified — почта аккаунта подтверждена. Аккаунт без почты сюда не
// относится: подтверждать нечего, и ограничивать его не за что.
func (a *App) emailVerified(ctx context.Context, tgID int64) bool {
	wu := a.webAccount(ctx, tgID)
	return wu == nil || wu.VerifiedAt != ""
}

// CabinetMoneyBlocked — аккаунту закрыты платные действия.
//
// Закрыты они ровно одному виду аккаунтов: тем, у кого личность подтверждена
// НИЧЕМ, кроме введённого при регистрации адреса (синтетический отрицательный
// идентификатор). Привязавший Telegram доказал личность подписью Telegram, и
// держать его на голодном пайке из-за неподтверждённого адреса незачем — почта
// у него после привязки лишь один из способов входа.
//
// Проверка нужна потому, что почта не косметика: по ней сопоставляются списки
// допущенных к тарифам, и без подтверждения чужой адрес присваивается обычной
// регистрацией.
func (a *App) CabinetMoneyBlocked(ctx context.Context, tgID int64) bool {
	if tgID >= 0 || !a.CabinetEnabled() {
		return false
	}
	// Почта не настроена — подтвердить нечем, и запирать людям оплату за то,
	// чего им не предложили, нельзя.
	if !a.MailReady() {
		return false
	}
	return !a.emailVerified(ctx, tgID)
}

// SessionEpoch — поколение пропусков аккаунта.
func (a *App) SessionEpoch(ctx context.Context, tgID int64) int {
	a.epochMu.RLock()
	v, ok := a.epochs[tgID]
	a.epochMu.RUnlock()
	if ok {
		return v
	}
	if a.store == nil {
		return 0
	}
	n, err := a.store.UserSessEpoch(ctx, tgID)
	if err != nil {
		// Ошибка чтения не должна выбрасывать всех из кабинета: отдаём то, что
		// стоит в пропуске у новичка, и пробуем снова на следующем запросе.
		a.log.Warn("кабинет: поколение пропусков не прочитано", "err", err)
		return 0
	}
	a.rememberEpoch(tgID, n)
	return n
}

func (a *App) rememberEpoch(tgID int64, n int) {
	a.epochMu.Lock()
	if a.epochs == nil {
		a.epochs = map[int64]int{}
	}
	// Карта живёт весь срок работы процесса. Верхняя граница нужна только на
	// случай базы с сотнями тысяч аккаунтов: сброс безопасен, значения
	// перечитаются из базы.
	if len(a.epochs) > 50000 {
		a.epochs = map[int64]int{}
	}
	a.epochs[tgID] = n
	a.epochMu.Unlock()
}

// bumpEpoch выбивает все прежние пропуска аккаунта.
func (a *App) bumpEpoch(ctx context.Context, tgID int64) {
	if a.store == nil {
		return
	}
	n, err := a.store.BumpSessEpoch(ctx, tgID)
	if err != nil {
		a.log.Warn("кабинет: поколение пропусков не поднято", "err", err)
		return
	}
	a.rememberEpoch(tgID, n)
}

// CabinetAccount — состояние аккаунта для экрана «Аккаунт».
func (a *App) CabinetAccount(ctx context.Context, tgID int64) web.CabinetAccountDTO {
	dto := web.CabinetAccountDTO{TgID: tgID, MailOn: a.MailReady()}
	if wu := a.webAccount(ctx, tgID); wu != nil {
		dto.Email = wu.Email
		dto.EmailVerified = wu.VerifiedAt != ""
		dto.HasPassword = true
	}
	dto.TgLinked = tgID > 0
	dto.MoneyBlocked = a.CabinetMoneyBlocked(ctx, tgID)
	if a.store != nil {
		if u, _ := a.store.GetUser(ctx, tgID); u != nil {
			dto.Name = displayName(u.FirstName, u.Username)
		}
	}
	return dto
}

// CabinetSendVerify отправляет письмо с подтверждением ещё раз.
func (a *App) CabinetSendVerify(ctx context.Context, tgID int64) error {
	wu := a.webAccount(ctx, tgID)
	if wu == nil {
		return errNotEmailAccount
	}
	if wu.VerifiedAt != "" {
		return nil
	}
	return a.sendVerifyMail(ctx, tgID, wu.Email)
}

// CabinetVerifyEmail гасит ссылку из письма и помечает адрес подтверждённым.
//
// Пропуск здесь не выдаётся намеренно: ссылку открывают в почтовом клиенте, на
// чужом устройстве и через пересылку, и превращать её во вход значило бы
// раздавать доступ всякому, кто увидел письмо.
func (a *App) CabinetVerifyEmail(ctx context.Context, token string) error {
	if a.store == nil || token == "" {
		return errBadLink
	}
	t, err := a.store.TakeEmailToken(ctx, emailTokenHash(token), model.EmailPurposeVerify)
	if err != nil || t == nil {
		return errBadLink
	}
	wu := a.webAccount(ctx, t.TgID)
	// Адрес мог смениться (например, аккаунт переехал на телеграмный
	// идентификатор и почту забрал сброс пароля) — подтверждаем только то, что
	// подтверждали.
	if wu == nil || !strings.EqualFold(wu.Email, t.Email) {
		return errBadLink
	}
	if wu.VerifiedAt != "" {
		return nil
	}
	return a.store.SetWebUserVerified(ctx, t.TgID, "")
}

// CabinetChangePassword меняет пароль по старому.
//
// Старый пароль обязателен: пропуск живёт семь суток, и без него укравший
// пропуск менял бы пароль и запирал хозяина снаружи.
func (a *App) CabinetChangePassword(ctx context.Context, tgID int64, oldPass, newPass string) error {
	wu := a.webAccount(ctx, tgID)
	if wu == nil {
		return errNotEmailAccount
	}
	if !longEnoughPassword(newPass) {
		return errShortPassword
	}
	if bcrypt.CompareHashAndPassword([]byte(wu.PassHash), []byte(oldPass)) != nil {
		return errWrongPassword
	}
	if oldPass == newPass {
		return errSamePassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := a.store.SetWebUserPassword(ctx, tgID, string(hash)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errNotEmailAccount
		}
		return err
	}
	// Прочие сессии выбиваются ДО ответа: смена пароля затем и делается, что
	// доступ у кого-то ещё считают чужим.
	a.bumpEpoch(ctx, tgID)
	a.notifyPasswordChanged(ctx, wu.Email)
	return nil
}

// CabinetForgotPassword отправляет ссылку на смену забытого пароля.
//
// Наружу всегда уходит один и тот же ответ: иначе форма превращается в
// проверялку «есть ли у вас аккаунт с таким адресом».
func (a *App) CabinetForgotPassword(ctx context.Context, email string) {
	if a.store == nil || !a.MailReady() {
		return
	}
	email = normEmail(email)
	if !mailer.ValidAddress(email) {
		return
	}
	wu, _ := a.store.GetWebUserByEmail(ctx, email)
	if wu == nil {
		return
	}
	if err := a.sendResetMail(ctx, wu.TgID, wu.Email); err != nil {
		a.log.Warn("кабинет: письмо о сбросе пароля не отправлено", "err", err)
	}
}

// CabinetResetPassword ставит новый пароль по ссылке из письма и возвращает
// идентификатор аккаунта.
//
// Успешный сброс ЗАОДНО подтверждает адрес: человек только что доказал, что
// ящик его. Это же чинит и обратный случай — аккаунт, заведённый на чужой
// адрес: настоящий владелец забирает его себе.
func (a *App) CabinetResetPassword(ctx context.Context, token, newPass string) (int64, error) {
	if a.store == nil || token == "" {
		return 0, errBadLink
	}
	if !longEnoughPassword(newPass) {
		return 0, errShortPassword
	}
	t, err := a.store.TakeEmailToken(ctx, emailTokenHash(token), model.EmailPurposeReset)
	if err != nil || t == nil {
		return 0, errBadLink
	}
	wu := a.webAccount(ctx, t.TgID)
	if wu == nil || !strings.EqualFold(wu.Email, t.Email) {
		return 0, errBadLink
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPass), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	if err := a.store.SetWebUserPassword(ctx, t.TgID, string(hash)); err != nil {
		return 0, err
	}
	if wu.VerifiedAt == "" {
		_ = a.store.SetWebUserVerified(ctx, t.TgID, "")
	}
	a.bumpEpoch(ctx, t.TgID)
	a.notifyPasswordChanged(ctx, wu.Email)
	return t.TgID, nil
}
