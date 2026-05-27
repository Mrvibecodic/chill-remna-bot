// Package hostctl позволяет боту управлять docker compose на хосте через
// смонтированный docker.sock: поднять PostgreSQL по выбору пользователя
// (дописав сервис в compose-файл) и обновить себя.
package hostctl

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"
)

// PostgresDSN — строка подключения к поднимаемому ботом PostgreSQL.
const PostgresDSN = "postgres://remnabot:remnabot@db:5432/remnabot?sslmode=disable"

type Controller struct {
	composeFile string
	project     string
	hostDir     string
}

func New() *Controller {
	return &Controller{
		composeFile: env("COMPOSE_FILE_PATH", "/compose/docker-compose.yml"),
		project:     env("COMPOSE_PROJECT", "remnachillbot"),
		hostDir:     env("COMPOSE_HOST_DIR", "/opt/remnachillbot"),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Available сообщает, может ли бот управлять docker compose.
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

// EnablePostgres дописывает сервис db, поднимает его и ждёт готовности.
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
		"networks": []any{"remnawave-network"},
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

// SelfUpdate запускает отдельный одноразовый контейнер-контроллер (pull+up).
func (c *Controller) SelfUpdate(ctx context.Context) error {
	script := fmt.Sprintf("docker compose -p %s pull && docker compose -p %s up -d", c.project, c.project)
	args := []string{
		"run", "-d", "--rm",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", c.hostDir + ":/p",
		"-w", "/p",
		"docker:cli",
		"sh", "-c", script,
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("self-update: %v: %s", err, out)
	}
	return nil
}

func (c *Controller) compose(ctx context.Context, args ...string) error {
	full := append([]string{"compose", "-f", c.composeFile, "-p", c.project}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
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
