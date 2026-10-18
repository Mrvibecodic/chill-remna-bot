package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LiveSquads — сквады, которые сейчас есть в панели (uuid → имя).
// IntKnown/ExtKnown — список получен и разобран однозначно. Без этого ни один
// сквад не считается удалённым: сбой связи или незнакомый ответ панели не
// должны выкидывать из настроек живые сквады.
type LiveSquads struct {
	Int, Ext           map[string]string
	IntKnown, ExtKnown bool
}

// IntGone — внутренний сквад точно удалён в панели.
func (l LiveSquads) IntGone(uuid string) bool {
	if !l.IntKnown || uuid == "" {
		return false
	}
	_, ok := l.Int[uuid]
	return !ok
}

// ExtGone — внешний сквад точно удалён в панели.
func (l LiveSquads) ExtGone(uuid string) bool {
	if !l.ExtKnown || uuid == "" {
		return false
	}
	_, ok := l.Ext[uuid]
	return !ok
}

// KeepInt делит набор внутренних сквадов на живые и удалённые в панели.
// kept — всегда новый срез, исходный не меняется.
func (l LiveSquads) KeepInt(uuids []string) (kept, gone []string) {
	kept = make([]string, 0, len(uuids))
	for _, u := range uuids {
		if l.IntGone(u) {
			gone = append(gone, u)
			continue
		}
		kept = append(kept, u)
	}
	return kept, gone
}

// liveSquadsTTL — сколько выдача доверяет последнему списку сквадов: без кэша
// каждая выдача (и проход по всей базе при синхронизации доп-подписок) ходила
// бы в панель за списком лишний раз.
const liveSquadsTTL = 30 * time.Second

type squadCache struct {
	mu   sync.Mutex
	live LiveSquads
	at   time.Time
}

// LiveSquads читает сквады из панели заново и обновляет кэш выдачи.
func (c *Client) LiveSquads(ctx context.Context) LiveSquads {
	var l LiveSquads
	l.Int, l.IntKnown = c.fetchSquadSet(ctx, "/api/internal-squads", "internalSquads")
	l.Ext, l.ExtKnown = c.fetchSquadSet(ctx, "/api/external-squads", "externalSquads")
	if l.IntKnown || l.ExtKnown {
		c.squads.mu.Lock()
		c.squads.live, c.squads.at = l, time.Now()
		c.squads.mu.Unlock()
	}
	return l
}

func (c *Client) cachedSquads(ctx context.Context) LiveSquads {
	c.squads.mu.Lock()
	l, at := c.squads.live, c.squads.at
	c.squads.mu.Unlock()
	if !at.IsZero() && time.Since(at) < liveSquadsTTL {
		return l
	}
	return c.LiveSquads(ctx)
}

// fetchSquadSet — строгий разбор списка сквадов. ok=false на любой ответ,
// который нельзя прочитать однозначно: другой код, нет ключа со списком,
// сквад без uuid, total не совпал с длиной (панель отдала не всё).
func (c *Client) fetchSquadSet(ctx context.Context, path, key string) (map[string]string, bool) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, false
	}
	var list []struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(env.Response, &obj) == nil {
		raw, ok := obj[key]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, false
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil, false
		}
		if t, ok := obj["total"]; ok {
			var n int
			if json.Unmarshal(t, &n) != nil || n != len(list) {
				return nil, false
			}
		}
	} else if json.Unmarshal(env.Response, &list) != nil {
		return nil, false
	}
	set := make(map[string]string, len(list))
	for _, s := range list {
		if s.UUID == "" {
			return nil, false
		}
		set[s.UUID] = s.Name
	}
	return set, true
}

// ErrSquadsGone — новому пользователю не осталось ни одного живого
// внутреннего сквада: без сквадов он получил бы подписку без серверов.
var ErrSquadsGone = errors.New("все выбранные сквады удалены в панели — выберите сквады заново")

// liveLimits убирает из лимитов сквады, удалённые в панели: с ними панель
// отвечает 500 (A018) и не создаёт пользователя. У существующего пользователя
// пустой набор просто не отправляется — его текущие сквады остаются.
func (c *Client) liveLimits(ctx context.Context, l UserLimits, create bool) (UserLimits, error) {
	if len(l.InternalSquads) == 0 && l.ExternalSquad == "" {
		return l, nil
	}
	live := c.cachedSquads(ctx)
	var gone []string
	if len(l.InternalSquads) > 0 {
		kept, g := live.KeepInt(l.InternalSquads)
		if len(g) > 0 {
			if create && len(kept) == 0 {
				return l, ErrSquadsGone
			}
			l.InternalSquads = kept
			gone = append(gone, g...)
		}
	}
	if live.ExtGone(l.ExternalSquad) {
		gone = append(gone, l.ExternalSquad)
		l.ExternalSquad = ""
	}
	if len(gone) > 0 {
		slog.Warn("сквады удалены в панели и пропущены", "squads", strings.Join(gone, ","))
	}
	return l, nil
}
