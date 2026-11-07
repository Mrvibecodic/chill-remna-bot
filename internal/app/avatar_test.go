package app

import (
	"context"
	"strconv"
	"testing"
	"time"

	"remnabot/internal/model"
)

// ProfilePhotoID двойника: фото есть, если в downloads лежит файл "ava:<id>".
func (f *fakeMsg) ProfilePhotoID(_ context.Context, userID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := "ava:" + strconv.FormatInt(userID, 10)
	if _, ok := f.downloads[id]; ok {
		return id, nil
	}
	return "", nil
}

func TestMiniAvatar(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}
	a, fm, _ := newTestApp(t)
	a.botCfg = &model.BotConfig{Installed: true, Language: "ru"}
	fm.downloads = map[string][]byte{
		"ava:1000000001": jpeg,
		"ava:1000000002": []byte("<html>не картинка</html>"),
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		id   int64
		want bool
	}{
		{"есть фото", 1000000001, true},
		{"не JPEG", 1000000002, false},
		{"нет фото", 1000000003, false},
		{"аккаунт по почте", -5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, ok := a.MiniAvatar(ctx, tc.id)
			if ok != tc.want || (ok && string(data) != string(jpeg)) {
				t.Fatalf("ok=%v, ждали %v", ok, tc.want)
			}
		})
	}
	// Кэш: повторный запрос не идёт в Telegram, даже если фото там сменилось.
	delete(fm.downloads, "ava:1000000001")
	if _, ok := a.MiniAvatar(ctx, 1000000001); !ok {
		t.Fatal("фото не взято из кэша")
	}
	// По истечении срока — снова из Telegram.
	a.avatars.mu.Lock()
	e := a.avatars.m[1000000001]
	e.at = e.at.Add(-avatarTTL - time.Minute)
	a.avatars.m[1000000001] = e
	a.avatars.mu.Unlock()
	if _, ok := a.MiniAvatar(ctx, 1000000001); ok {
		t.Fatal("устаревший кэш не перечитан")
	}
}

// Кэш не растёт больше avatarCap: вытесняется самая старая запись.
func TestAvatarCacheCap(t *testing.T) {
	var c avatarCache
	now := time.Unix(1_800_000_000, 0)
	for i := range avatarCap + 5 {
		c.put(int64(i+1), avatarEntry{at: now.Add(time.Duration(i) * time.Second)})
	}
	if len(c.m) != avatarCap {
		t.Fatalf("записей %d, ждали %d", len(c.m), avatarCap)
	}
	if _, ok := c.m[1]; ok {
		t.Fatal("самая старая запись не вытеснена")
	}
}
