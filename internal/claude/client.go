package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	ErrTimeout          = errors.New("claude process timed out")
	ErrProcess          = errors.New("claude process failed")
	ErrSessionIDMissing = errors.New("claude session id missing")
	ErrInvalidOutput    = errors.New("invalid claude output")
	ErrNotFound         = errors.New("claude executable not found")
	ErrNotAuthenticated = errors.New("claude is not authenticated")
	ErrOutputTooLarge   = errors.New("claude output exceeded configured limit")
)

type Request struct {
	RepositoryPath, Prompt, SystemPrompt, Schema, Model, FallbackModel, Effort, SessionID string
	MaxTurns                                                                              int
}

type Client interface {
	Run(context.Context, Request) (StreamResult, error)
}

type CLIClient struct {
	Binary              string
	Timeout             time.Duration
	MaxOutputBytes      int64
	Logger              *slog.Logger
	CheckAuthentication bool
	authMu              sync.Mutex
	authOK              bool
}

func (c *CLIClient) Run(ctx context.Context, req Request) (StreamResult, error) {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if c.CheckAuthentication {
		if err := c.checkAuth(ctx); err != nil {
			return StreamResult{}, err
		}
	}
	args := BuildArgs(req)
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = req.RepositoryPath
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return StreamResult{}, err
	}
	var stderr cappedBuffer
	stderr.limit = 64 * 1024
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return StreamResult{}, fmt.Errorf("%w: %v", ErrProcess, err)
	}
	debug := func(line string) {
		if c.Logger != nil {
			c.Logger.Debug("unrecognized claude stream event", "line_bytes", len(line))
		}
	}
	result, parseErr := ParseStream(stdout, c.MaxOutputBytes, debug)
	if parseErr != nil {
		_ = killProcessGroup(cmd)
	}
	waitErr := cmd.Wait()
	if c.Logger != nil {
		exitCode := 0
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		c.Logger.Debug("claude process completed", "exit_code", exitCode)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, ErrTimeout
	}
	if errors.Is(parseErr, ErrOutputTooLarge) {
		return result, ErrOutputTooLarge
	}
	if parseErr != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalidOutput, parseErr)
	}
	if waitErr != nil {
		return result, fmt.Errorf("%w: %v: %s", ErrProcess, waitErr, stderr.String())
	}
	if req.SessionID == "" && result.SessionID == "" {
		return result, ErrSessionIDMissing
	}
	if req.SessionID != "" && result.SessionID != "" && result.SessionID != req.SessionID {
		return result, fmt.Errorf("%w: resumed session changed from %s to %s", ErrInvalidOutput, req.SessionID, result.SessionID)
	}
	if len(result.StructuredOutput) == 0 {
		return result, fmt.Errorf("%w: structured_output missing", ErrInvalidOutput)
	}
	return result, nil
}

func (c *CLIClient) checkAuth(ctx context.Context) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.authOK {
		return nil
	}
	cmd := exec.CommandContext(ctx, c.Binary, "auth", "status")
	var stdout cappedBuffer
	stdout.limit = 16 * 1024
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrNotAuthenticated, err)
	}
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &status); err == nil && status.LoggedIn != nil && !*status.LoggedIn {
		return ErrNotAuthenticated
	}
	c.authOK = true
	return nil
}

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

type FailedClient struct{ Err error }

func (c FailedClient) Run(context.Context, Request) (StreamResult, error) {
	return StreamResult{}, c.Err
}

func BuildArgs(req Request) []string {
	args := []string{"-p"}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	args = append(args,
		"--output-format", "stream-json", "--verbose", "--permission-mode", "dontAsk",
		"--tools", "Read,Glob,Grep",
		"--disallowedTools", "Edit,Write,NotebookEdit,Bash,WebSearch,WebFetch,mcp__*",
		"--max-turns", strconv.Itoa(req.MaxTurns),
	)
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.FallbackModel != "" {
		args = append(args, "--fallback-model", req.FallbackModel)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.Schema != "" {
		args = append(args, "--json-schema", req.Schema)
	}
	if req.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", req.SystemPrompt)
	}
	return args
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buffer.Write(p)
	}
	return original, nil
}
func (b *cappedBuffer) String() string { return strings.TrimSpace(b.buffer.String()) }
