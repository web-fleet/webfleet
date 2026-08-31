package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	DataDir          string
	Listen           string
	CheckInterval    time.Duration
	CrawlInterval    time.Duration
	CheckConcurrency int
}

func Load() (Config, error) {
	c := Config{DataDir: "./data", Listen: "127.0.0.1:8090", CheckInterval: time.Minute, CrawlInterval: 6 * time.Hour, CheckConcurrency: 8}
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
	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return c, fmt.Errorf("create data dir: %w", err)
	}
	abs, err := filepath.Abs(c.DataDir)
	if err == nil {
		c.DataDir = abs
	}
	return c, nil
}
