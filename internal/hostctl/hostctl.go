package hostctl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const PostgresDSN = "postgres://remnabot:remnabot@db:5432/remnabot?sslmode=disable"

type Controller struct {
	composeFile    string
	project        string
	hostDir        string
	panelContainer string
	panelNetwork   string
	selfContainer  string
}

func New() *Controller {
	return &Controller{
		composeFile:    env("COMPOSE_FILE_PATH", "/compose/docker-compose.yml"),
		project:        env("COMPOSE_PROJECT", "remnachillbot"),
		hostDir:        env("COMPOSE_HOST_DIR", "/opt/remnachillbot"),
		panelContainer: env("PANEL_CONTAINER", "remnawave"),
		panelNetwork:   env("PANEL_NETWORK", "remnawave-network"),
		selfContainer:  env("SELF_CONTAINER", "remnabot"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (c *Controller) SelfContainer() string { return c.selfContainer }

func (c *Controller) Available() bool {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		return false
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	if _, err := os.Stat(c.composeFile); err != nil {
		return false
	}
	return true
}

func (c *Controller) ConnectPanelNetwork(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		nets := c.panelNetworks(ctx)
		if len(nets) == 0 && c.panelNetwork != "" {
			nets = []string{c.panelNetwork}
		}
		connected := false
		for _, netName := range nets {
			if c.connectNetwork(ctx, netName) {
				connected = true
			}
		}
		if connected {
			return nil
		}
		lastErr = fmt.Errorf("сеть панели не найдена (пробовал: %s)", strings.Join(nets, ", "))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("сеть панели не найдена")
	}
	return lastErr
}

func (c *Controller) panelNetworks(ctx context.Context) []string {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{range $k,$_ := .NetworkSettings.Networks}}{{$k}} {{end}}`, c.panelContainer).Output()
	if err != nil {
		return nil
	}
	var nets []string
	for _, netName := range strings.Fields(string(out)) {
		switch netName {
		case "bridge", "host", "none":
			continue
		}
		nets = append(nets, netName)
	}
	return nets
}

func (c *Controller) connectNetwork(ctx context.Context, netName string) bool {
	o, e := exec.CommandContext(ctx, "docker", "network", "connect", netName, c.selfContainer).CombinedOutput()
	if e == nil {
		return true
	}
	s := string(o)
	return strings.Contains(s, "already exists") || strings.Contains(s, "already connected") || strings.Contains(s, "endpoint with name")
}

func (c *Controller) EnablePostgres(ctx context.Context) (string, error) {
	if err := c.addPostgresToCompose(); err != nil {
		return "", fmt.Errorf("правка compose: %w", err)
	}
	if err := c.compose(ctx, "up", "-d", "db"); err != nil {
		return "", err
	}
	if err := waitTCP(ctx, "db:5432", 60*time.Second); err != nil {
		return "", err
	}
	return PostgresDSN, nil
}

func (c *Controller) addPostgresToCompose() error {
	data, err := os.ReadFile(c.composeFile)
	if err != nil {
		return err
	}
	root := map[string]any{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}

	services, _ := root["services"].(map[string]any)
	if services == nil {
		services = map[string]any{}
		root["services"] = services
	}
	services["db"] = map[string]any{
		"image":          "postgres:17-alpine",
		"container_name": "remnabot-db",
		"restart":        "unless-stopped",
		"environment": map[string]any{
			"POSTGRES_USER":     "remnabot",
			"POSTGRES_PASSWORD": "remnabot",
			"POSTGRES_DB":       "remnabot",
		},
		"volumes": []any{"pg-data:/var/lib/postgresql/data"},
		"healthcheck": map[string]any{
			"test":     []any{"CMD-SHELL", "pg_isready -U remnabot"},
			"interval": "5s",
			"timeout":  "5s",
			"retries":  10,
		},
	}

	if bot, ok := services["bot"].(map[string]any); ok {
		botEnv, _ := bot["environment"].(map[string]any)
		if botEnv == nil {
			botEnv = map[string]any{}
			bot["environment"] = botEnv
		}
		botEnv["DB_KIND"] = "postgres"
		botEnv["DATABASE_URL"] = PostgresDSN
	}

	volumes, _ := root["volumes"].(map[string]any)
	if volumes == nil {
		volumes = map[string]any{}
		root["volumes"] = volumes
	}
	volumes["pg-data"] = nil

	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(c.composeFile, out, 0o600)
}

func (c *Controller) runCompose(ctx context.Context, detached bool, script string) error {
	args := []string{"run", "--rm"}
	if detached {
		args = append(args, "-d")
	}
	args = append(args,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", c.hostDir+":/p",
		"-w", "/p",
		"docker:cli",
		"sh", "-c", script,
	)
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("compose: %w: %s", err, tailStr(string(out), 400))
	}
	return nil
}

func (c *Controller) runComposeDetached(ctx context.Context, script string) error {
	return c.runCompose(ctx, true, script)
}

// tailStr — хвост строки: у docker pull содержательная часть ошибки в конце.
func tailStr(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func (c *Controller) SelfUpdate(ctx context.Context) error {
	// Два шага вместо одного «pull && up -d» в отвязанном контейнере:
	//  1) pull ТОЛЬКО сервиса bot — синхронно, чтобы причина сбоя (например,
	//     429 Too Many Requests от Docker Hub) дошла до админа текстом, а не
	//     превратилась в пустое «рестарт не произошёл». Общий pull к тому же
	//     падал целиком из-за постороннего postgres.
	//  2) up -d — отвязанно: пересоздание контейнера убивает самого бота.
	if err := c.runCompose(ctx, false, fmt.Sprintf("docker compose -p %s pull bot", c.project)); err != nil {
		return err
	}
	return c.runComposeDetached(ctx, fmt.Sprintf("docker compose -p %s up -d", c.project))
}

// SetImageChannel меняет ТЕГ образа сервиса bot в compose-файле
// (…:latest → …:dev) и возвращает прежнее содержимое файла — оно нужно, чтобы
// откатиться, если образ так и не скачался (см. app.runSelfUpdate).
//
// Правка построчная, а не «разобрать YAML и записать обратно». Разбор с
// сериализацией переписывал ВЕСЬ файл своим форматом: пропадали комментарии
// (в том числе тот, что объясняет пин `:v1`), кавычки, порядок ключей и
// якоря. Файл принадлежит владельцу установки, а не боту, — трогаем ровно те
// байты, которые обязаны измениться.
//
// Пустой prev означает «менять было нечего»: тег уже нужный.
func (c *Controller) SetImageChannel(tag string) (prev []byte, err error) {
	data, err := os.ReadFile(c.composeFile)
	if err != nil {
		return nil, err
	}
	out, changed, err := replaceBotImageTag(data, tag)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, nil
	}
	if err := c.writeCompose(out); err != nil {
		return nil, err
	}
	return data, nil
}

// BotImageTag возвращает текущий тег образа сервиса bot ("" — тега нет).
func (c *Controller) BotImageTag() (string, error) {
	data, err := os.ReadFile(c.composeFile)
	if err != nil {
		return "", err
	}
	ref, err := botImageRef(data)
	if err != nil {
		return "", err
	}
	if j := strings.LastIndex(ref, ":"); j > strings.LastIndex(ref, "/") {
		return ref[j+1:], nil
	}
	return "", nil
}

// RestoreCompose возвращает файл к прежнему содержимому. Пустой prev — значит
// файл не трогали, и возвращать нечего.
func (c *Controller) RestoreCompose(prev []byte) error {
	if len(prev) == 0 {
		return nil
	}
	return c.writeCompose(prev)
}

// writeCompose пишет через временный файл рядом и переименование: обрыв на
// половине записи оставил бы установку с обрезанным compose, а это «бот не
// поднимается» до ручного вмешательства.
func (c *Controller) writeCompose(data []byte) error {
	dir := filepath.Dir(c.composeFile)
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(c.composeFile); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(dir, ".compose-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, c.composeFile)
}

// errImageDigestPinned — образ пришпилен по digest (…@sha256:…). Такой ref
// тега не имеет; прежний код дописывал тег прямо после «@sha256», получалась
// заведомо неверная ссылка и pull падал всегда.
var errImageDigestPinned = errors.New("образ сервиса bot пришпилен по digest — канал обновления менять нечему")

// botImageRef возвращает ссылку на образ сервиса bot как она записана.
func botImageRef(data []byte) (string, error) {
	_, _, ref, err := scanBotImage(data)
	if err != nil {
		return "", err
	}
	return ref, nil
}

// replaceBotImageTag заменяет тег в строке image сервиса bot, не трогая
// остальные байты. changed=false — тег уже нужный.
func replaceBotImageTag(data []byte, tag string) (out []byte, changed bool, err error) {
	lines, idx, ref, err := scanBotImage(data)
	if err != nil {
		return nil, false, err
	}
	if strings.Contains(ref, "@") {
		return nil, false, errImageDigestPinned
	}
	base := ref
	if j := strings.LastIndex(ref, ":"); j > strings.LastIndex(ref, "/") {
		base = ref[:j]
	}
	if base+":"+tag == ref {
		return data, false, nil
	}
	line := lines[idx]
	trimmed := strings.TrimSpace(line)
	_, quote, trail := splitImageValue(strings.TrimPrefix(trimmed, "image:"))
	lines[idx] = line[:len(line)-len(trimmed)] + "image: " + quote + base + ":" + tag + quote + trail
	return []byte(strings.Join(lines, "\n")), true, nil
}

// scanBotImage находит строку image у сервиса bot: её номер и значение.
func scanBotImage(data []byte) (lines []string, idx int, ref string, err error) {
	lines = strings.Split(string(data), "\n")
	inServices := false
	servicesIndent := -1
	svcIndent := -1
	curSvc := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if !inServices {
			if indent == 0 && (trimmed == "services:" || strings.HasPrefix(trimmed, "services:")) {
				inServices, servicesIndent = true, indent
			}
			continue
		}
		if indent <= servicesIndent {
			// Вышли из services — дальше сервиса bot быть не может.
			break
		}
		if svcIndent == -1 || indent == svcIndent {
			if name, ok := strings.CutSuffix(trimmed, ":"); ok && !strings.Contains(name, " ") {
				svcIndent, curSvc = indent, name
				continue
			}
		}
		if curSvc != "bot" || indent <= svcIndent {
			continue
		}
		rest, ok := strings.CutPrefix(trimmed, "image:")
		if !ok {
			continue
		}
		v, _, _ := splitImageValue(rest)
		if v == "" {
			return nil, 0, "", fmt.Errorf("у сервиса bot пустой image")
		}
		return lines, i, v, nil
	}
	return nil, 0, "", fmt.Errorf("в compose нет сервиса bot с image")
}

// splitImageValue разбирает хвост строки «image: …» на сам ref, кавычки
// вокруг него и хвостовой комментарий — чтобы вернуть строку в прежнем виде.
func splitImageValue(rest string) (ref, quote, trail string) {
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return "", "", ""
	}
	if q := rest[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			return rest[1 : 1+end], string(q), rest[2+end:]
		}
		return "", "", ""
	}
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		return rest[:sp], "", rest[sp:]
	}
	return rest, "", ""
}

// PortsBusy probes whether the given host ports are already in use. It runs a
// throwaway container in the HOST network namespace and inspects
// /proc/net/tcp{,6} for LISTEN sockets, so it needs no extra tools and no
// privileges beyond docker. Returns the busy subset of the requested ports.
// Used to refuse publishing a port before recreating the bot container, which
// would otherwise fail to bind and crash-loop the bot.
func (c *Controller) PortsBusy(ctx context.Context, ports ...int) ([]int, error) {
	want := map[string]int{}
	for _, p := range ports {
		want[fmt.Sprintf("%04X", p)] = p
	}
	script := `awk '{print $2, $4}' /proc/net/tcp /proc/net/tcp6 2>/dev/null | while read la st; do ` +
		`[ "$st" = "0A" ] && echo ${la##*:}; done | sort -u`
	args := []string{"run", "--rm", "--network", "host", "busybox", "sh", "-c", script}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("проверка портов: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var busy []int
	for _, hexp := range strings.Fields(string(out)) {
		if p, ok := want[strings.ToUpper(hexp)]; ok {
			busy = append(busy, p)
		}
	}
	return busy, nil
}

// WebhookPortsBusy reports whether host ports 80/443 are taken.
func (c *Controller) WebhookPortsBusy(ctx context.Context) ([]int, error) {
	return c.PortsBusy(ctx, 80, 443)
}

func (c *Controller) PublishWebhookPorts(ctx context.Context) error {
	if err := c.addWebhookPortsToCompose(); err != nil {
		return fmt.Errorf("правка compose: %w", err)
	}
	return c.runComposeDetached(ctx, fmt.Sprintf("docker compose -p %s up -d", c.project))
}

// setBotPorts rewrites the bot service "ports" in the compose file.
func (c *Controller) setBotPorts(ports []any) error {
	data, err := os.ReadFile(c.composeFile)
	if err != nil {
		return err
	}
	root := map[string]any{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	services, _ := root["services"].(map[string]any)
	if services == nil {
		return fmt.Errorf("в compose нет services")
	}
	bot, ok := services["bot"].(map[string]any)
	if !ok {
		return fmt.Errorf("в compose нет сервиса bot")
	}
	bot["ports"] = ports
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(c.composeFile, out, 0o600)
}

func (c *Controller) addWebhookPortsToCompose() error {
	return c.setBotPorts([]any{"80:80", "443:443"})
}

// PublishBotPort maps the bot's internal HTTP port to a HOST loopback port of
// the same number (127.0.0.1:port:port) and recreates the container, so an
// external reverse proxy (nginx/FastPanel) can reach the bot without exposing
// it publicly. Used by the in-bot "Bot port" setting so the admin doesn't have
// to edit compose by hand.
func (c *Controller) PublishBotPort(ctx context.Context, port int) error {
	mapping := fmt.Sprintf("127.0.0.1:%d:%d", port, port)
	if err := c.setBotPorts([]any{mapping}); err != nil {
		return fmt.Errorf("правка compose: %w", err)
	}
	return c.runComposeDetached(ctx, fmt.Sprintf("docker compose -p %s up -d", c.project))
}

func (c *Controller) compose(ctx context.Context, args ...string) error {
	full := append([]string{"compose", "-f", c.composeFile, "-p", c.project}, args...)
	if out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose %v: %w: %s", args, err, out)
	}
	return nil
}

func waitTCP(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("PostgreSQL не поднялся за %s", timeout)
}
