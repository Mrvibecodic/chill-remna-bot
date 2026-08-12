package model

import (
	"reflect"
	"testing"
)

// Pricing.Clone перечисляет карты руками, и это его слабое место: карта,
// добавленная в структуру позже, останется поделённой с конфигом молча — ни
// компилятор, ни обычный тест этого не заметят, а расплата за такую утечку
// (одновременное чтение и запись карты) — смерть процесса без возможности
// перехвата.
//
// Поэтому тест идёт от структуры, а не от списка полей: он рефлексией
// заполняет КАЖДУЮ карту и КАЖДЫЙ слайс, а затем требует, чтобы у копии они
// лежали по другому адресу.
func TestPricingCloneCoversEveryReferenceField(t *testing.T) {
	var p Pricing
	v := reflect.ValueOf(&p).Elem()
	rt := v.Type()

	filled := 0
	for i := 0; i < rt.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Map:
			f.Set(reflect.MakeMap(f.Type()))
			key := sampleValue(f.Type().Key())
			f.SetMapIndex(key, sampleValue(f.Type().Elem()))
			filled++
		case reflect.Slice:
			f.Set(reflect.Append(f, sampleValue(f.Type().Elem())))
			filled++
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
			filled++
		}
	}
	if filled == 0 {
		t.Fatal("в Pricing не нашлось ни одного поля-ссылки — тест потерял смысл, проверьте структуру")
	}

	c := p.Clone()
	cv := reflect.ValueOf(&c).Elem()
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		orig, copied := v.Field(i), cv.Field(i)
		switch orig.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer:
			if orig.IsNil() {
				t.Fatalf("поле %s не заполнено тестом", name)
			}
			if copied.IsNil() {
				t.Fatalf("поле %s потеряно при копировании", name)
			}
			if orig.Pointer() == copied.Pointer() {
				t.Fatalf("поле %s поделено с оригиналом: Clone его не копирует", name)
			}
		}
	}

	// Отдельно — вложенные слайсы внутри карты сквадов: их тоже правит админка.
	c.SquadsInt[1][0] = "changed"
	if p.SquadsInt[1][0] == "changed" {
		t.Fatal("SquadsInt: слайс внутри карты поделён с оригиналом")
	}
}

// sampleValue — любое непустое значение нужного типа.
func sampleValue(t reflect.Type) reflect.Value {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Slice:
		v.Set(reflect.Append(v, sampleValue(t.Elem())))
	case reflect.Map:
		v.Set(reflect.MakeMap(t))
		v.SetMapIndex(sampleValue(t.Key()), sampleValue(t.Elem()))
	}
	return v
}
