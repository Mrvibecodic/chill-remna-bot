package app

import (
	"context"
	"regexp"
	"strings"

	"github.com/go-telegram/bot/models"
)

// Экраны пользователя уходят с запретом пересылки (protect_content), если в
// них нечего копировать. Запрет снимает и копирование текста, поэтому
// открытыми остаются экраны со ссылками, реквизитами, кодом обращения и
// любым текстом, похожим на то, что нужно скопировать.

// copyableTagRe — разметка, которую ставят ради копирования или перехода.
var copyableTagRe = regexp.MustCompile(`(?i)<code|<pre|<a\s`)

// copyableRe — видимый текст, который человек может захотеть скопировать:
// ссылки, адреса, @ники, номера карт и телефонов (в том числе с пробелами),
// длинные ключи.
var copyableRe = regexp.MustCompile(`(?i)https?://|tg://|t\.me/|@[a-z0-9_]{3,}|\d{6,}|\+?\d[\d \-()]{8,}\d|[a-z0-9-]+\.[a-z]{2,}\b|[a-z0-9_-]{20,}`)

// copyableText — есть ли в экране (HTML) что копировать. Теги разбираются
// отдельно: в премиум-эмодзи длинный числовой id, он не текст.
func copyableText(s string) bool {
	return copyableTagRe.MatchString(s) || copyableRe.MatchString(stripHTMLTags(s))
}

// copyableEnts — то же для текста с entities (приветствие, набранное админом
// в Telegram).
func copyableEnts(text string, ents []models.MessageEntity) bool {
	for _, e := range ents {
		switch e.Type {
		case models.MessageEntityTypeCode, models.MessageEntityTypePre, models.MessageEntityTypeURL,
			models.MessageEntityTypeTextLink, models.MessageEntityTypeMention, models.MessageEntityTypeEmail,
			models.MessageEntityTypePhoneNumber:
			return true
		}
	}
	return copyableText(text)
}

// screenProtect — закрыть ли экран от пересылки. Экраны пользователя — все,
// где нечего копировать; экраны админки — только помеченные withAdminClosed.
func (a *App) screenProtect(ctx context.Context, chatID int64, copyable bool) bool {
	if ctx.Value(keepCopyKey{}) != nil || !hasChat(chatID) || a.cfg == nil {
		return false
	}
	if chatID == a.cfg.AdminID {
		return ctx.Value(adminClosedKey{}) != nil
	}
	return !copyable
}

type (
	protectKey     struct{}
	keepCopyKey    struct{}
	adminClosedKey struct{}
)

// withAdminClosed — экран админки, закрытый от пересылки. Экраны админки
// закрываются по одному, после проверки, что копировать там нечего: у админа
// это реквизиты, ID и тексты. Детектор здесь не спрашивается — в сводках
// бывают суммы, похожие для него на номера.
func withAdminClosed(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminClosedKey{}, true)
}

type flowKey struct{}

// withClientFlow — админ нажал кнопку покупателя (витрина, оплата, свои
// подписки): экран строится так же, как у покупателя.
func withClientFlow(ctx context.Context) context.Context {
	return context.WithValue(ctx, flowKey{}, "client")
}

// withAdminFlow — нажатие или команда админки. Без пометки (фоновая работа,
// ответ на вопрос) экран остаётся того же вида, что и текущий.
func withAdminFlow(ctx context.Context) context.Context {
	return context.WithValue(ctx, flowKey{}, "admin")
}

func flowOf(ctx context.Context) string {
	s, _ := ctx.Value(flowKey{}).(string)
	return s
}

// clientData — кнопка ведёт на экран покупателя.
func clientData(data string) bool {
	key, val, _ := strings.Cut(data, ":")
	switch key {
	case cbBuy, cbPlanBuy, cbPlanView, cbPlanBack, cbMethod, cbTop, cbP2P, cbYKCheck, cbAutoPay,
		cbCBCheck, cbPLCheck, cbYMCheck, cbHLCheck, cbDevices, cbTerms, cbTraffic:
		return true
	case cbPromo:
		return promoUserData(val)
	case cbMenu:
		switch val {
		case "buy", "renew", "topup", "balance", "ref", "promo", "mysubs", "autopay", "register":
			return true
		}
	}
	return false
}

// withKeepCopy — экран в этом контексте не закрывать, даже если детектор не
// нашёл в нём ничего для копирования (текст документов).
func withKeepCopy(ctx context.Context) context.Context {
	return context.WithValue(ctx, keepCopyKey{}, true)
}

// withProtect — отправка в этом контексте уходит с запретом пересылки
// (его читает botMessenger). Через контекст, а не через состояние чата: иначе
// уведомление, ушедшее в тот же чат в ту же секунду, закрылось бы тоже.
func withProtect(ctx context.Context, on bool) context.Context {
	if !on {
		return ctx
	}
	return context.WithValue(ctx, protectKey{}, true)
}

// adminClosedData — нажатие ведёт в раздел админки, закрытый от пересылки
// целиком, со всеми вложенными экранами: тарифы и триал (тарифы, цены,
// быстрая настройка, триал, сквады, доп-подписка) и сводка ошибок оплат.
// Копировать там нечего. «Система» открыта целиком, с обновлением и бэкапом:
// у раздела и его экранов один режим, иначе переходы не правятся на месте.
func adminClosedData(data string) bool {
	switch data {
	case "menu:plans", "menu:pricing", "menu:trial", "menu:squads", "menu:addsub":
		return true
	}
	for _, p := range []string{"pln:", "plq:", "prc:", "trial:", "sqd:", "addsub:", "pay:err"} {
		if strings.HasPrefix(data, p) {
			return true
		}
	}
	return false
}

// adminClosedInput — ответ админа на вопрос из тех же разделов.
func adminClosedInput(ui *uiState) bool {
	in := ui.adminInput
	switch {
	case strings.HasPrefix(in, "plan_"), strings.HasPrefix(in, "trial_"), strings.HasPrefix(in, "addsub_"):
		return true
	case in == "price", in == "ykprice", in == "starprice":
		// Те же вопросы задают экраны P2P, ЮKassa и Stars — там тариф не
		// выбран, и экран способа оплаты остаётся открытым.
		return ui.planCode != ""
	}
	switch in {
	case "baseprice", "traffic_gb", "device_per", "device_limit", "currency":
		return true
	}
	return false
}

func protectedCtx(ctx context.Context) bool {
	on, _ := ctx.Value(protectKey{}).(bool)
	return on
}

// markEditProt — сообщение, под которым нажали кнопку, закрыто от пересылки.
func (a *App) markEditProt(chatID int64) {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	if a.editProt == nil {
		a.editProt = map[int64]bool{}
	}
	a.editProt[chatID] = true
}

// editMatches — можно ли править сообщение, под которым нажали кнопку.
// Запрет пересылки задаётся только при отправке, поэтому экран с другим
// режимом уходит новым сообщением.
func (a *App) editMatches(chatID int64, prot bool) bool {
	a.scrMu.Lock()
	defer a.scrMu.Unlock()
	return a.editTarget[chatID] == 0 || a.editProt[chatID] == prot
}
