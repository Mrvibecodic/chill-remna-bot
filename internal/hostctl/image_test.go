package hostctl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const composeSample = `# Chill Remna bot
services:
  bot:
    # :v1 держит все релизы первой версии без автопрыжка на 2.0.0
    image: ghcr.io/mrvibecodic/chill-remna-bot:v1
    restart: unless-stopped
    environment:
      BOT_TOKEN: "123:abc"
      # CADDY_AUTH_API_TOKEN: ""
    ports:
      - "127.0.0.1:8080:8080"
    depends_on:
      - db
  db:
    image: postgres:16-alpine

volumes:
  bot-data:
`

// Файл принадлежит владельцу установки: правка обязана менять ровно тег и
// ничего больше. Прежний код разбирал YAML и писал обратно своим форматом —
// пропадали комментарии, кавычки и порядок ключей.
func TestReplaceBotImageTag_KeepsEverythingElse(t *testing.T) {
	out, changed, err := replaceBotImageTag([]byte(composeSample), "dev")
	if err != nil || !changed {
		t.Fatalf("замена не прошла: changed=%v err=%v", changed, err)
	}
	got := string(out)
	if !strings.Contains(got, "image: ghcr.io/mrvibecodic/chill-remna-bot:dev") {
		t.Fatalf("тег не заменён:\n%s", got)
	}
	for _, keep := range []string{
		"# Chill Remna bot",
		"# :v1 держит все релизы первой версии",
		`BOT_TOKEN: "123:abc"`,
		`# CADDY_AUTH_API_TOKEN: ""`,
		`- "127.0.0.1:8080:8080"`,
		"  bot-data:",
		"image: postgres:16-alpine",
	} {
		if !strings.Contains(got, keep) {
			t.Fatalf("пропало из файла: %q\n%s", keep, got)
		}
	}
	if strings.Count(got, "\n") != strings.Count(composeSample, "\n") {
		t.Fatalf("изменилось число строк: было %d стало %d",
			strings.Count(composeSample, "\n"), strings.Count(got, "\n"))
	}
}

// Сервис называется bot и в depends_on соседа — по имени в списке зависимостей
// искать образ нельзя.
func TestReplaceBotImageTag_IgnoresDependsOn(t *testing.T) {
	src := `services:
  web:
    image: nginx:1
    depends_on:
      bot:
        condition: service_started
  bot:
    image: repo/bot:latest
`
	out, changed, err := replaceBotImageTag([]byte(src), "dev")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got := string(out)
	if !strings.Contains(got, "image: repo/bot:dev") || !strings.Contains(got, "image: nginx:1") {
		t.Fatalf("заменён не тот образ:\n%s", got)
	}
}

func TestReplaceBotImageTag_Cases(t *testing.T) {
	cases := []struct {
		name, src, tag, want string
		changed              bool
	}{
		{"без тега", "services:\n  bot:\n    image: repo/bot\n", "dev", "image: repo/bot:dev", true},
		{"реестр с портом", "services:\n  bot:\n    image: localhost:5000/bot:v1\n", "dev", "image: localhost:5000/bot:dev", true},
		{"в кавычках", "services:\n  bot:\n    image: \"repo/bot:v1\"\n", "dev", "image: \"repo/bot:dev\"", true},
		{"хвостовой комментарий", "services:\n  bot:\n    image: repo/bot:v1 # пин\n", "dev", "image: repo/bot:dev # пин", true},
		{"тег уже нужный", "services:\n  bot:\n    image: repo/bot:dev\n", "dev", "image: repo/bot:dev", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed, err := replaceBotImageTag([]byte(c.src), c.tag)
			if err != nil {
				t.Fatalf("err=%v", err)
			}
			if changed != c.changed {
				t.Fatalf("changed=%v, ожидалось %v", changed, c.changed)
			}
			if !strings.Contains(string(out), c.want) {
				t.Fatalf("нет %q:\n%s", c.want, string(out))
			}
		})
	}
}

// Пин по digest тега не имеет: прежний код дописывал тег после «@sha256» и
// получал заведомо неверную ссылку — pull падал всегда.
func TestReplaceBotImageTag_DigestPinRefused(t *testing.T) {
	src := "services:\n  bot:\n    image: repo/bot@sha256:aaaabbbbccccddddeeeeffff0000111122223333444455556666777788889999\n"
	if _, _, err := replaceBotImageTag([]byte(src), "dev"); !errors.Is(err, errImageDigestPinned) {
		t.Fatalf("ожидался отказ по digest, получено %v", err)
	}
}

// SetImageChannel отдаёт прежнее содержимое, чтобы можно было откатиться,
// если образ так и не скачался.
func TestSetImageChannel_ReturnsPreviousForRollback(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(file, []byte(composeSample), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Controller{composeFile: file}

	prev, err := c.SetImageChannel("dev")
	if err != nil {
		t.Fatalf("SetImageChannel: %v", err)
	}
	if string(prev) != composeSample {
		t.Fatalf("prev не равен исходному файлу")
	}
	if tag, err := c.BotImageTag(); err != nil || tag != "dev" {
		t.Fatalf("тег после правки: %q (%v)", tag, err)
	}
	if err := c.RestoreCompose(prev); err != nil {
		t.Fatalf("RestoreCompose: %v", err)
	}
	back, _ := os.ReadFile(file)
	if string(back) != composeSample {
		t.Fatalf("откат не вернул файл:\n%s", string(back))
	}
	fi, _ := os.Stat(file)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("права файла изменились: %v", fi.Mode().Perm())
	}

	// Повторная установка того же тега файл не трогает и откатывать нечего.
	if _, err := c.SetImageChannel("v1"); err != nil {
		t.Fatal(err)
	}
	if prev2, err := c.SetImageChannel("v1"); err != nil || prev2 != nil {
		t.Fatalf("повторная установка того же тега: prev=%v err=%v", prev2, err)
	}
}
