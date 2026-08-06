package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

const torrentBody = `{
  "scope": "torrent_blocker",
  "event": "torrent_blocker.report",
  "timestamp": "2026-03-07T16:02:50.564Z",
  "data": {
    "node": {"name": "node-1", "countryCode": "NL"},
    "user": {"telegramId": 42, "username": "tg_42", "status": "ACTIVE"},
    "report": {
      "actionReport": {
        "blocked": true,
        "ip": "203.0.113.7",
        "blockDuration": 3600,
        "willUnblockAt": "2026-03-07T17:02:48.986Z",
        "userId": "2",
        "processedAt": "2026-03-07T16:02:48.986Z"
      },
      "xrayReport": {
        "email": "2",
        "protocol": "bittorrent",
        "source": "203.0.113.7:51431",
        "destination": "198.51.100.9:59755",
        "inboundTag": "VLESS_TCP_REALITY"
      }
    }
  }
}`

func countContains(texts []string, sub string) int {
	n := 0
	for _, t := range texts {
		if strings.Contains(t, sub) {
			n++
		}
	}
	return n
}

// По умолчанию (конфиг без явной настройки) отчёт уходит и админу
// (развёрнуто), и пользователю (предупреждение).
func TestTorrentWebhook_NotifiesAdminAndUser(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()

	handled, err := a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if countContains(fm.texts, "Торрент-блокер") != 1 {
		t.Fatalf("ожидалось одно сообщение админу, тексты: %v", fm.texts)
	}
	if countContains(fm.texts, "Обнаружен торрент-трафик") != 1 {
		t.Fatalf("ожидалось одно предупреждение пользователю, тексты: %v", fm.texts)
	}
	adm := ""
	for _, s := range fm.texts {
		if strings.Contains(s, "Торрент-блокер") {
			adm = s
		}
	}
	for _, want := range []string{"203.0.113.7", "bittorrent", "VLESS_TCP_REALITY", "node-1 (NL)", "1 ч", "198.51.100.9:59755"} {
		if !strings.Contains(adm, want) {
			t.Fatalf("в отчёте админу нет %q: %s", want, adm)
		}
	}

	// Повтор в пределах паузы: админ получает снова, пользователь — нет.
	_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if countContains(fm.texts, "Торрент-блокер") != 2 {
		t.Fatalf("админ должен получать каждый отчёт, тексты: %v", fm.texts)
	}
	if countContains(fm.texts, "Обнаружен торрент-трафик") != 1 {
		t.Fatalf("пользователь не должен получать повтор в пределах паузы, тексты: %v", fm.texts)
	}
}

// Выключенные тумблеры глушат соответствующие уведомления.
func TestTorrentWebhook_Toggles(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Torrent: model.TorrentConfig{Init: true}}

	handled, err := a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if len(fm.texts) != 0 {
		t.Fatalf("при выключенных тумблерах сообщений быть не должно: %v", fm.texts)
	}

	a.toggleTorrentNotify(true) // включить админа
	_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if countContains(fm.texts, "Торрент-блокер") != 1 || countContains(fm.texts, "Обнаружен торрент-трафик") != 0 {
		t.Fatalf("ожидался только отчёт админу, тексты: %v", fm.texts)
	}
}

// Пользователь без Telegram (нет telegramId) — предупреждение некому слать,
// но отчёт админу уходит с подписью из payload.
func TestTorrentWebhook_NoTelegramID(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()
	body := strings.Replace(torrentBody, `"telegramId": 42, `, "", 1)

	handled, err := a.HandleRemnawaveWebhook(ctx, "", []byte(body))
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if countContains(fm.texts, "Обнаружен торрент-трафик") != 0 {
		t.Fatalf("без telegramId предупреждать некого: %v", fm.texts)
	}
	if countContains(fm.texts, "tg_42") != 1 {
		t.Fatalf("админ должен видеть username из payload: %v", fm.texts)
	}
}

// Тумблеры переключаются с экрана «Вебхуки» через настоящий callback-роутер.
func TestTorrentToggle_Callback(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()

	a.handleCallback(ctx, cb(100, "wh:tusr"))
	if a.botCfg.Torrent.NotifyUser || !a.botCfg.Torrent.NotifyAdmin {
		t.Fatalf("wh:tusr должен выключить только юзера: %+v", a.botCfg.Torrent)
	}
	a.handleCallback(ctx, cb(100, "wh:tadm"))
	if a.botCfg.Torrent.NotifyAdmin {
		t.Fatalf("wh:tadm должен выключить админа: %+v", a.botCfg.Torrent)
	}
	a.handleCallback(ctx, cb(100, "wh:tusr"))
	if !a.botCfg.Torrent.NotifyUser {
		t.Fatalf("повторный wh:tusr должен включить юзера обратно: %+v", a.botCfg.Torrent)
	}

	// Не-админу тумблер недоступен.
	a.handleCallback(ctx, cb(500, "wh:tadm"))
	if a.botCfg.Torrent.NotifyAdmin {
		t.Fatalf("не-админ не должен переключать тумблер: %+v", a.botCfg.Torrent)
	}
}

// Отчёт пишется в журнал, повторные нарушения нумеруются в отчёте админу.
func TestTorrentWebhook_JournalAndRepeat(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()

	_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if len(fs.torrents) != 1 {
		t.Fatalf("ожидалась 1 запись в журнале, есть %d", len(fs.torrents))
	}
	r := fs.torrents[0]
	if r.TelegramID != 42 || r.IP != "203.0.113.7" || r.BlockSeconds != 3600 ||
		r.Node != "node-1 (NL)" || r.WillUnblockAt != "2026-03-07T17:02:48Z" || r.UnblockNotified {
		t.Fatalf("запись журнала кривая: %+v", r)
	}
	if countContains(fm.texts, "1-е за 30 дней") != 1 {
		t.Fatalf("первый отчёт должен быть «1-е за 30 дней»: %v", fm.texts)
	}

	_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if len(fs.torrents) != 2 {
		t.Fatalf("ожидались 2 записи, есть %d", len(fs.torrents))
	}
	if countContains(fm.texts, "2-е за 30 дней") != 1 {
		t.Fatalf("повтор должен быть «2-е за 30 дней»: %v", fm.texts)
	}
}

// Запись без telegramId помечается «уведомлять некого» и не попадает в очередь
// разблокировок.
func TestTorrentWebhook_JournalNoTgID(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	body := strings.Replace(torrentBody, `"telegramId": 42, `, "", 1)

	_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(body))
	if len(fs.torrents) != 1 || !fs.torrents[0].UnblockNotified || fs.torrents[0].Username != "tg_42" {
		t.Fatalf("запись кривая: %+v", fs.torrents)
	}
	due, _ := fs.DueTorrentUnblocks(ctx, "9999-01-01T00:00:00Z")
	if len(due) != 0 {
		t.Fatalf("без telegramId в очереди разблокировок пусто: %v", due)
	}
}

// Когда срок вышел — юзеру уходит «блокировка снята», запись помечается,
// повторный проход ничего не шлёт.
func TestTorrentUnblock_Notify(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()

	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	_ = fs.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: 42, WillUnblockAt: past})
	a.torrentUnblockOnce(ctx)
	if countContains(fm.texts, "Блокировка снята") != 1 {
		t.Fatalf("ожидалось одно «блокировка снята»: %v", fm.texts)
	}
	if !fs.torrents[0].UnblockNotified {
		t.Fatalf("запись не помечена отправленной: %+v", fs.torrents[0])
	}
	a.torrentUnblockOnce(ctx)
	if countContains(fm.texts, "Блокировка снята") != 1 {
		t.Fatalf("повторный проход не должен слать снова: %v", fm.texts)
	}
}

// Просроченные записи (бот долго стоял) помечаются молча, при выключенном
// тумблере юзера сообщение не шлётся.
func TestTorrentUnblock_StaleAndToggle(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()

	stale := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	_ = fs.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: 42, WillUnblockAt: stale})
	a.torrentUnblockOnce(ctx)
	if countContains(fm.texts, "Блокировка снята") != 0 || !fs.torrents[0].UnblockNotified {
		t.Fatalf("просроченная запись: молча пометить, не слать: %v %+v", fm.texts, fs.torrents[0])
	}

	a.botCfg = &model.BotConfig{Torrent: model.TorrentConfig{NotifyAdmin: true, NotifyUser: false, Init: true}}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	_ = fs.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: 43, WillUnblockAt: past})
	a.torrentUnblockOnce(ctx)
	if countContains(fm.texts, "Блокировка снята") != 0 || !fs.torrents[1].UnblockNotified {
		t.Fatalf("при выключенном тумблере: молча пометить, не слать: %v", fm.texts)
	}
}

// Кастомный текст разблокировки: сохранение через настоящий ввод админа
// (с entities), тест-отправка админу, сброс на стандартный.
func TestTorrentUnblock_CustomTextEditor(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()

	a.handleCallback(ctx, cb(100, "torj:edit"))
	if !a.getUI(100).torAwait {
		t.Fatalf("после torj:edit должен ждать ввод")
	}
	m := msgText(100, "Свобода! Качайте что угодно, кроме торрентов")
	m.Entities = []models.MessageEntity{{Type: "bold", Offset: 0, Length: 8}}
	a.handleMessage(ctx, m)
	if a.getUI(100).torAwait || a.botCfg.Torrent.UnblockText != m.Text || len(a.botCfg.Torrent.UnblockEntities) == 0 {
		t.Fatalf("текст не сохранился: %+v", a.botCfg.Torrent)
	}

	fm.texts = nil
	a.handleCallback(ctx, cb(100, "torj:test"))
	if len(fm.texts) == 0 || fm.texts[0] != m.Text {
		t.Fatalf("тест-отправка должна прислать кастомный текст 1-в-1: %v", fm.texts)
	}

	a.handleCallback(ctx, cb(100, "torj:reset"))
	if a.botCfg.Torrent.UnblockText != "" {
		t.Fatalf("сброс не сработал: %+v", a.botCfg.Torrent)
	}
	fm.texts = nil
	a.sendTorrentUnblock(ctx, 42)
	if countContains(fm.texts, "Блокировка снята") != 1 {
		t.Fatalf("после сброса должен уходить стандартный текст: %v", fm.texts)
	}
}

func TestFmtBlockDur(t *testing.T) {
	cases := map[int]string{
		0:    "—",
		45:   "45 сек",
		60:   "1 мин",
		90:   "1 мин 30 сек",
		3600: "1 ч",
		5400: "1 ч 30 мин",
	}
	for secs, want := range cases {
		if got := fmtBlockDur("ru", secs); got != want {
			t.Fatalf("fmtBlockDur(%d) = %q, ожидалось %q", secs, got, want)
		}
	}
}

// torrentPanelStub — панель для действий по отчёту: снятие блокировки через
// Executor, правка конфига плагина, отключение подписки.
type torrentPanelStub struct {
	srv        *httptest.Server
	unblocked  []string
	patched    int
	disabledTG bool
}

func newTorrentPanelStub(t *testing.T) *torrentPanelStub {
	t.Helper()
	s := &torrentPanelStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/node-plugins/executor", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Command struct {
				Command string   `json:"command"`
				IPs     []string `json:"ips"`
			} `json:"command"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.unblocked = append(s.unblocked, body.Command.IPs...)
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/api/node-plugins", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			s.patched++
			_, _ = w.Write([]byte(`{"response":{"uuid":"p-1","name":"main","viewPosition":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"total":1,"nodePlugins":[{"uuid":"p-1","name":"main",` +
			`"pluginConfig":{"torrentBlocker":{"enabled":true,"blockDuration":3600}}}]}}`))
	})
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","username":"tg_42","telegramId":42,"status":"ACTIVE"}]}`))
	})
	mux.HandleFunc("/api/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			s.disabledTG = true
		}
		_, _ = w.Write([]byte(`{"response":{"uuid":"u-1"}}`))
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *torrentPanelStub) attach(a *App) {
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: s.srv.URL, APIToken: "t"})
}

// В карточке отчёта админу есть кнопки действий: снять блокировку с IP и
// увести пользователя из-под блокера.
func TestTorrentWebhook_AdminCardHasActions(t *testing.T) {
	a, fm, _ := newTestApp(t)
	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", []byte(torrentBody)); err != nil {
		t.Fatalf("err=%v", err)
	}
	data := fm.allCallbackData()
	for _, want := range []string{"torj:unb:203.0.113.7", "torj:ign:2", "usr:block:42", "torj:log"} {
		if !hasCB(data, want) {
			t.Fatalf("нет кнопки %q; собрано: %v", want, data)
		}
	}
}

// Кнопка «снять блокировку»: команда уходит в панель, запись журнала
// закрывается, пользователю сразу уходит «блокировка снята» — иначе тикер
// прислал бы его ещё раз в исходный срок.
func TestTorrentUnblock_Button(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	if _, err := a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody)); err != nil {
		t.Fatalf("err=%v", err)
	}
	fm.texts = nil

	a.handleCallback(ctx, cb(100, "torj:unb:203.0.113.7"))

	if len(stub.unblocked) != 1 || stub.unblocked[0] != "203.0.113.7" {
		t.Fatalf("в панель не ушла команда разблокировки: %v", stub.unblocked)
	}
	if countContains(fm.texts, "Блокировка адреса") != 1 {
		t.Fatalf("админ не получил подтверждения: %v", fm.texts)
	}
	if countContains(fm.texts, "Блокировка снята") != 1 {
		t.Fatalf("пользователь не получил «блокировка снята»: %v", fm.texts)
	}
	pending, _ := fs.PendingTorrentUnblocksByIP(ctx, "203.0.113.7")
	if len(pending) != 0 {
		t.Fatalf("записи журнала остались открытыми: %+v", pending)
	}
}

// Кнопка «в исключения» правит конфиг плагина на панели.
func TestTorrentIgnore_Button(t *testing.T) {
	a, fm, _ := newTestApp(t)
	ctx := context.Background()
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	a.handleCallback(ctx, cb(100, "torj:ign:2"))

	if stub.patched != 1 {
		t.Fatalf("конфиг плагина не изменён: patched=%d", stub.patched)
	}
	if countContains(fm.texts, "добавлен в исключения") != 1 {
		t.Fatalf("нет подтверждения админу: %v", fm.texts)
	}
}

// Не-админ до действий не допускается.
func TestTorrentActions_AdminOnly(t *testing.T) {
	a, _, _ := newTestApp(t)
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	a.handleCallback(context.Background(), cb(500, "torj:unb:203.0.113.7"))
	if len(stub.unblocked) != 0 {
		t.Fatalf("не-админ снял блокировку: %v", stub.unblocked)
	}
}

// Политика страйков: до порога подписку не трогаем, на пороге — отключаем и
// уведомляем обе стороны.
func TestTorrentStrikes_DisablesAtThreshold(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 2
	a.botCfg.Webhook.RemnawaveSecret = "s"
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
	if stub.disabledTG {
		t.Fatalf("первое нарушение не должно отключать подписку")
	}

	fm.texts = nil
	rwDeliver(t, a, torrentBody)
	if !stub.disabledTG {
		t.Fatalf("на пороге подписка должна быть отключена")
	}
	if countContains(fm.texts, "Автоблокировка торрент-блокера") != 1 {
		t.Fatalf("админ не получил уведомления: %v", fm.texts)
	}
	if countContains(fm.texts, "Подписка приостановлена") != 1 {
		t.Fatalf("пользователь не получил уведомления: %v", fm.texts)
	}
}

// Порог 0 — политика выключена, сколько бы нарушений ни было.
func TestTorrentStrikes_OffByDefault(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	for i := 0; i < 5; i++ {
		_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	}
	if stub.disabledTG {
		t.Fatalf("при выключенной политике подписку трогать нельзя")
	}
}

// Повторные отчёты после срабатывания не отключают подписку снова: иначе
// возвращённый админом доступ снимался бы через минуту.
func TestTorrentStrikes_Cooldown(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 1
	a.botCfg.Webhook.RemnawaveSecret = "s"
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
	fm.texts = nil
	stub.disabledTG = false
	rwDeliver(t, a, torrentBody)

	if stub.disabledTG {
		t.Fatalf("повторное срабатывание сразу после страйка")
	}
	if countContains(fm.texts, "Автоблокировка торрент-блокера") != 0 {
		t.Fatalf("повторное уведомление внутри паузы: %v", fm.texts)
	}
}

// Порог задаётся админом с экрана «Вебхуки» и сохраняется в конфиг.
func TestTorrentStrikes_AdminSetsLimit(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "torj:strike"))
	a.handleMessage(ctx, msgText(100, "3"))
	if a.botCfg.Torrent.StrikeLimit != 3 {
		t.Fatalf("порог не сохранён: %+v", a.botCfg.Torrent)
	}

	a.handleCallback(ctx, cb(100, "torj:strike"))
	a.handleMessage(ctx, msgText(100, "0"))
	if a.botCfg.Torrent.StrikeLimit != 0 {
		t.Fatalf("порог не сбрасывается в 0: %+v", a.botCfg.Torrent)
	}
}

// rwDeliver доставляет отчёт с корректной подписью: политика автоблокировки
// работает только при заданном секрете вебхука.
func rwDeliver(t *testing.T, a *App, body string) {
	t.Helper()
	a.mu.Lock()
	secret := ""
	if a.botCfg != nil {
		secret = a.botCfg.Webhook.RemnawaveSecret
	}
	a.mu.Unlock()
	sig := ""
	if secret != "" {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(body))
		sig = hex.EncodeToString(m.Sum(nil))
	}
	if _, err := a.HandleRemnawaveWebhook(context.Background(), sig, []byte(body)); err != nil {
		t.Fatalf("доставка отчёта: %v", err)
	}
}

// Без секрета вебхука автоблокировка не срабатывает: иначе подделанным
// отчётом можно было бы отключить подписку любому пользователю.
func TestTorrentStrikes_RequiresWebhookSecret(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 1
	ctx := context.Background()
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	_, _ = a.HandleRemnawaveWebhook(ctx, "", []byte(torrentBody))
	if stub.disabledTG {
		t.Fatalf("без секрета вебхука подписку трогать нельзя")
	}
}

// Отчёт с blocked=false (например, пользователь уже в исключениях панели) —
// не нарушение: ни страйка, ни сообщения «ваш IP заблокирован».
func TestTorrentWebhook_NotBlockedIsNotAViolation(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 1
	a.botCfg.Webhook.RemnawaveSecret = "s"
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, strings.Replace(torrentBody, `"blocked": true`, `"blocked": false`, 1))

	if stub.disabledTG {
		t.Fatalf("отчёт без блокировки не должен отключать подписку")
	}
	if countContains(fm.texts, "Обнаружен торрент-трафик") != 0 {
		t.Fatalf("пользователю писать нечего: %v", fm.texts)
	}
	if countContains(fm.texts, "Торрент-блокер") != 1 {
		t.Fatalf("админу отчёт всё равно нужен: %v", fm.texts)
	}
}

// После ручного возврата доступа отсчёт начинается заново: старые нарушения
// не должны отключать подписку снова, в том числе после рестарта бота.
func TestTorrentStrikes_CountRestartsAfterStrike(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 2
	a.botCfg.Webhook.RemnawaveSecret = "s"
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
	rwDeliver(t, a, torrentBody)
	if !stub.disabledTG {
		t.Fatalf("на пороге подписка должна быть отключена")
	}

	// Админ вернул доступ, бот перезапустили (вся память сброшена). Отметку
	// страйка сдвигаем в прошлое: в бою между срабатыванием и следующим
	// отчётом проходят минуты, а в тесте всё укладывается в одну секунду.
	stub.disabledTG = false
	a.thrMu.Lock()
	a.torSeen, a.torUnbSeen = nil, nil
	a.thrMu.Unlock()
	now := time.Now().UTC()
	for i := range fs.torrents {
		fs.torrents[i].CreatedAt = now.Add(-60 * time.Second).Format(time.RFC3339)
	}
	fs.strikes[42] = now.Add(-30 * time.Second).Format(time.RFC3339)

	rwDeliver(t, a, torrentBody)
	if stub.disabledTG {
		t.Fatalf("одного нового нарушения при пороге 2 мало — отсчёт должен идти с момента страйка")
	}
	rwDeliver(t, a, torrentBody)
	if !stub.disabledTG {
		t.Fatalf("двух новых нарушений достаточно для повторного срабатывания")
	}
}

// Мусор вместо IP не попадает в callback_data: кнопка длиннее 64 байт
// не даёт Telegram отправить отчёт админу вообще.
func TestTorrentWebhook_BadIPHasNoUnblockButton(t *testing.T) {
	a, fm, _ := newTestApp(t)
	body := strings.Replace(torrentBody, `"ip": "203.0.113.7"`,
		`"ip": "`+strings.Repeat("x", 120)+`"`, 1)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", []byte(body)); err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "torj:unb:") {
			t.Fatalf("кнопка снятия блокировки не должна появиться: %q", d)
		}
		if len(d) > 64 {
			t.Fatalf("callback_data длиннее лимита Telegram: %d байт", len(d))
		}
	}
}

// Отказ панели (старая версия, нет скоупа у токена) админ должен увидеть
// текстом, а не тишиной.
func TestTorrentActions_PanelErrorIsReported(t *testing.T) {
	a, fm, _ := newTestApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Forbidden resource"}`))
	}))
	defer srv.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "torj:unb:203.0.113.7"))
	if countContains(fm.texts, "Не удалось снять блокировку") != 1 {
		t.Fatalf("нет сообщения об ошибке: %v", fm.texts)
	}
	fm.texts = nil
	a.handleCallback(ctx, cb(100, "torj:ign:2"))
	if countContains(fm.texts, "Не удалось изменить конфиг плагина") != 1 {
		t.Fatalf("нет сообщения об ошибке: %v", fm.texts)
	}
}

// Без панели действия молча не проваливаются.
func TestTorrentActions_NoPanel(t *testing.T) {
	a, fm, _ := newTestApp(t)
	a.handleCallback(context.Background(), cb(100, "torj:unb:203.0.113.7"))
	if countContains(fm.texts, "Панель не настроена") != 1 {
		t.Fatalf("нет сообщения: %v", fm.texts)
	}
}

// Отчёт без блокировки не попадает в журнал: иначе он копился бы в счётчике и
// доводил до автоблокировки вместе с настоящими нарушениями.
func TestTorrentWebhook_NotBlockedIsNotCounted(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 2
	a.botCfg.Webhook.RemnawaveSecret = "s"
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, strings.Replace(torrentBody, `"blocked": true`, `"blocked": false`, 1))
	if len(fs.torrents) != 0 {
		t.Fatalf("отчёт без блокировки в журнал не пишется: %+v", fs.torrents)
	}
	fm.texts = nil
	rwDeliver(t, a, torrentBody)
	if stub.disabledTG {
		t.Fatalf("одно настоящее нарушение при пороге 2 не должно отключать подписку")
	}
}

// Панель не знает такого пользователя: подписка не отключена, значит и
// рапортовать об отключении нельзя, и отметку страйка ставить нечего.
func TestTorrentStrikes_UserNotOnPanel(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 1
	a.botCfg.Webhook.RemnawaveSecret = "s"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	defer srv.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	rwDeliver(t, a, torrentBody)

	if countContains(fm.texts, "Подписка приостановлена") != 0 {
		t.Fatalf("пользователю нельзя сообщать об отключении, которого не было: %v", fm.texts)
	}
	if countContains(fm.texts, "Автоблокировка не сработала") != 1 {
		t.Fatalf("админ должен узнать о неудаче: %v", fm.texts)
	}
	if at, _ := fs.TorrentStrikeAt(ctxBG(), 42); at != "" {
		t.Fatalf("отметка страйка не должна ставиться: %q", at)
	}
}

// Если подписку сняли страйком, «блокировка снята» слать нельзя: срок блокировки
// IP истёк, а доступа у человека всё равно нет.
func TestTorrentUnblock_SilentAfterStrike(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 1
	a.botCfg.Webhook.RemnawaveSecret = "s"
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
	if !stub.disabledTG {
		t.Fatalf("подписка должна быть отключена")
	}
	fm.texts = nil

	// Срок блокировки IP вышел — тикер разбирает очередь.
	for i := range fs.torrents {
		fs.torrents[i].WillUnblockAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	}
	a.torrentUnblockOnce(ctxBG())

	if countContains(fm.texts, "Блокировка снята") != 0 {
		t.Fatalf("сообщение о снятии не должно уходить при отключённой подписке: %v", fm.texts)
	}
	if !fs.torrents[0].UnblockNotified {
		t.Fatalf("запись всё равно должна закрыться, иначе тикер будет крутить её вечно")
	}
}

func ctxBG() context.Context { return context.Background() }
