package app

import (
	"context"
	"strings"
	"testing"

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
