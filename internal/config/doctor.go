package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jbd/claude-reviewer/internal/session"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}
type DoctorReport struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

func Doctor(ctx context.Context, cfg Config) DoctorReport {
	report := DoctorReport{OK: true}
	add := func(name string, err error, detail string) {
		ok := err == nil
		if !ok {
			report.OK = false
			detail = err.Error()
		}
		report.Checks = append(report.Checks, Check{Name: name, OK: ok, Detail: detail})
	}
	binary, err := cfg.ResolveClaudeBinary()
	add("claude_binary", err, binary)
	if err == nil {
		out, runErr := command(ctx, binary, "--version")
		add("claude_version", runErr, strings.TrimSpace(out))
		authOut, authErr := command(ctx, binary, "auth", "status")
		var status struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal([]byte(authOut), &status) != nil || !status.LoggedIn {
			authErr = fmt.Errorf("claude auth status reports loggedIn=false")
		}
		add("claude_authentication", authErr, "authenticated")
		help, helpErr := command(ctx, binary, "--help")
		if helpErr == nil {
			for _, flag := range []string{"--resume", "--output-format", "--permission-mode", "--tools", "--disallowedTools", "--json-schema"} {
				if !strings.Contains(help, flag) {
					helpErr = fmt.Errorf("required flag missing: %s", flag)
					break
				}
			}
		}
		// Claude documents that --help may omit supported flags. An invalid numeric
		// value proves that this local parser recognizes --max-turns without an API call.
		_, probeErr := command(ctx, binary, "-p", "--max-turns", "not-a-number", "probe")
		if probeErr == nil || !strings.Contains(probeErr.Error(), "must be a number") {
			helpErr = fmt.Errorf("required flag unsupported: --max-turns")
		}
		add("claude_flags", helpErr, "all required flags supported")
	}
	gitPath, gitErr := exec.LookPath("git")
	add("git", gitErr, gitPath)
	dataErr := os.MkdirAll(cfg.DataDir, 0o700)
	if dataErr == nil {
		f, e := os.CreateTemp(cfg.DataDir, ".doctor-*.tmp")
		dataErr = e
		if e == nil {
			name := f.Name()
			if e = f.Sync(); e != nil {
				dataErr = e
			}
			if e = f.Close(); e != nil && dataErr == nil {
				dataErr = e
			}
			_ = os.Remove(name)
		}
	}
	add("data_directory_writable", dataErr, cfg.DataDir)
	storeErr := session.NewJSONStore(cfg.SessionsPath()).Validate(ctx)
	add("session_storage", storeErr, filepath.Join(cfg.DataDir, "sessions.json"))
	return report
}

func command(parent context.Context, binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
