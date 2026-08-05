package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"remnabot/internal/model"
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
