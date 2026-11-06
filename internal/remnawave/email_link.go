package remnawave

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// errEmailAmbiguous — под одной почтой несколько учёток. «Не нашли» здесь
// отвечать нельзя: продление завело бы человеку вторую учётку вместо
// оплаченной.
var errEmailAmbiguous = errors.New("в панели несколько учёток с почтой этого аккаунта")

// findByAccountEmail ищет учётку аккаунта кабинета по его подтверждённой почте
// в поле email панели. Учётки Telegram-пользователей (telegramId > 0) и
// доп-подписки сюда не попадают: они принадлежат не этому аккаунту.
func (c *Client) findByAccountEmail(ctx context.Context, telegramID int64) (*panelUser, error) {
	if c.emailFor == nil {
		return nil, nil
	}
	email := c.emailFor(ctx, telegramID)
	if email == "" {
		return nil, nil
	}
	users, err := c.usersByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	var found *panelUser
	for i := range users {
		u := &users[i]
		if u.TelegramID > 0 || u.Tag == BotTagAdd || u.ref().Empty() {
			continue
		}
		if found != nil {
			return nil, errEmailAmbiguous
		}
		found = u
	}
	if found != nil {
		found.byEmail = true
	}
	return found, nil
}

// UsersByEmail — все учётки панели с этой почтой (сравнение без учёта регистра).
func (c *Client) UsersByEmail(ctx context.Context, email string) ([]PanelUser, error) {
	users, err := c.usersByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	out := make([]PanelUser, 0, len(users))
	for i := range users {
		if pu := toPanelUser(&users[i]); pu != nil {
			out = append(out, *pu)
		}
	}
	return out, nil
}

// usersByEmail выбирает маршрут по версии панели так же, как findByTelegram:
// до 3.0.0 — /api/users/by-email/{email}, с 3.0.0 — фильтр /api/users/stream.
func (c *Client) usersByEmail(ctx context.Context, email string) ([]panelUser, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	if c.generation() == genV3 {
		us, gone, err := c.usersByEmailStream(ctx, email)
		if err != nil {
			return nil, err
		}
		if !gone {
			return us, nil
		}
		c.resetGen()
	}
	us, gone, err := c.usersByEmailLegacy(ctx, email)
	if err != nil {
		return nil, err
	}
	if !gone {
		return us, nil
	}
	if c.probeGen(ctx) == genV3 {
		us2, gone2, err2 := c.usersByEmailStream(ctx, email)
		if err2 != nil {
			return nil, err2
		}
		if !gone2 {
			return us2, nil
		}
	}
	return nil, errPanelDialect
}

// sameEmail — панель сравнивает почту строго, а бот хранит её в нижнем регистре.
func sameEmail(a, b string) bool {
	return a != "" && strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// keepEmail оставляет только учётки с этой почтой. Фильтру панели не верим
// вслепую: панель, которая его не знает, вернёт кого попало.
func keepEmail(users []panelUser, email string) (kept []panelUser, foreign bool) {
	for i := range users {
		if sameEmail(users[i].Email, email) {
			kept = append(kept, users[i])
		} else {
			foreign = true
		}
	}
	return kept, foreign
}

func (c *Client) usersByEmailStream(ctx context.Context, email string) (users []panelUser, gone bool, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/users/stream?size=10&email="+url.QueryEscape(email), nil)
	if err != nil {
		return nil, false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, true, nil
	case http.StatusBadRequest:
		// Панель не принимает такой адрес: учётки с ним в ней быть не может.
		return nil, false, nil
	case http.StatusOK:
	default:
		return nil, false, classifyHTTP(resp)
	}
	var env struct {
		Response struct {
			Users []panelUser `json:"users"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, false, fmt.Errorf("разбор ответа панели: %w", err)
	}
	kept, foreign := keepEmail(env.Response.Users, email)
	if foreign && len(kept) == 0 {
		// Фильтр не применён — это не «нет такой учётки».
		return nil, true, nil
	}
	for i := range kept {
		c.noteUser(&kept[i])
	}
	return kept, false, nil
}

func (c *Client) usersByEmailLegacy(ctx context.Context, email string) (users []panelUser, gone bool, err error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/users/by-email/"+url.PathEscape(email), nil)
	if err != nil {
		return nil, false, fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, routeGone(body), nil
	case http.StatusBadRequest:
		return nil, false, nil
	case http.StatusOK:
	default:
		return nil, false, classifyHTTP(resp)
	}
	var env struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, false, fmt.Errorf("разбор ответа панели: %w", err)
	}
	var arr []panelUser
	if json.Unmarshal(env.Response, &arr) != nil {
		var one panelUser
		if json.Unmarshal(env.Response, &one) == nil && !one.ref().Empty() {
			arr = []panelUser{one}
		}
	}
	kept, _ := keepEmail(arr, email)
	for i := range kept {
		c.noteUser(&kept[i])
	}
	return kept, false, nil
}

// LinkEmail записывает почту аккаунта кабинета в учётку и ставит тег бота.
// clearTelegram стирает telegramId — нужен, когда там лежит мусор, а не id
// живого человека.
func (c *Client) LinkEmail(ctx context.Context, ref UserRef, email string, clearTelegram bool) error {
	body := ref.apply(map[string]any{"email": email, "tag": BotTag})
	if clearTelegram {
		body["telegramId"] = nil
	}
	return c.patchUser(ctx, body)
}

// UnlinkUser снимает с учётки то, по чему бот её находит: telegramId и/или
// почту. Сама учётка, её срок и трафик не трогаются. dropTag снимает тег бота.
func (c *Client) UnlinkUser(ctx context.Context, ref UserRef, clearTelegram, clearEmail, dropTag bool) error {
	body := ref.apply(map[string]any{})
	if clearTelegram {
		body["telegramId"] = nil
	}
	if clearEmail {
		body["email"] = nil
	}
	if dropTag {
		body["tag"] = nil
	}
	return c.patchUser(ctx, body)
}

func (c *Client) patchUser(ctx context.Context, body map[string]any) error {
	resp, err := c.do(ctx, http.MethodPatch, "/api/users", body)
	if err != nil {
		return fmt.Errorf("нет связи с панелью: %w", err)
	}
	defer resp.Body.Close()
	if !writeOK(resp.StatusCode) {
		return classifyHTTP(resp)
	}
	return nil
}

// IsBotUsername — учётку завёл сам бот под этим аккаунтом: она принадлежит ему
// по имени, и снять это панель не даёт (имя не меняется).
func IsBotUsername(username string, telegramID int64) bool {
	return username == botUsername(telegramID)
}
