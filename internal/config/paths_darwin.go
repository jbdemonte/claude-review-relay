package config

import (
	"errors"
	"os"
	"path/filepath"
)

func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", errors.New("home directory is empty")
	}
	return filepath.Join(home, "Library", "Application Support", "claude-reviewer"), nil
}
