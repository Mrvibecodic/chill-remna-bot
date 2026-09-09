package app

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"remnabot/internal/i18n"

	"remnabot/internal/remnawave"
)

// Привязка Telegram к аккаунту, заведённому в кабинете по почте.
//
// Почему аккаунт ПЕРЕЕЗЖАЕТ на телеграмный идентификатор, а не обзаводится
// вторым. У бота одна личность — телеграмный идентификатор: им же он адресует
// сообщения, по нему хранит подписку, баланс и историю, и по нему же находит
// учётку в панели. Аккаунт кабинета живёт на синтетическом ОТРИЦАТЕЛЬНОМ
// идентификаторе — приёме, который позволяет вписать «человека без Telegram» в
// ту же систему. Оставить оба значит развести человека надвое: в чате он видел
// бы пустой аккаунт, а купленное — только в браузере.
//
// Почему ссылка подписки при этом не меняется. Панель НЕ умеет переименование
// пользователя, а имя нашей учётки выведено из идентификатора. Переезд имени =
// создать нового и удалить старого, то есть выдать новую ссылку и заставить
// человека переподключать приложение. Вместо этого учётке в панели
// проставляется telegramId: владение определяется и по нему, и по имени, так
// что бот находит ту же самую учётку, а ссылка, ключи и остаток трафика
// остаются прежними.
//
// Почему занятый Telegram — отказ. Слияние двух историй покупок автоматически
// не делается: складывать балансы и выбирать «более длинную» подписку значит
// решать за человека судьбу оплаченного, и ошибка стоит ему дней.

var (
	errBindOnlyEmail = errors.New("привязка доступна аккаунту, заведённому по почте")
	errBindBusy      = errors.New("у этого Telegram уже есть аккаунт в боте — напишите в поддержку")
	errBindDenied    = errors.New("доступ к боту для этого Telegram ограничен")
	errBindPanel     = errors.New("панель недоступна, попробуйте позже")
)

// bindMu делает привязку строго последовательной. Операция редкая, а две
// одновременные умеют переехать на один и тот же идентификатор.
var bindMu sync.Mutex

// CabinetBindTelegram переносит аккаунт кабинета на телеграмный идентификатор
// и возвращает его.
func (a *App) CabinetBindTelegram(ctx context.Context, tgID, newTgID int64) (int64, error) {
	if !a.CabinetEnabled() {
		return 0, errCabinetOff
	}
	if a.store == nil {
		return 0, errors.New("хранилище недоступно")
	}
	if tgID >= 0 {
		return 0, errBindOnlyEmail
	}
	if newTgID <= 0 || newTgID == tgID {
		return 0, errBindBusy
	}
	if a.webAccount(ctx, tgID) == nil {
		return 0, errBindOnlyEmail
	}

	bindMu.Lock()
	defer bindMu.Unlock()

	// Аккаунт мог переехать, пока запрос ждал замка.
	if u, _ := a.store.GetUser(ctx, tgID); u == nil {
		return 0, errBindOnlyEmail
	} else if u.Blocked {
		return 0, errors.New("доступ заблокирован")
	}
	// Закрытый бот (приглашения/вайтлист): привязка к Telegram, которого бот не
	// пускает, заперла бы человека снаружи вместе с его оплаченной подпиской.
	if a.MiniAccessDenied(ctx, newTgID) {
		return 0, errBindDenied
	}
	if u, _ := a.store.GetUser(ctx, newTgID); u != nil && u.Blocked {
		return 0, errBindDenied
	}

	fp, err := a.store.AccountFootprint(ctx, newTgID)
	if err != nil {
		return 0, errors.New("не удалось проверить Telegram")
	}
	if fp.Active {
		return 0, errBindBusy
	}

	panel := a.panelClient()
	var ref *remnawave.UserRef
	if panel != nil {
		// Занятость в панели проверяется ДО переноса и по обоим признакам,
		// которыми бот опознаёт свои учётки: telegramId и имя.
		busy, err := panel.FindByTelegramID(ctx, newTgID)
		if err != nil {
			return 0, errBindPanel
		}
		if busy != nil {
			return 0, errBindBusy
		}
		mine, err := panel.FindByTelegramID(ctx, tgID)
		if err != nil {
			return 0, errBindPanel
		}
		if mine != nil {
			// Ошибка проставления telegramId — отказ до всякой правки базы:
			// перенести аккаунт и оставить подписку на старом имени значит
			// потерять её для бота целиком.
			if err := panel.LinkTelegramID(ctx, mine.Ref, newTgID, false); err != nil {
				a.log.Warn("привязка Telegram: панель не приняла telegramId", "err", err)
				return 0, errBindPanel
			}
			r := mine.Ref
			ref = &r
		}
	}

	if err := a.store.MoveAccount(ctx, tgID, newTgID); err != nil {
		a.log.Error("привязка Telegram: перенос аккаунта не удался", "err", err)
		// Компенсация обязательна. Учётка в панели уже помечена чужим
		// telegramId, а аккаунт остался на старом идентификаторе: без отката
		// первый же вход этого Telegram в бота подцепил бы чужую подписку
		// самостоятельной сверкой с панелью.
		if ref != nil && panel != nil {
			if cerr := panel.LinkTelegramID(ctx, *ref, 0, false); cerr != nil {
				a.log.Error("привязка Telegram: откат telegramId не удался — учётка в панели помечена чужим Telegram",
					"panel_ref", ref.Key(), "err", cerr)
				a.notifyAdminBindRollback(ctx, ref.Key(), newTgID)
			}
		}
		return 0, errors.New("не удалось привязать Telegram, попробуйте позже")
	}

	// Кэши, ключом которых был прежний идентификатор.
	a.epochMu.Lock()
	delete(a.epochs, tgID)
	a.epochMu.Unlock()
	cabNotifyMu.Lock()
	delete(cabNotified, tgID)
	delete(cabNotified, newTgID)
	cabNotifyMu.Unlock()
	a.invalidateSubCache(tgID)
	a.invalidateSubCache(newTgID)
	a.mu.Lock()
	delete(a.ui, tgID)
	a.mu.Unlock()

	// Аккаунт уже был пущен в кабинет — привязка не повод отправлять его в
	// очередь на одобрение заново. Без этой строки режим модерации «только
	// Telegram-вход» отбирал бы доступ ровно в момент привязки: до неё аккаунт
	// считался почтовым и модерации не подлежал, после — стал телеграмным.
	if a.cabinetNeedsApproval(false) {
		if err := a.store.SetWebApproved(ctx, newTgID, true); err != nil {
			a.log.Warn("привязка Telegram: одобрение не сохранено", "err", err)
		}
	}

	// Пропуска прежнего идентификатора выдавались на аккаунт, которого больше
	// нет; у нового поднимаем поколение, чтобы ни один старый пропуск не
	// оказался годным после переезда.
	a.bumpEpoch(ctx, newTgID)
	a.log.Info("кабинет: Telegram привязан", "tg_id", newTgID)
	return newTgID, nil
}

// CabinetBindName записывает имя и @username из подписи виджета входа: без них
// привязанный аккаунт остаётся в списке пользователей безымянной строкой.
func (a *App) CabinetBindName(ctx context.Context, tgID int64, username, firstName string) {
	a.rememberUser(ctx, tgID, username, firstName)
}

// notifyAdminBindRollback зовётся, когда откат не удался: учётка в панели
// осталась помеченной чужим Telegram, и разобрать это может только человек.
func (a *App) notifyAdminBindRollback(ctx context.Context, panelRef string, tgID int64) {
	lang := a.lang(a.cfg.AdminID)
	a.notify(ctx, a.cfg.AdminID, i18n.T(lang, "cabinet.bind_rollback", escapeName(panelRef), strconv.FormatInt(tgID, 10)))
}
