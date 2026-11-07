package app

import (
	"bytes"
	"context"
	"sync"
	"time"
)

// Аватарка для мини-аппа и кабинета берётся ботом через Bot API и отдаётся с
// его домена: браузер посетителя не ходит за ней на серверы Telegram.
const (
	avatarTTL     = 6 * time.Hour
	avatarMissTTL = time.Hour
	avatarCap     = 500
	avatarMaxSize = 96 << 10
)

type avatarEntry struct {
	data []byte // nil — фото нет или Telegram его не отдал
	at   time.Time
}

type avatarCache struct {
	mu sync.Mutex
	m  map[int64]avatarEntry
}

func (c *avatarCache) get(id int64, now time.Time) (avatarEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok {
		return avatarEntry{}, false
	}
	ttl := avatarTTL
	if e.data == nil {
		ttl = avatarMissTTL
	}
	if now.Sub(e.at) > ttl {
		delete(c.m, id)
		return avatarEntry{}, false
	}
	return e, true
}

func (c *avatarCache) put(id int64, e avatarEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[int64]avatarEntry)
	}
	if _, ok := c.m[id]; !ok && len(c.m) >= avatarCap {
		var oldID int64
		var old time.Time
		for k, v := range c.m {
			if old.IsZero() || v.at.Before(old) {
				oldID, old = k, v.at
			}
		}
		delete(c.m, oldID)
	}
	c.m[id] = e
}

// MiniAvatar — фото профиля Telegram (JPEG) или false, если его нет. У
// аккаунта по почте без Telegram фото нет.
func (a *App) MiniAvatar(ctx context.Context, tgID int64) ([]byte, bool) {
	if !hasChat(tgID) || a.msg == nil {
		return nil, false
	}
	now := time.Now()
	if e, ok := a.avatars.get(tgID, now); ok {
		return e.data, e.data != nil
	}
	data := a.fetchAvatar(ctx, tgID)
	// Обрыв по таймауту запроса — не повод на час считать, что фото нет.
	if data == nil && ctx.Err() != nil {
		return nil, false
	}
	a.avatars.put(tgID, avatarEntry{data: data, at: now})
	return data, data != nil
}

func (a *App) fetchAvatar(ctx context.Context, tgID int64) []byte {
	fileID, err := a.msg.ProfilePhotoID(ctx, tgID)
	if err != nil {
		a.log.Debug("avatar: фото профиля", "err", err)
		return nil
	}
	if fileID == "" {
		return nil
	}
	data, err := a.msg.Download(ctx, fileID)
	if err != nil {
		a.log.Debug("avatar: загрузка", "err", err)
		return nil
	}
	if len(data) > avatarMaxSize || !bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}) {
		return nil
	}
	return data
}
