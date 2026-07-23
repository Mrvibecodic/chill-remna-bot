package hostctl

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
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

func (c *Controller) runComposeDetached(ctx context.Context, script string) error {
	args := []string{
		"run", "-d", "--rm",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", c.hostDir + ":/p",
		"-w", "/p",
		"docker:cli",
		"sh", "-c", script,
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("compose detached: %w: %s", err, out)
	}
	return nil
}

func (c *Controller) SelfUpdate(ctx context.Context) error {
	return c.runComposeDetached(ctx, fmt.Sprintf("docker compose -p %s pull && docker compose -p %s up -d", c.project, c.project))
}

// SetImageChannel rewrites the tag of the bot service image in the compose file
// (e.g. ...:latest -> ...:dev), preserving the registry/repository part.
func (c *Controller) SetImageChannel(tag string) error {
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
	img, _ := bot["image"].(string)
	if img == "" {
		return fmt.Errorf("у сервиса bot нет image")
	}
	base := img
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		base = img[:i]
	}
	bot["image"] = base + ":" + tag
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(c.composeFile, out, 0o600)
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
