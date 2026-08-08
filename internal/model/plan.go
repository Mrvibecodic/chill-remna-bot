package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PlanSnapshot — условия сделки, зафиксированные в момент выставления счёта.
//
// Зачем: до сих пор о покупке хранилось только число месяцев, а лимиты и
// сквады при продлении, автосписании и добивании счёта реконсилятором брались
// из ТЕКУЩЕГО конфига. Админ поднял лимит устройств — и он молча менялся у
// всех, кто продлевается; наоборот, снизил — и человек терял то, за что уже
// заплатил. Снимок делает сделку самодостаточной: продлеваем ровно то, что
// продали.
//
// Снимок хранится JSON-строкой в отдельных колонках (payments,
// pending_invoices, p2p_requests, autopay, users). Колонки объявлены
// NOT NULL DEFAULT ”: вставки везде перечисляют поля явно, поэтому
// предыдущий образ бота, который про них не знает, продолжает писать свои
// строки — откат остаётся безопасным.
type PlanSnapshot struct {
	// Code и Name появятся вместе с сущностью тарифа; пока пусты.
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`

	Months      int      `json:"months"`
	TrafficGB   int      `json:"traffic_gb"`
	DeviceLimit int      `json:"device_limit"`
	Strategy    string   `json:"strategy,omitempty"`
	IntSquads   []string `json:"int_squads,omitempty"`
	ExtSquad    string   `json:"ext_squad,omitempty"`

	Price    string `json:"price,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// TrafficBytes — лимит трафика снимка в байтах (0 = безлимит).
func (s *PlanSnapshot) TrafficBytes() int64 {
	if s == nil || s.TrafficGB <= 0 {
		return 0
	}
	return int64(s.TrafficGB) * 1024 * 1024 * 1024
}

// Encode сериализует снимок для хранения. Пустой снимок пишется пустой
// строкой, а не "null": так колонка остаётся совместимой с DEFAULT ”.
func (s *PlanSnapshot) Encode() string {
	if s == nil {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// Fingerprint — короткий отпечаток условий сделки. Нужен, чтобы дешёво
// заметить, что условия изменились (например, перед уведомлением об
// изменившемся автопродлении). В ключ идемпотентности платежей отпечаток
// намеренно НЕ входит: там повтор обязан попадать в тот же платёж.
func (s *PlanSnapshot) Fingerprint() string {
	if s == nil {
		return "0"
	}
	// Код и имя тарифа в отпечаток НЕ входят: это опознание сделки и её
	// оформление, а не условия. Иначе первое же обновление бота (в снимках
	// появился код) и любое переименование тарифа рассылали бы всем
	// подписчикам «условия автопродления изменились», хотя не изменилось
	// ничего.
	cond := *s
	cond.Code = ""
	cond.Name = ""
	sum := sha256.Sum256([]byte(cond.Encode()))
	return hex.EncodeToString(sum[:4])
}

// DecodePlanSnapshot читает снимок из хранилища. Пустая строка и битый JSON
// дают nil — вызывающий обязан уметь работать без снимка (строки, созданные
// до появления колонок, и строки, записанные предыдущим образом бота).
func DecodePlanSnapshot(raw string) *PlanSnapshot {
	if raw == "" {
		return nil
	}
	var s PlanSnapshot
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil
	}
	return &s
}
