package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Config struct {
	ClaudeBinary         string `json:"claude_binary"`
	DefaultModel         string `json:"default_model"`
	DefaultFallbackModel string `json:"default_fallback_model"`
	DefaultEffort        string `json:"default_effort"`
	DefaultMaxTurns      int    `json:"default_max_turns"`
	TimeoutSeconds       int    `json:"timeout_seconds"`
	AsyncTimeoutSeconds  int    `json:"async_timeout_seconds"`
	MaxDiffBytes         int64  `json:"max_diff_bytes"`
	MaxOutputBytes       int64  `json:"max_output_bytes"`
	LogLevel             string `json:"log_level"`
	SessionRetentionDays int    `json:"session_retention_days"`
	DataDir              string `json:"-"`
}

func Defaults() (Config, error) {
	dir, err := DefaultDataDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		DefaultModel: "fable", DefaultFallbackModel: "opus", DefaultEffort: "max",
		DefaultMaxTurns: 12, TimeoutSeconds: 240, AsyncTimeoutSeconds: 1200,
		MaxDiffBytes: 2 * 1024 * 1024, MaxOutputBytes: 8 * 1024 * 1024,
		LogLevel: "info", SessionRetentionDays: 30, DataDir: dir,
	}, nil
}

func Load() (Config, error) {
	cfg, err := Defaults()
	if err != nil {
		return Config{}, err
	}
	path := filepath.Join(cfg.DataDir, "config.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	cfg.DataDir = filepath.Dir(path)
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validate(cfg Config) error {
	if cfg.DefaultModel == "" {
		return errors.New("config default_model must not be empty")
	}
	if cfg.DefaultFallbackModel == "" {
		return errors.New("config default_fallback_model must not be empty")
	}
	if !ValidEffort(cfg.DefaultEffort) {
		return fmt.Errorf("config default_effort %q must be low, medium, high, xhigh, or max", cfg.DefaultEffort)
	}
	if cfg.DefaultMaxTurns <= 0 {
		return errors.New("config default_max_turns must be greater than zero")
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 1200 {
		return fmt.Errorf("config timeout_seconds must be between 1 and 1200; got %d", cfg.TimeoutSeconds)
	}
	if cfg.AsyncTimeoutSeconds < 1 || cfg.AsyncTimeoutSeconds > 1200 {
		return fmt.Errorf("config async_timeout_seconds must be between 1 and 1200; got %d", cfg.AsyncTimeoutSeconds)
	}
	if cfg.MaxDiffBytes <= 0 {
		return errors.New("config max_diff_bytes must be greater than zero")
	}
	if cfg.MaxOutputBytes <= 0 {
		return errors.New("config max_output_bytes must be greater than zero")
	}
	return nil
}

func ValidEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func (c Config) Timeout() time.Duration { return time.Duration(c.TimeoutSeconds) * time.Second }
func (c Config) SessionsPath() string   { return filepath.Join(c.DataDir, "sessions.json") }

func (c Config) ResolveClaudeBinary() (string, error) {
	if c.ClaudeBinary != "" {
		if err := executable(c.ClaudeBinary); err != nil {
			return "", fmt.Errorf("configured claude binary: %w", err)
		}
		return c.ClaudeBinary, nil
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/claude", "/usr/local/bin/claude"} {
		if executable(p) == nil {
			return p, nil
		}
	}
	return "", errors.New("claude executable not found")
}

func executable(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.IsDir() || st.Mode().Perm()&0o111 == 0 {
		return errors.New("path is not executable")
	}
	return nil
}
