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
	selfContainer  string
}

func New() *Controller {
	return &Controller{
		composeFile:    env("COMPOSE_FILE_PATH", "/compose/docker-compose.yml"),
		project:        env("COMPOSE_PROJECT", "remnachillbot"),
		hostDir:        env("COMPOSE_HOST_DIR", "/opt/remnachillbot"),
		panelContainer: env("PANEL_CONTAINER", "remnawave"),
		selfContainer:  env("SELF_CONTAINER", "remnabot"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

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
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f",
		`{{range $k,$_ := .NetworkSettings.Networks}}{{$k}} {{end}}`, c.panelContainer).Output()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", c.panelContainer, err)
	}
	connected := false
	for _, netName := range strings.Fields(string(out)) {
		switch netName {
		case "bridge", "host", "none":
			continue
		}
		o, e := exec.CommandContext(ctx, "docker", "network", "connect", netName, c.selfContainer).CombinedOutput()
		if e == nil || strings.Contains(string(o), "already exists") {
			connected = true
		}
	}
	if !connected {
		return fmt.Errorf("у контейнера %q не найдено пользовательских сетей", c.panelContainer)
	}
	return nil
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
	return os.WriteFile(c.composeFile, out, 0o644)
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
		return fmt.Errorf("compose detached: %v: %s", err, out)
	}
	return nil
}

func (c *Controller) SelfUpdate(ctx context.Context) error {
	return c.runComposeDetached(ctx, fmt.Sprintf("docker compose -p %s pull && docker compose -p %s up -d", c.project, c.project))
}

// PublishWebhookPorts добавляет порты 80/443 сервису bot и пересоздаёт его (detached),
// чтобы встроенный HTTPS-сервер (autocert) стал доступен снаружи.
func (c *Controller) PublishWebhookPorts(ctx context.Context) error {
	if err := c.addWebhookPortsToCompose(); err != nil {
		return fmt.Errorf("правка compose: %w", err)
	}
	return c.runComposeDetached(ctx, fmt.Sprintf("docker compose -p %s up -d", c.project))
}

func (c *Controller) addWebhookPortsToCompose() error {
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
	bot["ports"] = []any{"80:80", "443:443"}
	out, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(c.composeFile, out, 0o644)
}

func (c *Controller) compose(ctx context.Context, args ...string) error {
	full := append([]string{"compose", "-f", c.composeFile, "-p", c.project}, args...)
	if out, err := exec.CommandContext(ctx, "docker", full...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose %v: %v: %s", args, err, out)
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
