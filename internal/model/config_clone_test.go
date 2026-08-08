package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Копия конфига делается кругом через JSON, и обещание у неё сильное: круг
// переносит ВСЁ, что подлежит сохранению. Тест проверяет обещание буквально —
// рефлексией заполняет каждое поле дерева конфига и сравнивает JSON до и после
// круга. Поле, которое круг теряет (например, помеченное `json:"-"`), ломает
// сохранение конфига молча: в базу уедет копия без него.
func TestBotConfigCloneKeepsEverything(t *testing.T) {
	var cfg BotConfig
	fillValue(reflect.ValueOf(&cfg).Elem(), 0)

	before, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("круг через JSON изменил конфиг:\nдо:    %s\nпосле: %s", before, after)
	}
	// Сравнение самих структур, а не только их JSON: поле, помеченное `json:"-"`,
	// выпало бы из ОБОИХ снимков одинаково, и сверка снимков его потерю не
	// заметила бы — а копия уехала бы в базу без него.
	if !reflect.DeepEqual(cfg, *cp) {
		t.Fatal("копия отличается от конфига: какое-то поле не переносится кругом через JSON " +
			"(например, помеченное json:\"-\" — такому полю в конфиге не место, его нельзя сохранить)")
	}

	// И копия обязана быть независимой: карты и слайсы не поделены.
	cp.Pricing.Base[1] = "changed"
	if cfg.Pricing.Base[1] == "changed" {
		t.Fatal("сетка цен поделена с копией")
	}
}

// Пустые входы обязаны отвечать «ничего нет» без ошибки, а битый снимок —
// ошибкой. Второе важнее: сохранение конфига отличает «бот не настроен» от сбоя
// именно по этому, и молчаливый пустой конфиг записал бы в базу пустоту вместо
// настроек.
func TestConfigSnapshotEdgeCases(t *testing.T) {
	var nilCfg *BotConfig
	if raw, err := nilCfg.SnapshotJSON(); err != nil || raw != nil {
		t.Fatalf("снимок nil-конфига: %q, %v", raw, err)
	}
	if cp, err := nilCfg.Clone(); err != nil || cp != nil {
		t.Fatalf("копия nil-конфига: %v, %v", cp, err)
	}
	if cp, err := ConfigFromJSON(nil); err != nil || cp != nil {
		t.Fatalf("конфиг из пустого снимка: %v, %v", cp, err)
	}
	if cp, err := ConfigFromJSON([]byte{}); err != nil || cp != nil {
		t.Fatalf("конфиг из пустого снимка: %v, %v", cp, err)
	}
	if cp, err := ConfigFromJSON([]byte("{не json")); err == nil || cp != nil {
		t.Fatalf("битый снимок принят: %v, %v", cp, err)
	}

	cfg := &BotConfig{Installed: true, Language: LangRU}
	raw, err := cfg.SnapshotJSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ConfigFromJSON(raw)
	if err != nil || back == nil || !back.Installed || back.Language != LangRU {
		t.Fatalf("снимок не восстановился: %+v, %v", back, err)
	}
}

// fillValue заполняет всё дерево значения непустыми данными: скаляры, карты,
// слайсы, указатели и вложенные структуры. depth ограничивает рекурсию на
// случай самоссылающихся типов.
func fillValue(v reflect.Value, depth int) {
	if depth > 6 || !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		// Символы разметки и не-ASCII: круг через JSON обязан переносить их без
		// изменения смысла.
		v.SetString("значение <&> ok")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillValue(p.Elem(), depth+1)
		v.Set(p)
	case reflect.Slice:
		// json.RawMessage — слайс байт, и произвольные байты в нём JSON'ом не
		// пройдут: кладём валидный фрагмент.
		if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			v.Set(reflect.ValueOf(json.RawMessage(`[{"type":"bold","offset":0,"length":2}]`)))
			return
		}
		el := reflect.New(v.Type().Elem()).Elem()
		fillValue(el, depth+1)
		v.Set(reflect.Append(v, el))
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillValue(k, depth+1)
		el := reflect.New(v.Type().Elem()).Elem()
		fillValue(el, depth+1)
		m.SetMapIndex(k, el)
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fillValue(v.Field(i), depth+1)
		}
	}
}
