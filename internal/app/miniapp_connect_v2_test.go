package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseV2Sample(t *testing.T) {
	body := []byte(`{
	  "version":"1","locales":["en"],
	  "platforms":{
	    "ios":{"apps":[
	      {"name":"App One","featured":true,"blocks":[
	        {"description":{"en":"block-store"},"buttons":[{"link":"https://example.com/store/app-one","type":"external","text":{"en":"store-btn"}}]},
	        {"description":{"en":"block-add"},"buttons":[{"link":"appone://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"en":"add-btn"}}]}
	      ]},
	      {"name":"App Two","featured":false,"blocks":[
	        {"buttons":[{"link":"apptwo://add/{{SUBSCRIPTION_LINK}}#{{USERNAME}}","type":"subscriptionLink","text":{"en":"add-btn"}}]}
	      ]}
	    ]},
	    "android":{"apps":[]}
	  }
	}`)
	var v2 appConfigV2
	if err := json.Unmarshal(body, &v2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(v2.Platforms["ios"].Apps) != 2 {
		t.Fatalf("ios apps = %d, want 2", len(v2.Platforms["ios"].Apps))
	}
	sub := "https://sub.example.com/AbCd"
	out := acBuildV2(v2.Platforms["ios"].Apps, sub, "tg_99", "en")
	if len(out) != 2 {
		t.Fatalf("built %d apps, want 2", len(out))
	}
	if !out[0].Featured || out[0].Name != "App One" {
		t.Errorf("featured-first failed: %+v", out[0])
	}
	if out[0].Deeplink != "appone://add/"+sub {
		t.Errorf("deeplink = %q", out[0].Deeplink)
	}
	if len(out[0].Installs) != 1 || out[0].Installs[0].Text != "store-btn" {
		t.Errorf("installs = %+v", out[0].Installs)
	}
	if out[0].AddDesc != "block-add" {
		t.Errorf("add_desc = %q", out[0].AddDesc)
	}
	if out[1].Deeplink != "apptwo://add/"+sub+"#tg_99" {
		t.Errorf("deeplink2 = %q", out[1].Deeplink)
	}
}

// Блоки после добавления подписки — отдельные разделы инструкции: название,
// текст без разметки и кнопки (копирование, ссылка). Верх карточки приложения —
// добавление и установка — остаётся как был.
func TestV2MoreBlocks(t *testing.T) {
	body := []byte(`{
	  "platforms":{"ios":{"displayName":{"en":"iOS"},"svgIconKey":"i","apps":[
	    {"name":"App One","featured":true,"blocks":[
	      {"svgIconKey":"d","svgIconColor":"cyan","title":{"ru":"Установка приложения"},"description":{"ru":"Установите."},
	       "buttons":[{"link":"https://example.com/store","type":"external","text":{"ru":"Магазин"},"svgIconKey":"e"},
	                  {"link":" JavaScript:alert(1)","type":"external","text":{"ru":"Плохая установка"},"svgIconKey":"e"}]},
	      {"svgIconKey":"a","svgIconColor":"cyan","title":{"ru":"Добавление подписки"},"description":{"ru":"Нажмите <b>кнопку</b> ниже"},
	       "buttons":[{"link":"appone://add/{{SUBSCRIPTION_LINK}}","type":"subscriptionLink","text":{"ru":"Добавить"},"svgIconKey":"a"},
	                  {"link":"{{SUBSCRIPTION_LINK}}","type":"copyButton","text":{"ru":"Скопировать вручную"},"svgIconKey":"c"}]},
	      {"svgIconKey":"q","svgIconColor":"cyan","title":{"en":"If not added","ru":"Если подписка <i>не добавилась</i>"},
	       "description":{"ru":"Скопируйте ссылку<br>и вставьте: «+» &gt; «Вставить», 2 > 1"},
	       "buttons":[{"link":"{{SUBSCRIPTION_LINK}}","type":"copyButton","text":{"ru":"Скопировать"},"svgIconKey":"c"},
	                  {"link":"javascript:alert(1)","type":"external","text":{"ru":"Плохая"},"svgIconKey":"e"},
	                  {"link":"https://example.com/help","type":"external","text":{"ru":"Справка"},"svgIconKey":"e"}]},
	      {"svgIconKey":"u","svgIconColor":"cyan","title":{"ru":"Подключение и использование"},"description":{"ru":"Нажмите кнопку включения."},"buttons":[]},
	      {"svgIconKey":"x","svgIconColor":"cyan","title":{"ru":""},"description":{"ru":"без названия"},
	       "buttons":[{"link":"https://example.com/extra","type":"external","text":{"ru":"Ещё"},"svgIconKey":"e"}]}
	    ]},
	    {"name":"Manual","featured":false,"blocks":[
	      {"svgIconKey":"d","svgIconColor":"cyan","title":{"ru":"Установка приложения"},"description":{"ru":"Установите."},
	       "buttons":[{"link":"https://example.com/manual","type":"external","text":{"ru":"Магазин"},"svgIconKey":"e"}]},
	      {"svgIconKey":"a","svgIconColor":"cyan","title":{"ru":"Добавление подписки"},"description":{"ru":"Вставьте ссылку вручную."},
	       "buttons":[{"link":"{{SUBSCRIPTION_LINK}}#{{USERNAME}}","type":"copyButton","text":{"ru":""},"svgIconKey":"c"}]},
	      {"svgIconKey":"r","svgIconColor":"cyan","title":{"ru":"Правила маршрутизации"},"description":{"ru":"Добавьте правила."},
	       "buttons":[{"link":"https://example.com/rules","type":"external","text":{"ru":"Правила"},"svgIconKey":"e"}]}
	    ]},
	    {"name":"Text First","featured":false,"blocks":[
	      {"svgIconKey":"d","svgIconColor":"cyan","title":{"ru":"Установка приложения"},"description":{"ru":"Откройте магазин."},"buttons":[]},
	      {"svgIconKey":"s","svgIconColor":"cyan","title":{"ru":"Магазин"},"description":{"ru":"Ссылка."},
	       "buttons":[{"link":"https://example.com/text-first","type":"external","text":{"ru":"Открыть магазин"},"svgIconKey":"e"}]}
	    ]}
	  ]}}
	}`)
	var v2 appConfigV2
	if err := json.Unmarshal(body, &v2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sub := "https://sub.example.com/AbCd"
	pl := buildV2Platforms(&v2, sub, "tg_99", "ru")
	if len(pl) != 1 || len(pl[0].Apps) != 3 {
		t.Fatalf("платформы: %+v", pl)
	}
	one, manual, textFirst := pl[0].Apps[0], pl[0].Apps[1], pl[0].Apps[2]

	if one.Deeplink != "appone://add/"+sub || one.AddDesc != "Нажмите кнопку ниже" {
		t.Fatalf("добавление подписки: ссылка %q, текст %q", one.Deeplink, one.AddDesc)
	}
	// Блок без названия разделом не становится: его кнопка остаётся среди
	// установочных, как раньше.
	if len(one.Installs) != 2 || one.Installs[0].Text != "Магазин" || one.Installs[1].Text != "Ещё" {
		t.Fatalf("кнопки установки: %+v", one.Installs)
	}
	// Кнопка копирования из блока добавления — отдельной кнопкой у приложения.
	if len(one.Copies) != 1 || !one.Copies[0].Copy || one.Copies[0].URL != sub || one.Copies[0].Text != "Скопировать вручную" {
		t.Fatalf("копирование в основном блоке: %+v", one.Copies)
	}
	if len(one.More) != 2 {
		t.Fatalf("разделов %d, ждали 2: %+v", len(one.More), one.More)
	}
	b := one.More[0]
	if b.Title != "Если подписка не добавилась" || b.Text != "Скопируйте ссылку\nи вставьте: «+» > «Вставить», 2 > 1" {
		t.Fatalf("раздел: %q / %q", b.Title, b.Text)
	}
	if len(b.Buttons) != 2 || !b.Buttons[0].Copy || b.Buttons[0].URL != sub || b.Buttons[0].Text != "Скопировать" {
		t.Fatalf("кнопка копирования: %+v", b.Buttons)
	}
	if b.Buttons[1].Copy || b.Buttons[1].URL != "https://example.com/help" {
		t.Fatalf("кнопка-ссылка: %+v", b.Buttons)
	}
	if u := one.More[1]; u.Title != "Подключение и использование" || u.Text != "Нажмите кнопку включения." || len(u.Buttons) != 0 {
		t.Fatalf("раздел без кнопок: %+v", u)
	}

	// Приложение без кнопки добавления: кнопки-ссылки всех блоков остаются на
	// виду, как раньше, а блок без таких кнопок — раздел с копированием ссылки.
	if manual.Deeplink != "" || len(manual.Installs) != 2 || manual.Installs[0].Text != "Магазин" || manual.Installs[1].Text != "Правила" {
		t.Fatalf("кнопки приложения без кнопки добавления: %+v", manual)
	}
	if len(manual.More) != 1 || manual.More[0].Title != "Добавление подписки" {
		t.Fatalf("разделы приложения без кнопки добавления: %+v", manual.More)
	}
	if mb := manual.More[0].Buttons; len(mb) != 1 || !mb[0].Copy || mb[0].URL != sub+"#tg_99" {
		t.Fatalf("копирование у ручного добавления: %+v", manual.More[0].Buttons)
	}

	// Первый блок — установка: разделом он не становится и без кнопок.
	if len(textFirst.More) != 0 || len(textFirst.Installs) != 1 {
		t.Fatalf("приложение с текстовым первым блоком: %+v", textFirst)
	}

	// Фронт читает разделы по этим именам полей.
	raw, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"more":[{"title":"Если подписка не добавилась"`, `"copy":true`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("в ответе нет %s: %s", want, raw)
		}
	}
}

// Ссылка, исполняющая код в странице, не доходит до кнопок ни в одном формате
// конфига: ни в кнопки установки, ни в добавление подписки.
func TestConnectDropsUnsafeLinks(t *testing.T) {
	sub := "https://sub.example.com/AbCd"
	var v2 appConfigV2
	if err := json.Unmarshal([]byte(`{"platforms":{"ios":{"apps":[
	  {"name":"Bad","featured":false,"blocks":[
	    {"title":{"ru":"Установка"},"description":{"ru":""},"buttons":[
	      {"link":"https://example.com/ok","type":"external","text":{"ru":"Хорошая"}},
	      {"link":"vbscript:x","type":"external","text":{"ru":"Плохая"}}]},
	    {"title":{"ru":"Добавление"},"description":{"ru":""},"buttons":[
	      {"link":"javascript:alert('{{SUBSCRIPTION_LINK}}')","type":"subscriptionLink","text":{"ru":"Добавить"}},
	      {"link":"data:text/html,x","type":"copyButton","text":{"ru":"Копия"}}]},
	    {"title":{"ru":"Раздел без текста"},"description":{"ru":""},"buttons":[
	      {"link":"javascript:alert(2)","type":"external","text":{"ru":"Плохая из раздела"}}]}
	  ]}]}}}`), &v2); err != nil {
		t.Fatal(err)
	}
	apps := acBuildV2(v2.Platforms["ios"].Apps, sub, "tg_99", "ru")
	if len(apps) != 1 {
		t.Fatalf("приложений %d", len(apps))
	}
	a := apps[0]
	if a.Deeplink != "" || len(a.Copies) != 0 || len(a.More) != 0 {
		t.Fatalf("опасная ссылка дошла до страницы: %+v", a)
	}
	if len(a.Installs) != 1 || a.Installs[0].Text != "Хорошая" {
		t.Fatalf("кнопки установки: %+v", a.Installs)
	}

	std := acBuildStd([]acApp{{
		Name: "Old", URLScheme: "javascript:alert(1)//",
		InstallationStep: acStep{Buttons: []acButton{
			{ButtonLink: "https://example.com/old", ButtonText: acLocalized{"ru": "Хорошая"}},
			{ButtonLink: "javascript:alert(3)", ButtonText: acLocalized{"ru": "Плохая"}},
		}},
	}}, sub, "ru")
	if len(std) != 1 || std[0].Deeplink != "" || len(std[0].Installs) != 1 || std[0].Installs[0].Text != "Хорошая" {
		t.Fatalf("старый формат: %+v", std)
	}
}

// Ссылка проверяется так, как её прочтёт браузер: табуляция и перевод строки
// внутри схемы и управляющие символы по краям схему не прячут.
func TestConnectLinkAsBrowserReads(t *testing.T) {
	for _, bad := range []string{
		"", "  \t ", "javascript:alert(1)", "JavaScript:alert(1)", " javascript:alert(1)",
		"java\tscript:alert(1)", "jav\nascript:alert(1)", "java\r\nscript:alert(1)", "\x01javascript:alert(1)",
		"data:text/html,x", "vbscript:x", "file:///etc/passwd",
		"\u00a0javascript:alert(1)", "\ufeffjavascript:alert(1)", "\u2028javascript:alert(1)",
	} {
		if got, ok := acLink(bad); ok {
			t.Errorf("ссылка %q пропущена как %q", bad, got)
		}
	}
	for in, want := range map[string]string{
		" https://example.com/a ":      "https://example.com/a",
		"appone://add/https://s.test/": "appone://add/https://s.test/",
		"tg://resolve?domain=example":  "tg://resolve?domain=example",
		"//example.com/app":            "//example.com/app",
		"/install":                     "/install",
		"https://example.com/\ta":      "https://example.com/a",
		"note: javascript:":            "note: javascript:",
	} {
		if got, ok := acLink(in); !ok || got != want {
			t.Errorf("ссылка %q: %q, %v; ждали %q", in, got, ok, want)
		}
	}
}

// Что показывать разделом, решается по кнопкам, дошедшим до страницы. Кнопка
// добавления с негодной ссылкой не считается: приложение ведёт себя как
// приложение без кнопки добавления.
func TestV2SectionsFollowShownButtons(t *testing.T) {
	sub := "https://sub.example.com/AbCd"
	var v2 appConfigV2
	if err := json.Unmarshal([]byte(`{"platforms":{"ios":{"apps":[
	  {"name":"Broken Add","featured":false,"blocks":[
	    {"title":{"ru":"Установка"},"description":{"ru":"Установите."},"buttons":[
	      {"link":"https://example.com/store","type":"external","text":{"ru":"Магазин"}}]},
	    {"title":{"ru":"Добавление"},"description":{"ru":"Добавьте."},"buttons":[
	      {"link":"java\tscript:alert(1)","type":"subscriptionLink","text":{"ru":"Добавить"}},
	      {"link":"{{SUBSCRIPTION_LINK}}","type":"copyButton","text":{"ru":"A &amp; <b>B</b>"}}]},
	    {"title":{"ru":"Если не добавилась"},"description":{"ru":"Вставьте ссылку."},"buttons":[]}
	  ]},
	  {"name":"Crypt","featured":false,"blocks":[
	    {"title":{"ru":"Установка"},"description":{"ru":"Установите."},"buttons":[
	      {"link":"https://example.com/crypt","type":"external","text":{"ru":"Магазин"}}]},
	    {"title":{"ru":"Добавление"},"description":{"ru":"Добавьте."},"buttons":[
	      {"link":"{{HAPP_CRYPT4_LINK}}","type":"subscriptionLink","text":{"ru":"Добавить"}},
	      {"link":"appx://{{INCY_CRYPT1_LINK}}","type":"copyButton","text":{"ru":"Копия"}},
	      {"link":"{{SUBSCRIPTION_LINK}}","type":"copyButton","text":{"ru":"Ссылка"}}]}
	  ]},
	  {"name":"Crypt Only","featured":false,"blocks":[
	    {"title":{"ru":"Установка"},"description":{"ru":"Установите."},"buttons":[
	      {"link":"https://example.com/only","type":"external","text":{"ru":"Магазин"}}]},
	    {"title":{"ru":"Добавление"},"description":{"ru":"Нажмите кнопку ниже."},"buttons":[
	      {"link":"{{HAPP_CRYPT3_LINK}}","type":"subscriptionLink","text":{"ru":"Добавить"}}]},
	    {"title":{"ru":"Использование"},"description":{"ru":"Включите."},"buttons":[]},
	    {"title":{"ru":"Новое"},"description":{"ru":"Нажмите кнопку."},"buttons":[
	      {"link":"https://example.com/new","type":"newKind","text":{"ru":"Неизвестная"}}]}
	  ]},
	  {"name":"No Buttons","featured":false,"blocks":[
	    {"title":{"ru":"Добавление"},"description":{"ru":"Нажмите кнопку ниже."},"buttons":[
	      {"link":"{{HAPP_CRYPT4_LINK}}","type":"subscriptionLink","text":{"ru":"Добавить"}}]}
	  ]}]}}}`), &v2); err != nil {
		t.Fatal(err)
	}
	apps := acBuildV2(v2.Platforms["ios"].Apps, sub, "tg_99", "ru")
	// Приложение, у которого не осталось ни одной кнопки, в список не попадает.
	if len(apps) != 3 {
		t.Fatalf("приложений %d", len(apps))
	}
	a := apps[0]
	if a.Deeplink != "" || a.AddDesc != "" || len(a.Installs) != 1 || len(a.Copies) != 0 {
		t.Fatalf("приложение с негодной кнопкой добавления: %+v", a)
	}
	if len(a.More) != 2 || a.More[0].Title != "Добавление" || a.More[1].Title != "Если не добавилась" {
		t.Fatalf("разделы: %+v", a.More)
	}
	// Подпись кнопки страница подписки выводит текстом, без разбора разметки.
	if b := a.More[0].Buttons; len(b) != 1 || !b[0].Copy || b[0].URL != sub || b[0].Text != "A &amp; <b>B</b>" {
		t.Fatalf("кнопка раздела: %+v", b)
	}

	// Зашифрованных ссылок бот не строит: кнопок с ними нет, остальные на месте.
	c := apps[1]
	if c.Deeplink != "" || len(c.Copies) != 0 || len(c.More) != 1 {
		t.Fatalf("приложение с зашифрованной ссылкой: %+v", c)
	}
	if b := c.More[0].Buttons; len(b) != 1 || b[0].Text != "Ссылка" || b[0].URL != sub {
		t.Fatalf("кнопки рядом с зашифрованной ссылкой: %+v", b)
	}

	// Блок, от которого не осталось ни одной кнопки, разделом не становится:
	// его текст говорит о кнопке, которой нет. Блок без кнопок остаётся.
	o := apps[2]
	if len(o.Installs) != 1 || len(o.More) != 1 || o.More[0].Title != "Использование" {
		t.Fatalf("приложение с блоками без своих кнопок: %+v", o)
	}
}
