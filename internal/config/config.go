package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DataDir          string
	DatabaseURL      string
	Listen           string
	CheckInterval    time.Duration
	CrawlInterval    time.Duration
	CheckConcurrency int
	AuditSandbox     string
}

func Load() (Config, error) {
	c := Config{DataDir: "./data", Listen: "127.0.0.1:8090", CheckInterval: time.Minute, CrawlInterval: 6 * time.Hour, CheckConcurrency: 8, AuditSandbox: "strict"}
	if v := os.Getenv("WEBFLEET_DATABASE_URL"); v != "" {
		c.DatabaseURL = v
	}
	if v := os.Getenv("WEBFLEET_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("WEBFLEET_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("WEBFLEET_CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("WEBFLEET_CHECK_INTERVAL: %w", err)
		}
		c.CheckInterval = d
	}
	if v := os.Getenv("WEBFLEET_CRAWL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("WEBFLEET_CRAWL_INTERVAL: %w", err)
		}
		c.CrawlInterval = d
	}
	if v := os.Getenv("WEBFLEET_CHECK_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 128 {
			return c, fmt.Errorf("WEBFLEET_CHECK_CONCURRENCY must be 1..128")
		}
		c.CheckConcurrency = n
	}
	if v := os.Getenv("WEBFLEET_AUDIT_SANDBOX"); v != "" {
		if v != "strict" && v != "allow-no-sandbox" {
			return c, fmt.Errorf("WEBFLEET_AUDIT_SANDBOX must be strict or allow-no-sandbox")
		}
		c.AuditSandbox = v
	}
	abs, err := filepath.Abs(c.DataDir)
	if err == nil {
		c.DataDir = abs
	}
	if c.DatabaseURL == "" {
		if choice, e := LoadDatabaseChoice(c.DataDir); e == nil && choice.Provider == "postgres" {
			c.DatabaseURL = choice.URL
		}
	}
	return c, nil
}

type DatabaseChoice struct {
	Provider string `json:"provider"`
	URL      string `json:"url,omitempty"`
}

func DatabaseChoicePath(dataDir string) string { return filepath.Join(dataDir, "database.json") }
func LoadDatabaseChoice(dataDir string) (DatabaseChoice, error) {
	b, err := os.ReadFile(DatabaseChoicePath(dataDir))
	if os.IsNotExist(err) {
		return DatabaseChoice{Provider: "sqlite"}, nil
	}
	if err != nil {
		return DatabaseChoice{}, err
	}
	var c DatabaseChoice
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Provider == "" {
		c.Provider = "sqlite"
	}
	return c, nil
}
func SaveDatabaseChoice(dataDir string, c DatabaseChoice) error {
	if c.Provider != "sqlite" && c.Provider != "postgres" {
		return fmt.Errorf("unsupported database provider")
	}
	if c.Provider == "postgres" && strings.TrimSpace(c.URL) == "" {
		return fmt.Errorf("PostgreSQL URL is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dataDir, 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(DatabaseChoicePath(dataDir), append(b, '\n'), 0o600)
}
