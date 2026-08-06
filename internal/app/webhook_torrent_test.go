package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// torrentReportN — тот же отчёт, но как ОТДЕЛЬНЫЙ инцидент: журнал теперь
// идемпотентен по (кто, адрес, нода, момент блокировки), и повтор того же тела
// считается переотправкой вебхука, а не новым нарушением.
func torrentReportN(i int) string {
	return strings.Replace(torrentBody, "16:02:48.986",
		fmt.Sprintf("16:%02d:%02d.000", i/60%60, i%60), -1)
}

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
	a, fm, _ := torrentApp(t)

	rwDeliver(t, a, torrentBody)
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
	rwDeliver(t, a, torrentBody)
	if countContains(fm.texts, "Торрент-блокер") != 2 {
		t.Fatalf("админ должен получать каждый отчёт, тексты: %v", fm.texts)
	}
	if countContains(fm.texts, "Обнаружен торрент-трафик") != 1 {
		t.Fatalf("пользователь не должен получать повтор в пределах паузы, тексты: %v", fm.texts)
	}
}

// Выключенные тумблеры глушат соответствующие уведомления.
func TestTorrentWebhook_Toggles(t *testing.T) {
	a, fm, _ := torrentApp(t)
	a.botCfg.Torrent = model.TorrentConfig{Init: true}

	rwDeliver(t, a, torrentBody)
	if len(fm.texts) != 0 {
		t.Fatalf("при выключенных тумблерах сообщений быть не должно: %v", fm.texts)
	}

	a.toggleTorrentNotify(true) // включить админа
	rwDeliver(t, a, torrentBody)
	if countContains(fm.texts, "Торрент-блокер") != 1 || countContains(fm.texts, "Обнаружен торрент-трафик") != 0 {
		t.Fatalf("ожидался только отчёт админу, тексты: %v", fm.texts)
	}
}

// Пользователь без Telegram (нет telegramId) — предупреждение некому слать,
// но отчёт админу уходит с подписью из payload.
func TestTorrentWebhook_NoTelegramID(t *testing.T) {
	a, fm, _ := torrentApp(t)
	body := strings.Replace(torrentBody, `"telegramId": 42, `, "", 1)

	rwDeliver(t, a, body)
	if countContains(fm.texts, "Обнаружен торрент-трафик") != 0 {
		t.Fatalf("без telegramId предупреждать некого: %v", fm.texts)
	}
	if countContains(fm.texts, "tg_42") != 1 {
		t.Fatalf("админ должен видеть username из payload: %v", fm.texts)
	}
}

// Тумблеры переключаются с экрана «Торренты» через настоящий callback-роутер.
func TestTorrentToggle_Callback(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()

	a.handleCallback(ctx, cb(100, "torj:tusr"))
	if a.botCfg.Torrent.NotifyUser || !a.botCfg.Torrent.NotifyAdmin {
		t.Fatalf("torj:tusr должен выключить только юзера: %+v", a.botCfg.Torrent)
	}
	a.handleCallback(ctx, cb(100, "torj:tadm"))
	if a.botCfg.Torrent.NotifyAdmin {
		t.Fatalf("torj:tadm должен выключить админа: %+v", a.botCfg.Torrent)
	}
	a.handleCallback(ctx, cb(100, "torj:tusr"))
	if !a.botCfg.Torrent.NotifyUser {
		t.Fatalf("повторный torj:tusr должен включить юзера обратно: %+v", a.botCfg.Torrent)
	}

	// Не-админу тумблер недоступен.
	a.handleCallback(ctx, cb(500, "torj:tadm"))
	if a.botCfg.Torrent.NotifyAdmin {
		t.Fatalf("не-админ не должен переключать тумблер: %+v", a.botCfg.Torrent)
	}
}

// Отчёт пишется в журнал, повторные нарушения нумеруются в отчёте админу.
func TestTorrentWebhook_JournalAndRepeat(t *testing.T) {
	a, fm, fs := torrentApp(t)

	rwDeliver(t, a, torrentBody)
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

	rwDeliver(t, a, torrentReportN(1))
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
	a, _, fs := torrentApp(t)
	ctx := context.Background()
	body := strings.Replace(torrentBody, `"telegramId": 42, `, "", 1)

	rwDeliver(t, a, body)
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
	a, fm, fs := torrentApp(t)
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
	a, fm, fs := torrentApp(t)
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
	// Статус подписки стаб держит честно: бот спрашивает его перед тем, как
	// сказать человеку «блокировка снята».
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := "ACTIVE"
		if s.disabledTG {
			status = "DISABLED"
		}
		_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","username":"tg_42","telegramId":42,` +
			`"subscriptionUrl":"https://example.com/s","expireAt":"2099-01-01T00:00:00.000Z","status":"` + status + `"}]}`))
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
	a, fm, _ := torrentApp(t)
	rwDeliver(t, a, torrentBody)
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
	a, fm, fs := torrentApp(t)
	ctx := context.Background()
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
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
	a, fm, _ := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 2
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentReportN(1))
	if stub.disabledTG {
		t.Fatalf("первое нарушение не должно отключать подписку")
	}

	fm.texts = nil
	rwDeliver(t, a, torrentReportN(2))
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
	a, _, _ := torrentApp(t)
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	for i := 0; i < 5; i++ {
		rwDeliver(t, a, torrentReportN(i))
	}
	if stub.disabledTG {
		t.Fatalf("при выключенной политике подписку трогать нельзя")
	}
}

// Повторные отчёты после срабатывания не отключают подписку снова: иначе
// возвращённый админом доступ снимался бы через минуту.
func TestTorrentStrikes_Cooldown(t *testing.T) {
	a, fm, _ := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentReportN(1))
	fm.texts = nil
	stub.disabledTG = false
	rwDeliver(t, a, torrentReportN(2))

	if stub.disabledTG {
		t.Fatalf("повторное срабатывание сразу после страйка")
	}
	if countContains(fm.texts, "Автоблокировка торрент-блокера") != 0 {
		t.Fatalf("повторное уведомление внутри паузы: %v", fm.texts)
	}
}

// Порог задаётся админом с экрана «Пользователи → Торренты» и сохраняется в конфиг.
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
// torrentApp — приложение с уже настроенным секретом вебхука: обработка
// отчётов торрент-блокера без него намеренно отключена.
func torrentApp(t *testing.T) (*App, *fakeMsg, *fakeStore) {
	t.Helper()
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Webhook.RemnawaveSecret = "s"
	return a, fm, fs
}

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

// Без секрета вебхука подпись не проверяется, поэтому отчёт торрент-блокера
// не обрабатывается вовсе: подделанным запросом можно было бы и подписку
// отключить, и рассылать сообщения произвольным Telegram-ID, и раздуть журнал.
func TestTorrentWebhook_RequiresWebhookSecret(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	a.botCfg.Torrent.StrikeLimit = 1
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	if _, err := a.HandleRemnawaveWebhook(context.Background(), "", []byte(torrentBody)); err != nil {
		t.Fatalf("err=%v", err)
	}
	if stub.disabledTG {
		t.Fatalf("без секрета вебхука подписку трогать нельзя")
	}
	if len(fs.torrents) != 0 {
		t.Fatalf("без секрета в журнал писать нельзя: %+v", fs.torrents)
	}
	if len(fm.texts) != 0 {
		t.Fatalf("без секрета сообщений быть не должно: %v", fm.texts)
	}
}

// Отчёт с blocked=false (например, пользователь уже в исключениях панели) —
// не нарушение: ни страйка, ни сообщения «ваш IP заблокирован».
func TestTorrentWebhook_NotBlockedIsNotAViolation(t *testing.T) {
	a, fm, _ := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
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
	a, _, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 2
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentReportN(1))
	rwDeliver(t, a, torrentReportN(2))
	if !stub.disabledTG {
		t.Fatalf("на пороге подписка должна быть отключена")
	}

	// Админ вернул доступ, бот перезапустили (вся память сброшена). Время
	// сдвигаем в прошлое: в бою между срабатыванием и следующей волной отчётов
	// проходят часы, а в тесте всё укладывается в одну секунду. Отметка старше
	// torrentStrikeGrace — пауза после страйка уже истекла.
	stub.disabledTG = false
	a.thrMu.Lock()
	a.torSeen, a.torUnbSeen, a.torStrikeSeen = nil, nil, nil
	a.thrMu.Unlock()
	now := time.Now().UTC()
	for i := range fs.torrents {
		fs.torrents[i].CreatedAt = now.Add(-3 * time.Hour).Format(time.RFC3339)
	}
	fs.strikes[42] = now.Add(-2 * time.Hour).Format(time.RFC3339)

	rwDeliver(t, a, torrentReportN(3))
	if stub.disabledTG {
		t.Fatalf("одного нового нарушения при пороге 2 мало — отсчёт должен идти с момента страйка")
	}
	rwDeliver(t, a, torrentReportN(4))
	if !stub.disabledTG {
		t.Fatalf("двух новых нарушений достаточно для повторного срабатывания")
	}
}

// Мусор вместо IP не попадает в callback_data: кнопка длиннее 64 байт
// не даёт Telegram отправить отчёт админу вообще.
func TestTorrentWebhook_BadIPHasNoUnblockButton(t *testing.T) {
	a, fm, _ := torrentApp(t)
	body := strings.Replace(torrentBody, `"ip": "203.0.113.7"`,
		`"ip": "`+strings.Repeat("x", 120)+`"`, 1)

	rwDeliver(t, a, body)
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
	a, fm, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 2
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
	a, fm, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
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
	a, fm, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
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

// Раздел «Торренты» живёт в «Пользователях», а не в «Вебхуках»: на экране
// вебхуков остаётся только транспорт и подсказка, куда всё переехало.
func TestTorrentSection_LivesUnderUsers(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()
	ctx := context.Background()

	a.handleCallback(ctx, cb(100, "menu:users"))
	if !hasCB(fm.allCallbackData(), "torj:home") {
		t.Fatalf("на экране пользователей нет входа в раздел: %v", fm.allCallbackData())
	}

	fm.cbData, fm.texts = nil, nil
	a.handleCallback(ctx, cb(100, "menu:webhooks"))
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "torj:") {
			t.Fatalf("настройки торрентов остались в вебхуках: %q", d)
		}
	}
	if countContains(fm.texts, "Пользователи") != 1 {
		t.Fatalf("нет подсказки, куда переехали настройки: %v", fm.texts)
	}

	fm.cbData, fm.texts = nil, nil
	a.handleCallback(ctx, cb(100, "torj:home"))
	for _, want := range []string{"torj:tadm", "torj:tusr", "torj:strike", "torj:log", "torj:text", "menu:users"} {
		if !hasCB(fm.allCallbackData(), want) {
			t.Fatalf("в разделе нет кнопки %q: %v", want, fm.allCallbackData())
		}
	}
}

// В карточке нарушителя есть строка и кнопка с его историей; у чистого
// пользователя карточка не меняется.
func TestUserCard_TorrentsOnlyWhenViolations(t *testing.T) {
	a, fm, fs := torrentApp(t)
	ctx := context.Background()
	_ = fs.UpsertUser(ctx, 42)
	_ = fs.UpsertUser(ctx, 77)

	a.handleCallback(ctx, cb(100, "usr:view:77"))
	if countContains(fm.texts, "Нарушений торрент-блокера") != 0 {
		t.Fatalf("у чистого пользователя строки быть не должно: %v", fm.texts)
	}
	if hasCB(fm.allCallbackData(), "torj:u:77") {
		t.Fatalf("у чистого пользователя кнопки быть не должно")
	}

	rwDeliver(t, a, torrentBody)
	fm.cbData, fm.texts = nil, nil

	a.handleCallback(ctx, cb(100, "usr:view:42"))
	if countContains(fm.texts, "Нарушений торрент-блокера") != 1 {
		t.Fatalf("нет строки о нарушениях: %v", fm.texts)
	}
	if !hasCB(fm.allCallbackData(), "torj:u:42") {
		t.Fatalf("нет кнопки истории: %v", fm.allCallbackData())
	}

	fm.texts = nil
	a.handleCallback(ctx, cb(100, "torj:u:42"))
	if countContains(fm.texts, "Нарушения") != 1 {
		t.Fatalf("история не открылась: %v", fm.texts)
	}
	fm.texts = nil
	a.handleCallback(ctx, cb(100, "torj:u:77"))
	if countContains(fm.texts, "отчётов торрент-блокера нет") != 1 {
		t.Fatalf("для чистого пользователя ожидался пустой экран: %v", fm.texts)
	}
}

// Сразу после срабатывания политика молчит: отключение доезжает до нод не
// мгновенно, и отчёты этих минут не должны дать второй страйк подряд.
func TestTorrentStrikes_GraceAfterStrike(t *testing.T) {
	a, fm, _ := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
	if !stub.disabledTG {
		t.Fatalf("первое срабатывание должно отключить подписку")
	}
	stub.disabledTG = false
	fm.texts = nil

	for i := 1; i < 6; i++ {
		rwDeliver(t, a, torrentReportN(i))
	}
	if stub.disabledTG {
		t.Fatalf("повторное отключение внутри паузы")
	}
	if countContains(fm.texts, "Подписка приостановлена") != 0 {
		t.Fatalf("повторные сообщения пользователю внутри паузы: %v", fm.texts)
	}
}

// Панель не приняла отключение: отметка страйка должна вернуться к прежней,
// иначе окно сдвинулось бы без единого отключения. Жалоба админу — одна.
func TestTorrentStrikes_FailureRollsBackMarkAndThrottles(t *testing.T) {
	a, fm, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`)) // пользователя на панели нет
	}))
	defer srv.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	for i := 0; i < 4; i++ {
		rwDeliver(t, a, torrentReportN(i))
	}
	if at, _ := fs.TorrentStrikeAt(ctxBG(), 42); at != "" {
		t.Fatalf("отметка не откатилась после неудачи: %q", at)
	}
	if n := countContains(fm.texts, "Автоблокировка не сработала"); n != 1 {
		t.Fatalf("админу должна уйти одна жалоба, ушло %d: %v", n, fm.texts)
	}
}

// Отчёт с blocked=false у аккаунта без Telegram считается по username панели —
// иначе админ увидит «1-е нарушение» у человека с длинной историей.
func TestTorrentWebhook_NotBlockedCountsByPanelName(t *testing.T) {
	a, fm, _ := torrentApp(t)
	body := strings.Replace(torrentBody, `"telegramId": 42, `, "", 1)

	rwDeliver(t, a, strings.Replace(body, "16:02:48.986", "16:01:01.000", -1))
	rwDeliver(t, a, strings.Replace(body, "16:02:48.986", "16:02:02.000", -1))
	fm.texts = nil
	rwDeliver(t, a, strings.Replace(body, `"blocked": true`, `"blocked": false`, 1))

	// Сам отчёт-не-нарушение в журнал не идёт, поэтому счётчик показывает две
	// прошлые записи — но именно их, а не «1-е».
	if countContains(fm.texts, "2-е за 30 дней") != 1 {
		t.Fatalf("счётчик должен видеть прошлые нарушения: %v", fm.texts)
	}
}

// Старые кнопки с экрана вебхуков уводят на новый экран, а не молчат.
func TestTorrentSection_OldWebhookButtonsRedirect(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()

	for _, old := range []string{"wh:tadm", "wh:tusr"} {
		fm.cbData, fm.texts = nil, nil
		a.handleCallback(context.Background(), cb(100, old))
		if !hasCB(fm.allCallbackData(), "torj:strike") {
			t.Fatalf("%s не привёл на экран торрентов: %v", old, fm.allCallbackData())
		}
	}
}

// Отчёты, пришедшие в паузу после срабатывания, выпадают из следующего окна:
// иначе накопленный хвост отключал бы подписку с первого же нарушения после
// того, как админ вернул доступ.
func TestTorrentStrikes_GraceTailDoesNotCount(t *testing.T) {
	a, _, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 2
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentReportN(1))
	rwDeliver(t, a, torrentReportN(2))
	if !stub.disabledTG {
		t.Fatalf("подписка должна быть отключена")
	}
	// Хвост отчётов внутри паузы: подписку они не трогают.
	for i := 3; i < 6; i++ {
		rwDeliver(t, a, torrentReportN(i))
	}

	// Прошли сутки: пауза давно истекла, бот перезапущен, админ вернул доступ.
	// Хвостовые отчёты датируем ВНУТРИ часовой паузы после срабатывания — они
	// должны выпасть из нового окна. Со сдвигом «отметка + секунда» они бы в
	// него попали, и одного свежего нарушения хватило бы на отключение.
	now := time.Now().UTC()
	struck := now.Add(-24 * time.Hour)
	stub.disabledTG = false
	a.thrMu.Lock()
	a.torSeen, a.torUnbSeen, a.torStrikeSeen = nil, nil, nil
	a.thrMu.Unlock()
	for i := range fs.torrents {
		fs.torrents[i].CreatedAt = struck.Add(5 * time.Minute).Format(time.RFC3339)
	}
	fs.strikes[42] = struck.Format(time.RFC3339)

	rwDeliver(t, a, torrentReportN(10))
	if stub.disabledTG {
		t.Fatalf("одного нарушения при пороге 2 мало — хвост паузы считаться не должен")
	}
	rwDeliver(t, a, torrentReportN(11))
	if !stub.disabledTG {
		t.Fatalf("двух новых нарушений достаточно")
	}
}

// Отметка страйка из будущего (часы сервера съехали) не должна навсегда
// усыплять политику.
func TestTorrentStrikes_FutureMarkSelfHeals(t *testing.T) {
	a, _, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
	stub := newTorrentPanelStub(t)
	stub.attach(a)
	fs.strikes = map[int64]string{42: time.Now().UTC().Add(72 * time.Hour).Format(time.RFC3339)}

	rwDeliver(t, a, torrentBody)
	if at, _ := fs.TorrentStrikeAt(ctxBG(), 42); at == "" || at > time.Now().UTC().Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("отметка не выправлена: %q", at)
	}
}

// Нечитаемая отметка больше не глушит сообщения о снятии блокировки: статус
// подписки бот спрашивает у панели, а не выводит из отметки.
func TestTorrentUnblock_BrokenStrikeMarkDoesNotSilence(t *testing.T) {
	a, fm, fs := torrentApp(t)
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody)
	fs.strikes = map[int64]string{42: "не-дата"}
	fm.texts = nil
	for i := range fs.torrents {
		fs.torrents[i].WillUnblockAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	}
	a.torrentUnblockOnce(ctxBG())

	if countContains(fm.texts, "Блокировка снята") != 1 {
		t.Fatalf("подписка активна — сообщение должно уйти: %v", fm.texts)
	}
}

// Отчёт, пришедший в паузу после срабатывания, не мешает сообщению о снятии:
// подписка к тому моменту может быть уже возвращена админом.
func TestTorrentUnblock_TailReportAfterStrike(t *testing.T) {
	a, fm, fs := torrentApp(t)
	a.botCfg.Torrent.StrikeLimit = 1
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentReportN(1)) // срабатывание, подписка отключена
	rwDeliver(t, a, torrentReportN(2)) // хвост внутри паузы
	stub.disabledTG = false            // админ вернул доступ
	fm.texts = nil
	a.thrMu.Lock()
	a.torUnbSeen = nil
	a.thrMu.Unlock()
	for i := range fs.torrents {
		fs.torrents[i].WillUnblockAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	}
	a.torrentUnblockOnce(ctxBG())

	if countContains(fm.texts, "Блокировка снята") != 1 {
		t.Fatalf("доступ вернули — сообщение должно уйти ровно один раз: %v", fm.texts)
	}
}

// Два ожидания ввода не должны перебивать друг друга: незакрытое ожидание
// текста разблокировки съедало ответ на вопрос про порог, а незакрытое
// ожидание порога превращало любую реплику админа в «выключить политику».
func TestTorrentAdmin_InputModesDoNotCollide(t *testing.T) {
	a, _, _ := torrentApp(t)
	ctx := context.Background()

	// Начали править текст, ушли на ввод порога — текст ждать перестаём.
	a.handleCallback(ctx, cb(100, "torj:edit"))
	a.handleCallback(ctx, cb(100, "torj:strike"))
	a.handleMessage(ctx, msgText(100, "3"))
	if a.botCfg.Torrent.StrikeLimit != 3 {
		t.Fatalf("порог не сохранён: %+v", a.botCfg.Torrent)
	}
	if a.botCfg.Torrent.UnblockText != "" {
		t.Fatalf("число ушло в текст разблокировки: %q", a.botCfg.Torrent.UnblockText)
	}

	// Начали ввод порога, ушли править текст — порог ждать перестаём.
	a.handleCallback(ctx, cb(100, "torj:strike"))
	a.handleCallback(ctx, cb(100, "torj:edit"))
	a.handleMessage(ctx, msgText(100, "Свободны!"))
	a.handleMessage(ctx, msgText(100, "привет"))
	if a.botCfg.Torrent.StrikeLimit != 3 {
		t.Fatalf("случайная реплика обнулила порог: %+v", a.botCfg.Torrent)
	}
	if a.botCfg.Torrent.UnblockText != "Свободны!" {
		t.Fatalf("текст не сохранён: %q", a.botCfg.Torrent.UnblockText)
	}
}

// Нечисловой ввод порога не должен молча выключать политику.
func TestTorrentAdmin_StrikeRejectsGarbage(t *testing.T) {
	a, fm, _ := torrentApp(t)
	ctx := context.Background()
	a.botCfg.Torrent.StrikeLimit = 5

	a.handleCallback(ctx, cb(100, "torj:strike"))
	a.handleMessage(ctx, msgText(100, "много"))
	if a.botCfg.Torrent.StrikeLimit != 5 {
		t.Fatalf("порог сброшен опечаткой: %+v", a.botCfg.Torrent)
	}
	if countContains(fm.texts, "Нужно число") != 1 {
		t.Fatalf("админ не предупреждён: %v", fm.texts)
	}
}

// Ручное снятие блокировки подчиняется той же паузе, что и тикер: три нажатия
// подряд не должны родить три одинаковых «блокировка снята».
func TestTorrentUnblock_ManualRespectsCooldown(t *testing.T) {
	a, fm, fs := torrentApp(t)
	ctx := context.Background()
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	for _, ip := range []string{"203.0.113.7", "203.0.113.8", "203.0.113.9"} {
		rwDeliver(t, a, strings.Replace(torrentBody, "203.0.113.7", ip, -1))
	}
	if len(fs.torrents) != 3 {
		t.Fatalf("ожидались три записи: %d", len(fs.torrents))
	}
	fm.texts = nil
	a.thrMu.Lock()
	a.torUnbSeen = nil
	a.thrMu.Unlock()

	for _, ip := range []string{"203.0.113.7", "203.0.113.8", "203.0.113.9"} {
		a.handleCallback(ctx, cb(100, "torj:unb:"+ip))
	}
	if n := countContains(fm.texts, "Блокировка снята"); n != 1 {
		t.Fatalf("пользователю ушло %d сообщений вместо одного: %v", n, fm.texts)
	}
}

// Ручное снятие тоже спрашивает панель: при отключённой подписке молчим.
func TestTorrentUnblock_ManualSilentWhenSubDisabled(t *testing.T) {
	a, fm, _ := torrentApp(t)
	ctx := context.Background()
	a.botCfg.Torrent.StrikeLimit = 1
	stub := newTorrentPanelStub(t)
	stub.attach(a)

	rwDeliver(t, a, torrentBody) // сработала автоблокировка
	fm.texts = nil
	a.thrMu.Lock()
	a.torUnbSeen = nil
	a.thrMu.Unlock()

	a.handleCallback(ctx, cb(100, "torj:unb:203.0.113.7"))
	if countContains(fm.texts, "Блокировка снята") != 0 {
		t.Fatalf("подписка отключена — сообщение уходить не должно: %v", fm.texts)
	}
}

// Панель не отвечает: статус подписки неизвестен, обещать восстановление
// доступа нельзя.
func TestTorrentUnblock_SilentWhenPanelUnknown(t *testing.T) {
	a, fm, fs := torrentApp(t)
	stub := newTorrentPanelStub(t)
	stub.attach(a)
	rwDeliver(t, a, torrentBody)

	// Панель «пропала»: пользователя в ней нет.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[]}`))
	}))
	defer srv.Close()
	a.panel = remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})

	fm.texts = nil
	for i := range fs.torrents {
		fs.torrents[i].WillUnblockAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	}
	a.torrentUnblockOnce(ctxBG())

	if countContains(fm.texts, "Блокировка снята") != 0 {
		t.Fatalf("статус неизвестен — молчим: %v", fm.texts)
	}
	if !fs.torrents[0].UnblockNotified {
		t.Fatalf("запись должна закрыться, иначе тикер крутит её вечно")
	}
}

// Панель переотправила вебхук (не дождалась 200) — в журнале должна остаться
// одна запись, иначе дубли ускоряют набор страйков.
func TestTorrentWebhook_RetryIsIdempotent(t *testing.T) {
	a, _, fs := torrentApp(t)
	for i := 0; i < 3; i++ {
		rwDeliver(t, a, torrentBody)
	}
	if len(fs.torrents) != 1 {
		t.Fatalf("ожидалась одна запись, есть %d", len(fs.torrents))
	}
	// Другой инцидент того же пользователя журнал различает.
	rwDeliver(t, a, strings.Replace(torrentBody, "16:02:48.986", "16:09:11.100", -1))
	if len(fs.torrents) != 2 {
		t.Fatalf("разные инциденты склеились: %d", len(fs.torrents))
	}
}

// Отчёт без блокировки не должен выглядеть как блокировка: ни срока, ни
// кнопки снятия того, чего не было.
func TestTorrentWebhook_NotBlockedCardHasNoDurationOrUnblock(t *testing.T) {
	a, fm, _ := torrentApp(t)
	rwDeliver(t, a, strings.Replace(torrentBody, `"blocked": true`, `"blocked": false`, 1))

	if countContains(fm.texts, "не применялась") != 1 {
		t.Fatalf("срок блокировки показан как настоящий: %v", fm.texts)
	}
	for _, d := range fm.allCallbackData() {
		if strings.HasPrefix(d, "torj:unb:") {
			t.Fatalf("кнопка снятия не должна появиться: %q", d)
		}
	}
}

// Панель не прислала срок разблокировки — обещать сообщение о снятии нельзя,
// оно уже помечено отправленным и не придёт никогда.
func TestTorrentWebhook_NoUnblockTimeNoPromise(t *testing.T) {
	a, fm, fs := torrentApp(t)
	body := strings.Replace(torrentBody, `"blockDuration": 3600`, `"blockDuration": 0`, 1)
	body = strings.Replace(body, `"willUnblockAt": "2026-03-07T17:02:48.986Z",`, "", 1)

	rwDeliver(t, a, body)
	if countContains(fm.texts, "О снятии блокировки придёт") != 0 {
		t.Fatalf("обещание, которое не выполнится: %v", fm.texts)
	}
	if !fs.torrents[0].UnblockNotified {
		t.Fatalf("без срока запись должна быть закрыта сразу: %+v", fs.torrents[0])
	}

	fm.texts = nil
	a.thrMu.Lock()
	a.torSeen = nil // снимаем 10-минутную паузу между предупреждениями
	a.thrMu.Unlock()
	rwDeliver(t, a, strings.Replace(torrentBody, "16:02:48.986", "16:30:00.000", -1))
	if countContains(fm.texts, "О снятии блокировки придёт") != 1 {
		t.Fatalf("со сроком обещание должно быть: %v", fm.texts)
	}
}

// Отчёт, в котором пользователя не опознать, обрабатывать нечего.
func TestTorrentWebhook_UnidentifiedReportDropped(t *testing.T) {
	a, fm, fs := torrentApp(t)
	body := strings.Replace(torrentBody, `"telegramId": 42, `, "", 1)
	body = strings.Replace(body, `"username": "tg_42", `, "", 1)
	body = strings.Replace(body, `"userId": "2",`, `"userId": "",`, 1)
	body = strings.Replace(body, `"email": "2",`, `"email": "",`, 1)

	rwDeliver(t, a, body)
	if len(fs.torrents) != 0 || len(fm.texts) != 0 {
		t.Fatalf("отчёт ни о ком должен отбрасываться: %+v %v", fs.torrents, fm.texts)
	}
}

// Журнал по одному нарушителю листается: страницы, а не одна и та же.
func TestTorrentUserLog_Pagination(t *testing.T) {
	a, fm, fs := torrentApp(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		rwDeliver(t, a, strings.Replace(torrentBody, "16:02:48.986",
			"16:0"+strconv.Itoa(i%10)+":"+strconv.Itoa(10+i)+".000", -1))
	}
	if len(fs.torrents) < 11 {
		t.Fatalf("мало записей для пагинации: %d", len(fs.torrents))
	}
	fm.cbData, fm.texts = nil, nil

	a.handleCallback(ctx, cb(100, "torj:u:42"))
	if !hasCB(fm.allCallbackData(), "torj:up:42:1") {
		t.Fatalf("нет перехода на вторую страницу: %v", fm.allCallbackData())
	}
	first := fm.last()
	fm.cbData, fm.texts = nil, nil
	a.handleCallback(ctx, cb(100, "torj:up:42:1"))
	if fm.last() == first {
		t.Fatalf("вторая страница совпала с первой")
	}
	if !hasCB(fm.allCallbackData(), "torj:up:42:0") {
		t.Fatalf("нет возврата на первую страницу: %v", fm.allCallbackData())
	}
}

// Сбой пометки в БД не должен съедать обещанное «блокировка снята»: пауза
// откатывается, и следующий тик доставляет сообщение.
func TestTorrentUnblock_MarkFailureKeepsPromise(t *testing.T) {
	a, fm, fs := torrentApp(t)
	ctx := context.Background()

	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	_ = fs.AddTorrentReport(ctx, &model.TorrentReport{TelegramID: 42, WillUnblockAt: past})
	fs.failMark = 1

	a.torrentUnblockOnce(ctx)
	if countContains(fm.texts, "Блокировка снята") != 0 || fs.torrents[0].UnblockNotified {
		t.Fatalf("при сбое пометки слать и помечать нельзя: %v %+v", fm.texts, fs.torrents[0])
	}

	a.torrentUnblockOnce(ctx)
	if countContains(fm.texts, "Блокировка снята") != 1 || !fs.torrents[0].UnblockNotified {
		t.Fatalf("после восстановления БД сообщение должно дойти: %v %+v", fm.texts, fs.torrents[0])
	}
}

// Экран «Торренты» предупреждает, что без секрета вебхука отчёты
// отбрасываются, и убирает предупреждение после настройки секрета.
func TestTorrentAdmin_SecretHint(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	ctx := context.Background()
	a.botCfg = &model.BotConfig{Installed: true}
	a.botCfg.NormalizeTorrent()

	a.handleCallback(ctx, cb(100, "torj:home"))
	if countContains(fm.texts, "Не задан секрет вебхука") != 1 {
		t.Fatalf("без секрета должна быть подсказка: %v", fm.texts)
	}

	a.botCfg.Webhook.RemnawaveSecret = "s"
	fm.texts = nil
	a.handleCallback(ctx, cb(100, "torj:home"))
	if countContains(fm.texts, "Не задан секрет вебхука") != 0 {
		t.Fatalf("с секретом подсказки быть не должно: %v", fm.texts)
	}
}
