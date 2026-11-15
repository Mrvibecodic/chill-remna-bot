package app

import (
	"context"
	"strconv"
	"testing"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
)

// tgMsg — fakeMsg, который правит сообщение так же, как Telegram: текст —
// только у текстовых, подпись — только у сообщений с картинкой.
type tgMsg struct {
	*fakeMsg
	photo map[int]bool
}

func (m *tgMsg) SendPhotoCacheable(ctx context.Context, chat int64, c string, b []byte, cap string, rows [][]models.InlineKeyboardButton) (int, string) {
	id, f := m.fakeMsg.SendPhotoCacheable(ctx, chat, c, b, cap, rows)
	m.photo[id] = true
	return id, f
}
func (m *tgMsg) SendBanner(ctx context.Context, chat int64, p models.InputFile, cap string, e []models.MessageEntity, rm models.ReplyMarkup) (int, error) {
	id, err := m.fakeMsg.SendBanner(ctx, chat, p, cap, e, rm)
	m.photo[id] = true
	return id, err
}
func (m *tgMsg) EditText(ctx context.Context, chat int64, id int, text string, rows [][]models.InlineKeyboardButton) bool {
	if m.photo[id] {
		m.fakeMsg.noteEdit(-1)
		return false
	}
	ok := m.fakeMsg.EditText(ctx, chat, id, text, rows)
	if ok {
		m.fakeMsg.recordKB(rows)
	}
	return ok
}
func (m *tgMsg) EditCaption(ctx context.Context, chat int64, id int, cap string, rows [][]models.InlineKeyboardButton) bool {
	if !m.photo[id] {
		return false
	}
	ok := m.fakeMsg.EditCaption(ctx, chat, id, cap, rows)
	if ok {
		m.fakeMsg.recordKB(rows)
	}
	return ok
}

func editApp(t *testing.T) (*App, *fakeMsg) {
	a, fm, _ := newTextsApp(t)
	fm.editOK = true
	a.msg = &tgMsg{fakeMsg: fm, photo: map[int]bool{}}
	return a, fm
}

// Переход между экраном раздела и его экранами правит сообщение на месте, а
// не присылает новое: у раздела и его экранов один режим пересылки и одна
// картинка. Новое сообщение — только на входе в подраздел с другим режимом.
func TestAdminNavigationEditsInPlace(t *testing.T) {
	ctx := context.Background()
	id := strconv.FormatInt(protUser, 10)
	clusters := map[string][]string{
		"menu:pay":              {"menu:payments", "menu:analytics", "menu:payplans"},
		"menu:paymethods":       {"menu:p2p", "menu:stars", "menu:yookassa", "menu:cryptobot", "menu:platega", "menu:heleket", "menu:tribute", "menu:yoomoney", "menu:wallet", "menu:moynalog"},
		"menu:payplans":         {"menu:plans", "menu:pricing", "menu:trial", "menu:squads", "menu:addsub"},
		"menu:system":           {"menu:status", "menu:updates", "menu:backup", "menu:apilog", "menu:panelauth", "menu:webhooks", "menu:mail", "menu:miniapp", "menu:cabinet", "menu:rsimp", "menu:reconf"},
		"menu:iface":            {"tx:pm", "menu:welcome", "menu:welcome_sections", "menu:contacts", "tx:home", "menu:devices", "menu:webui", "menu:botlang"},
		"menu:payments":         {"pay:card:5:0", "pay:err"},
		"menu:updates":          {"upd:chan"},
		"menu:backup":           {"bk:dest"},
		"usr:view:" + id:        {"usr:pays:" + id + ":0", "usr:plans:" + id},
		"usr:pays:" + id + ":0": {"usr:pay:5:0"},
	}
	for hub, kids := range clusters {
		for _, kid := range kids {
			for _, p := range [][2]string{{hub, kid}, {kid, hub}} {
				a, fm := editApp(t)
				_ = a.store.UpsertUser(ctx, protUser)
				_ = a.store.AddPayment(ctx, &model.Payment{ID: 5, TelegramID: protUser, Method: model.PayMethodP2P, Months: 1,
					Amount: "199 ₽", Status: model.PaymentPaid, CreatedAt: "2026-11-01T10:00:00Z", ExtID: "p2p:1"})
				a.handleCallback(ctx, cb(100, p[0]))
				msg := fm.seq
				a.handleCallback(ctx, cbUnder(100, p[1], msg, fm.isProt(msg)))
				if fm.seq != msg {
					t.Errorf("%s → %s: новое сообщение вместо правки", p[0], p[1])
				}
			}
		}
	}
}

// Вопрос «введите значение» правит экран с картинкой (подписью), ответ на
// него — тоже, «Отмена» возвращает на экран правкой и в том же режиме
// пересылки.
func TestAdminAskAndAnswerEditInPlace(t *testing.T) {
	ctx := context.Background()
	for _, c := range [][3]string{
		{"menu:p2p", "adm:cards", "2200000000000000"},
		{"menu:yookassa", "yk:shop", "123456"},
		{"menu:trial", "trial:days", "5"},
		{"menu:backup", "bk:pass", "a-long-enough-password"},
		{"menu:moynalog", "mn:name", "Доступ"},
	} {
		for _, cancel := range []bool{false, true} {
			a, fm := editApp(t)
			a.handleCallback(ctx, cb(100, c[0]))
			screen, prot := fm.seq, fm.isProt(fm.seq)
			a.handleCallback(ctx, cbUnder(100, c[1], screen, prot))
			if fm.seq != screen {
				t.Errorf("%s → %s: вопрос пришёл новым сообщением", c[0], c[1])
				continue
			}
			if cancel {
				a.handleCallback(ctx, cbUnder(100, "inp:cancel", screen, prot))
			} else {
				a.handleMessage(ctx, msgText(100, c[2]))
			}
			if fm.seq != screen {
				t.Errorf("%s → %s (отмена=%v): экран после ответа пришёл новым сообщением", c[0], c[1], cancel)
			}
		}
	}
}
