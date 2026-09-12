package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remnabot/internal/model"
	"remnabot/internal/remnawave"
)

// devicesPanel — панель, отдающая заданный список HWID-устройств.
func devicesPanel(t *testing.T, devicesJSON string, total int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/users/by-telegram-id/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":[{"uuid":"u-1","telegramId":42,"status":"ACTIVE","hwidDeviceLimit":3,"subscriptionUrl":"https://s/ex","expireAt":"2030-01-02T03:04:05Z"}]}`))
	})
	mux.HandleFunc("/api/hwid/devices/u-1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"total":` + itoa(total) + `,"devices":[` + devicesJSON + `]}}`))
	})
	return httptest.NewServer(mux)
}

func devicesApp(base string, cfg model.DevicesConfig) *App {
	return &App{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		panel:  remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: base, APIToken: "t"}),
		botCfg: &model.BotConfig{Language: model.LangRU, Devices: cfg},
	}
}

const twoDevices = `{"hwid":"aaaabbbbccccdddd1111","userId":1,"platform":"iOS","osVersion":"18.2","deviceModel":"iPhone 15","userAgent":"Happ/2.9.1","requestIp":null,"createdAt":"2026-09-01T10:00:00Z","updatedAt":"2026-09-05T10:00:00Z"},` +
	`{"hwid":"eeeeffff000011112222","userId":1,"platform":"Windows","osVersion":null,"deviceModel":null,"userAgent":"v2rayNG/1.9.16","requestIp":null,"createdAt":"2026-09-02T10:00:00Z","updatedAt":"2026-09-09T10:00:00Z"}`

// Панель отдаёт список — клиент обязан его разобрать, пропустить запись без
// отпечатка и положить сверху то, что заходило последним.
func TestDevicesByTelegramID_ParsesList(t *testing.T) {
	srv := devicesPanel(t, twoDevices+`,{"hwid":"","userId":1,"platform":null,"osVersion":null,"deviceModel":null,"userAgent":null,"requestIp":null,"createdAt":"2026-09-03T10:00:00Z","updatedAt":"2026-09-03T10:00:00Z"}`, 3)
	defer srv.Close()
	cl := remnawave.New(model.PanelConfig{Mode: model.ModeRemote, BaseURL: srv.URL, APIToken: "t"})
	info, ok := cl.DevicesByTelegramID(context.Background(), 42)
	if !ok {
		t.Fatal("панель не ответила")
	}
	if info.Used != 3 || info.Limit != 3 || !info.HasLimit {
		t.Fatalf("счётчик: %+v", info)
	}
	if len(info.List) != 2 {
		t.Fatalf("устройств в списке %d, ждали 2 (запись без отпечатка пропускается)", len(info.List))
	}
	if info.List[0].Platform != "Windows" {
		t.Fatalf("сверху должно быть последнее заходившее, а там %q", info.List[0].Platform)
	}
	if info.List[0].UserAgent != "v2rayNG/1.9.16" {
		t.Fatalf("подпись приложения не разобрана: %+v", info.List[0])
	}
	if info.List[1].Model != "iPhone 15" || info.List[1].OSVersion != "18.2" {
		t.Fatalf("поля разобраны неверно: %+v", info.List[1])
	}
	if info.List[1].FirstSeen.IsZero() || info.List[1].LastSeen.IsZero() {
		t.Fatalf("даты не разобраны: %+v", info.List[1])
	}
}

// Набор полей выбирает админ: выключенное поле не должно просачиваться.
func TestDeviceParts_Fields(t *testing.T) {
	d := remnawave.Device{
		HWID:      "aaaabbbbccccdddd1111",
		Platform:  "iOS",
		OSVersion: "18.2",
		Model:     "iPhone 15",
		UserAgent: "Happ/2.9.1",
		FirstSeen: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		LastSeen:  time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
	}
	head, meta := deviceParts(model.LangRU, d, model.DevicesConfig{Model: true, Platform: true, UA: true, HWID: true, Dates: true})
	if head != "iPhone 15 · Happ/2.9.1" || !strings.Contains(meta, "iOS 18.2") || !strings.Contains(meta, "aaaa…1111") || !strings.Contains(meta, "подключено 01.09.2026") {
		t.Fatalf("полный набор: head=%q meta=%q", head, meta)
	}
	head, meta = deviceParts(model.LangRU, d, model.DevicesConfig{Platform: true})
	if head != "iOS 18.2" || meta != "" {
		t.Fatalf("только платформа: head=%q meta=%q", head, meta)
	}
	if _, meta := deviceParts(model.LangRU, d, model.DevicesConfig{Model: true}); meta != "" {
		t.Fatalf("модель включена, остальное нет, а приписка не пуста: %q", meta)
	}
	// Модели нет, а подпись приложения есть — она и становится заголовком:
	// ради этого поле и добавлено.
	noModel := remnawave.Device{HWID: "zzzz", Platform: "Android", UserAgent: "v2rayNG/1.9.16"}
	if head, _ := deviceParts(model.LangRU, noModel, model.DevicesConfig{Model: true, UA: true, Platform: true}); head != "v2rayNG/1.9.16" {
		t.Fatalf("без модели заголовком должно быть приложение, а там %q", head)
	}
	if head, _ := deviceParts(model.LangRU, noModel, model.DevicesConfig{Model: true, Platform: true}); head != "Android" {
		t.Fatalf("приложение выключено, а просочилось: %q", head)
	}
	// Клиент не прислал ничего, а отпечаток скрыт — строка всё равно нужна.
	empty := remnawave.Device{HWID: "zzzz"}
	if head, _ := deviceParts(model.LangRU, empty, model.DevicesConfig{Model: true, Platform: true, Dates: true}); head == "" {
		t.Fatal("пустое устройство осталось без заголовка")
	}
	// Чужой текст в модели не должен уезжать в разметку как есть.
	evil := remnawave.Device{HWID: "zzzz", Model: "<b>hack</b>"}
	if line := deviceLine(model.LangRU, evil, model.DevicesConfig{Model: true}); strings.Contains(line, "<b>hack") {
		t.Fatalf("разметка клиента не экранирована: %q", line)
	}
}

// Длинную модель обрезаем: заголовок с клиента не должен занимать экран.
func TestDeviceParts_CutsLongField(t *testing.T) {
	long := strings.Repeat("я", 200)
	head, _ := deviceParts(model.LangRU, remnawave.Device{HWID: "z", Model: long}, model.DevicesConfig{Model: true})
	if len([]rune(head)) > deviceFieldMaxLen+1 {
		t.Fatalf("заголовок длиной %d знаков", len([]rune(head)))
	}
}

// На экране подписки остаётся счётчик и подсказка: сам список живёт на
// отдельном экране, потому что подпись под баннером кончается на 1000 знаках.
func TestDevicesLine_CounterAndHint(t *testing.T) {
	srv := devicesPanel(t, twoDevices, 2)
	defer srv.Close()

	on := model.DevicesConfig{List: true, Platform: true, Model: true, UA: true, HWID: true, Dates: true, Init: true}
	a := devicesApp(srv.URL, on)
	line, has := a.devicesLine(context.Background(), 42, a.panel)
	if !strings.Contains(line, "Устройства: <b>2 / 3</b>") {
		t.Fatalf("счётчик пропал: %q", line)
	}
	if !has || !strings.Contains(line, "Мои устройства") {
		t.Fatalf("подсказки про кнопку нет: %q (кнопка=%v)", line, has)
	}
	if strings.Contains(line, "•") {
		t.Fatalf("список уехал на экран подписки: %q", line)
	}

	// Тумблер списка выключен — ни подсказки, ни кнопки, экран как раньше.
	off := devicesApp(srv.URL, model.DevicesConfig{List: false, Platform: true, Init: true})
	line, has = off.devicesLine(context.Background(), 42, off.panel)
	if has || strings.Contains(line, "Мои устройства") {
		t.Fatalf("кнопка предложена при выключенном списке: %q (%v)", line, has)
	}
	// Список включён, но ни одного поля не выбрано — показывать нечего.
	bare := devicesApp(srv.URL, model.DevicesConfig{List: true, Init: true})
	if _, has := bare.devicesLine(context.Background(), 42, bare.panel); has {
		t.Fatal("кнопка предложена без единого поля")
	}
}

// Экран устройств не обрезает список: что не влезло в сообщение, уходит
// следующим, и разрыв всегда по границе устройства.
func TestDevicePages_SplitsWholeList(t *testing.T) {
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "• <b>Устройство "+itoa(i)+"</b>\n   <i>Windows 11 · подключено 01.09.2026</i>")
	}
	parts := devicePages("ЗАГОЛОВОК", lines, "приписка")
	if len(parts) < 2 {
		t.Fatalf("длинный список уместился в %d сообщение — нарезки нет", len(parts))
	}
	joined := strings.Join(parts, "\n")
	for _, l := range lines {
		if !strings.Contains(joined, l) {
			t.Fatalf("устройство потерялось при нарезке: %q", l)
		}
	}
	if !strings.HasPrefix(parts[0], "ЗАГОЛОВОК") {
		t.Fatalf("первая часть без заголовка: %q", parts[0][:40])
	}
	if !strings.Contains(parts[len(parts)-1], "приписка") {
		t.Fatal("приписка не доехала до последней части")
	}
	for i, p := range parts {
		if n := len([]rune(p)); n > 4096 {
			t.Fatalf("часть %d на %d знаков — Telegram не примет", i, n)
		}
	}
	// Короткий список остаётся одним сообщением.
	if got := devicePages("ЗАГОЛОВОК", lines[:2], ""); len(got) != 1 {
		t.Fatalf("два устройства разъехались на %d сообщения", len(got))
	}
}

// Мини-апп и кабинет получают тот же список и по тем же правилам.
func TestMiniSubscription_Devices(t *testing.T) {
	srv := devicesPanel(t, twoDevices, 2)
	defer srv.Close()

	a := devicesApp(srv.URL, model.DevicesConfig{List: true, Model: true, Platform: true, Init: true})
	dto := a.MiniSubscription(context.Background(), 42)
	if !dto.DevicesOK || dto.DevicesUsed != 2 {
		t.Fatalf("счётчик: %+v", dto)
	}
	if len(dto.Devices) != 2 || dto.Devices[0].Name == "" {
		t.Fatalf("устройства не доехали: %+v", dto.Devices)
	}
	off := devicesApp(srv.URL, model.DevicesConfig{List: false, Model: true, Init: true})
	if dto := off.MiniSubscription(context.Background(), 42); len(dto.Devices) != 0 {
		t.Fatalf("обзор выключен, а устройства отданы: %+v", dto.Devices)
	}
}

// Обновление с версии без настройки: обзор включается, отпечаток остаётся
// скрытым, повторная нормализация чужой выбор не переписывает.
func TestNormalizeDevices(t *testing.T) {
	var c model.BotConfig
	c.NormalizeDevices()
	if !c.Devices.List || !c.Devices.Platform || !c.Devices.Model || !c.Devices.UA || !c.Devices.Dates {
		t.Fatalf("по умолчанию: %+v", c.Devices)
	}
	if c.Devices.HWID {
		t.Fatal("отпечаток не должен включаться сам")
	}
	if !c.Devices.Show() {
		t.Fatal("обзор с полями обязан показываться")
	}
	c.Devices.Platform, c.Devices.Model, c.Devices.UA, c.Devices.Dates = false, false, false, false
	if c.Devices.Show() {
		t.Fatal("без единого поля обзор показывать нечем")
	}
	c.Devices.List = false
	c.NormalizeDevices()
	if c.Devices.List {
		t.Fatal("нормализация переписала выбор владельца")
	}
}

// Установки, успевшие обновиться на сборку без поля «приложение», прошли
// нормализацию с уже выставленным Init: приложение им включается разово.
func TestNormalizeDevices_TurnsOnAppOnce(t *testing.T) {
	c := model.BotConfig{Devices: model.DevicesConfig{
		List: true, Platform: true, Model: true, Dates: true, Init: true,
	}}
	c.NormalizeDevices()
	if !c.Devices.UA || !c.Devices.UAInit {
		t.Fatalf("приложение не включилось: %+v", c.Devices)
	}
	// Владелец выключил его сам — второй раз не включаем.
	c.Devices.UA = false
	c.NormalizeDevices()
	if c.Devices.UA {
		t.Fatal("нормализация включила приложение поверх выбора владельца")
	}
}
