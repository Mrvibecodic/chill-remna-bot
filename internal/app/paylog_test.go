package app

import (
	"context"
	"strings"
	"testing"

	"remnabot/internal/i18n"
	"remnabot/internal/model"
)

// Все способы оплаты, кроме P2P, обязаны писать в журнал СБОИ — иначе разбор
// жалобы «оплата не прошла» упирается в догадки. Тест фиксирует, что этапы,
// которыми пользуются платёжные модули, классифицируются как ошибки и попадают
// в выгрузку «только ошибки».
func TestPayErrorStagesCoverAllGateways(t *testing.T) {
	mustBeError := []string{
		"invoice_error",   // не удалось выставить счёт (все шлюзы)
		"checkout_error",  // отказ на оплате из мини-аппа или кабинета
		"topup_error",     // отказ при пополнении баланса
		"verify_error",    // не удалось перепроверить платёж через API шлюза
		"sign_error",      // подпись вебхука не сошлась (CryptoBot, Tribute)
		"sign_mismatch",   // то же для Heleket
		"panel_error",     // деньги приняты, панель не выдала подписку
		"finalize_error",  // сбой выдачи после успешной оплаты
		"reconcile_error", // реконсилятор не смог опросить шлюз
		"reconcile_giveup",
		"autocharge_error", // автосписание ЮKassa
		"autopay_disabled",
		"precheckout_rejected", // Stars
		"error",
	}
	for _, st := range mustBeError {
		if !payErrorStages[st] {
			t.Fatalf("этап %q не считается сбоем — он не попадёт в выгрузку ошибок", st)
		}
	}
	// Успешные этапы не должны засорять выгрузку сбоев.
	for _, st := range []string{"invoice_created", "webhook", "verified", "manual_check", "reconcile", "duplicate", "topup_credited", "finalize", "panel_ok", "done"} {
		if payErrorStages[st] {
			t.Fatalf("этап %q ошибочно помечен сбоем", st)
		}
	}
}

func TestExportPayErrorsFiltersAndCounts(t *testing.T) {
	a, fm, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	a.payLog(ctx, model.PayMethodHeleket, "hl:u-1", 555, "invoice_created", "purchase months=1")
	a.payLog(ctx, model.PayMethodHeleket, "hl:u-2", 555, "invoice_error", "запрос отклонён (amount: validation.min)")
	a.payLog(ctx, model.PayMethodYooKassa, "yk-1", 777, "panel_error", "панель недоступна")
	a.payLog(ctx, model.PayMethodTribute, "", 0, "sign_error", "подпись вебхука не сошлась")
	a.payLog(ctx, model.PayMethodCryptoBot, "cb:1", 555, "webhook", "invoice_paid")

	all, _ := fs.AllPayLogs(ctx, 100)
	doc, n := buildPayErrorReport(all, "now")
	if n != 3 {
		t.Fatalf("ожидалось 3 сбоя, получено %d:\n%s", n, doc)
	}
	a.exportPayErrors(ctx, 100)
	if !strings.Contains(fm.joined(), "DOC:payment_errors.log") {
		t.Fatalf("файл со сбоями не отправлен админу:\n%s", fm.joined())
	}
	for _, want := range []string{"invoice_error", "panel_error", "sign_error", "validation.min"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("в выгрузке нет %q:\n%s", want, doc)
		}
	}
	for _, notWant := range []string{"invoice_created", "invoice_paid"} {
		if strings.Contains(doc, notWant) {
			t.Fatalf("в выгрузку сбоев попал успешный этап %q:\n%s", notWant, doc)
		}
	}
	// Сводка по способам оплаты — чтобы сразу видеть, какой шлюз сыплется.
	if !strings.Contains(doc, "heleket: 1") || !strings.Contains(doc, "yookassa: 1") || !strings.Contains(doc, "tribute: 1") {
		t.Fatalf("нет сводки по способам оплаты:\n%s", doc)
	}
}

// Неудачная попытка оплаты из мини-аппа и из веб-кабинета должна оставлять
// след с указанием источника.
func TestMiniCheckoutFailureIsLogged(t *testing.T) {
	a, _, fs := newTestApp(t)
	a.store = fs
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	ctx := context.Background()

	if _, _, err := a.miniPayURL(ctx, 555, 1, "нетакогометода", false); err == nil {
		t.Fatal("ожидалась ошибка на неизвестном способе оплаты")
	}
	if _, _, err := a.miniPayURL(ctx, 555, 1, model.PayMethodHeleket, true); err == nil {
		t.Fatal("ожидалась ошибка: Heleket выключен")
	}

	entries, _ := fs.PayLogs(ctx, "", 555, 100)
	var sources []string
	for _, e := range entries {
		if e.Stage == "checkout_error" {
			sources = append(sources, e.Detail)
		}
	}
	if len(sources) != 2 {
		t.Fatalf("ожидалось 2 записи checkout_error, получено %d: %+v", len(sources), entries)
	}
	joined := strings.Join(sources, "\n")
	if !strings.Contains(joined, "мини-апп") || !strings.Contains(joined, "веб-кабинет") {
		t.Fatalf("в записях не виден источник оплаты:\n%s", joined)
	}
}

func TestPayLogScreenPlaceholders(t *testing.T) {
	for _, lang := range []string{model.LangRU, model.LangEN} {
		for key, got := range map[string]string{
			"paylog.errors_caption": i18n.T(lang, "paylog.errors_caption", 7),
			"paylog.no_errors":      i18n.T(lang, "paylog.no_errors"),
			"paylog.btn_errors":     i18n.T(lang, "paylog.btn_errors"),
			"paylog.csv_caption":    i18n.T(lang, "paylog.csv_caption", 7),
		} {
			if strings.Contains(got, "%!") || strings.Contains(got, "MISSING") {
				t.Fatalf("битый шаблон %s (%s): %q", key, lang, got)
			}
		}
	}
}
