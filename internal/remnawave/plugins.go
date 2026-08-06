package remnawave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// Плагины нод появились в панели 2.7.0 и живут отдельным контроллером
// /api/node-plugins. Конфиг плагина — произвольный JSON (валидируется на
// стороне панели схемой @remnawave/node-plugins), поэтому бот его не
// типизирует целиком: он читает конфиг, точечно правит нужную ветку и кладёт
// обратно, не теряя незнакомых ему полей.
//
// Панели старше 2.7.0 отвечают на эти пути 404 — вызывающий код должен
// показывать ошибку как «панель не поддерживает», а не падать.

// NodePlugin — именованный конфиг плагинов, привязываемый к ноде полем
// activePluginUuid.
type NodePlugin struct {
	UUID   string
	Name   string
	Config map[string]any
}

// pluginConfigUnmarshal разбирает pluginConfig числами-как-строками
// (json.Number). Иначе целые из чужих полей ушли бы обратно в панель как
// float64 и могли бы уехать в экспоненциальную запись (1e+06), которую
// zod-схема панели не примет.
func pluginConfigUnmarshal(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// NodePlugins возвращает список конфигов плагинов панели.
func (c *Client) NodePlugins(ctx context.Context) ([]NodePlugin, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/node-plugins", nil)
	if err != nil {
		return nil, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyHTTP(resp)
	}
	var out struct {
		Response struct {
			NodePlugins []struct {
				UUID         string          `json:"uuid"`
				Name         string          `json:"name"`
				PluginConfig json.RawMessage `json:"pluginConfig"`
			} `json:"nodePlugins"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("разбор ответа панели: %w", err)
	}
	plugins := make([]NodePlugin, 0, len(out.Response.NodePlugins))
	for _, p := range out.Response.NodePlugins {
		cfg, err := pluginConfigUnmarshal(p.PluginConfig)
		if err != nil {
			return nil, fmt.Errorf("разбор конфига плагина %q: %w", p.Name, err)
		}
		plugins = append(plugins, NodePlugin{UUID: p.UUID, Name: p.Name, Config: cfg})
	}
	return plugins, nil
}

// UpdateNodePluginConfig сохраняет конфиг целиком (панель принимает только
// полную замену pluginConfig).
func (c *Client) UpdateNodePluginConfig(ctx context.Context, uuid string, cfg map[string]any) error {
	resp, err := c.do(ctx, http.MethodPatch, "/api/node-plugins",
		map[string]any{"uuid": uuid, "pluginConfig": cfg})
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// UnblockIP снимает блокировку адреса на всех подключённых нодах через
// Executor плагинов. Панель сама отбирает ноды: отключённые и неподключённые
// в рассылку не попадают.
func (c *Client) UnblockIP(ctx context.Context, ip string) error {
	ip = strings.TrimSpace(ip)
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("некорректный IP-адрес: %q", ip)
	}
	body := map[string]any{
		"command":     map[string]any{"command": "unblockIps", "ips": []string{ip}},
		"targetNodes": map[string]any{"target": "allNodes"},
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/node-plugins/executor", body)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// TorrentIgnoreUser добавляет пользователя в ignoreLists.userId всех конфигов,
// где вообще есть секция torrentBlocker, и возвращает, сколько конфигов
// изменено. Ноль без ошибки означает, что торрент-блокер на панели не настроен
// (или пользователь уже в списке — тогда already=true).
func (c *Client) TorrentIgnoreUser(ctx context.Context, userID int64) (changed int, already bool, err error) {
	plugins, err := c.NodePlugins(ctx)
	if err != nil {
		return 0, false, err
	}
	seen := false
	for _, p := range plugins {
		tb, ok := p.Config["torrentBlocker"].(map[string]any)
		if !ok {
			continue
		}
		seen = true
		// Неожиданные типы не «чиним» подстановкой пустых значений: так молча
		// улетели бы ignoreLists.ip и весь остальной список исключений.
		lists, ok := tb["ignoreLists"].(map[string]any)
		if !ok {
			if _, exists := tb["ignoreLists"]; exists {
				return changed, false, fmt.Errorf("конфиг плагина %q: ignoreLists не объект — правьте на панели вручную", p.Name)
			}
			lists = map[string]any{}
		}
		ids, ok := lists["userId"].([]any)
		if !ok {
			if _, exists := lists["userId"]; exists {
				return changed, false, fmt.Errorf("конфиг плагина %q: ignoreLists.userId не список — правьте на панели вручную", p.Name)
			}
		}
		if containsNumber(ids, userID) {
			continue
		}
		lists["userId"] = append(ids, json.Number(strconv.FormatInt(userID, 10)))
		tb["ignoreLists"] = lists
		p.Config["torrentBlocker"] = tb
		if err := c.UpdateNodePluginConfig(ctx, p.UUID, p.Config); err != nil {
			return changed, false, err
		}
		changed++
	}
	return changed, changed == 0 && seen, nil
}

// containsNumber сравнивает по числовому значению: панель возвращает элементы
// json.Number, но чужой конфиг мог быть записан и строкой.
func containsNumber(list []any, want int64) bool {
	for _, v := range list {
		switch n := v.(type) {
		case json.Number:
			if got, err := n.Int64(); err == nil && got == want {
				return true
			}
		case float64:
			if int64(n) == want {
				return true
			}
		case string:
			if got, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil && got == want {
				return true
			}
		}
	}
	return false
}
