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
	"unicode/utf8"

	"github.com/jbd/claude-reviewer/internal/security"
)

var (
	ErrTimeout          = errors.New("claude process timed out")
	ErrCanceled         = errors.New("claude process canceled")
	ErrProcess          = errors.New("claude process failed")
	ErrSessionIDMissing = errors.New("claude session id missing")
	ErrInvalidOutput    = errors.New("invalid claude output")
	ErrNotFound         = errors.New("claude executable not found")
	ErrNotAuthenticated = errors.New("claude is not authenticated")
	ErrOutputTooLarge   = errors.New("claude output exceeded configured limit")
	ErrMaxTurns         = errors.New("claude reached the maximum number of turns")
)

const (
	StageAuthentication          = "authentication"
	StageProcessStart            = "process_start"
	StageProcessExit             = "process_exit"
	StageStreamParsing           = "stream_parsing"
	StageMissingSessionID        = "missing_session_id"
	StageMissingStructuredOutput = "missing_structured_output"
)

type RunError struct {
	Kind            error
	Stage           string
	Cause           error
	ExitCode        *int
	CauseExcerpt    string
	StderrExcerpt   string
	TerminalSubtype string
	TerminalIsError bool
	TerminalReason  string
	TerminalErrors  []string
	EventCount      int
	NumTurns        int
	MaxTurns        int
	TimeoutSeconds  int
	Model           string
	ArgumentNames   []string
}

func (e *RunError) Error() string {
	message := "claude invocation failed at " + e.Stage
	if e.TerminalReason != "" {
		message += ": " + e.TerminalReason
	}
	return message
}

func (e *RunError) Unwrap() error { return e.Kind }

func (e *RunError) PublicDetails() map[string]any {
	details := map[string]any{
		"stage":           e.Stage,
		"event_count":     e.EventCount,
		"max_turns":       e.MaxTurns,
		"timeout_seconds": e.TimeoutSeconds,
		"argument_names":  append([]string(nil), e.ArgumentNames...),
	}
	if e.Model != "" {
		details["model"] = e.Model
	}
	if e.ExitCode != nil {
		details["exit_code"] = *e.ExitCode
	}
	if e.CauseExcerpt != "" {
		details["cause_excerpt"] = e.CauseExcerpt
	}
	if e.StderrExcerpt != "" {
		details["stderr_excerpt"] = e.StderrExcerpt
	}
	if e.TerminalSubtype != "" {
		details["terminal_subtype"] = e.TerminalSubtype
	}
	if e.TerminalIsError {
		details["terminal_is_error"] = true
	}
	if e.TerminalReason != "" {
		details["terminal_reason"] = e.TerminalReason
	}
	if len(e.TerminalErrors) > 0 {
		details["terminal_errors"] = append([]string(nil), e.TerminalErrors...)
	}
	if e.NumTurns > 0 {
		details["num_turns"] = e.NumTurns
	}
	return details
}

type Request struct {
	RepositoryPath, Prompt, SystemPrompt, Schema, Model, FallbackModel, Effort, SessionID string
	MaxTurns                                                                              int
	Timeout                                                                               time.Duration
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
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = c.Timeout
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	req.Timeout = timeout
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := BuildArgs(req)
	if c.CheckAuthentication {
		if err := c.checkAuth(ctx); err != nil {
			return StreamResult{}, c.runError(req, args, StreamResult{}, ErrNotAuthenticated, StageAuthentication, err, nil, "")
		}
	}
	if c.Logger != nil {
		c.Logger.Debug("starting claude process", "argument_names", ArgumentNames(args), "model", req.Model, "max_turns", req.MaxTurns)
	}
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = req.RepositoryPath
	cmd.Stdin = strings.NewReader(req.Prompt)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return StreamResult{}, c.runError(req, args, StreamResult{}, ErrProcess, StageProcessStart, err, nil, "")
	}
	var stderr cappedBuffer
	stderr.limit = 64 * 1024
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return StreamResult{}, c.runError(req, args, StreamResult{}, ErrProcess, StageProcessStart, err, nil, stderr.String())
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
	exitCode := processExitCode(waitErr)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return result, c.runError(req, args, result, ErrTimeout, StageProcessExit, ctx.Err(), exitCode, stderr.String())
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return result, c.runError(req, args, result, ErrCanceled, StageProcessExit, ctx.Err(), exitCode, stderr.String())
	}
	if errors.Is(parseErr, ErrOutputTooLarge) {
		return result, c.runError(req, args, result, ErrOutputTooLarge, StageStreamParsing, parseErr, exitCode, stderr.String())
	}
	if parseErr != nil {
		return result, c.runError(req, args, result, ErrInvalidOutput, StageStreamParsing, parseErr, exitCode, stderr.String())
	}
	terminalMaxTurns := result.TerminalReason == "max_turns" || result.TerminalSubtype == "error_max_turns"
	if terminalMaxTurns && (result.TerminalIsError || len(result.StructuredOutput) == 0) {
		return result, c.runError(req, args, result, ErrMaxTurns, StageProcessExit, waitErr, exitCode, stderr.String())
	}
	if waitErr != nil {
		return result, c.runError(req, args, result, ErrProcess, StageProcessExit, waitErr, exitCode, stderr.String())
	}
	if req.SessionID == "" && result.SessionID == "" {
		return result, c.runError(req, args, result, ErrSessionIDMissing, StageMissingSessionID, nil, exitCode, stderr.String())
	}
	if req.SessionID != "" && result.SessionID != "" && result.SessionID != req.SessionID {
		return result, c.runError(req, args, result, ErrInvalidOutput, StageStreamParsing, errors.New("resumed session id changed"), exitCode, stderr.String())
	}
	if len(result.StructuredOutput) == 0 {
		return result, c.runError(req, args, result, ErrInvalidOutput, StageMissingStructuredOutput, errors.New("structured_output missing"), exitCode, stderr.String())
	}
	return result, nil
}

func (c *CLIClient) runError(req Request, args []string, result StreamResult, kind error, stage string, cause error, exitCode *int, stderr string) error {
	excerpt := sanitizeDiagnostic(stderr, 1024)
	var causeExcerpt string
	if cause != nil {
		causeExcerpt = sanitizeDiagnostic(cause.Error(), 512)
	}
	const maxTerminalErrors = 5
	count := len(result.TerminalErrors)
	if count > maxTerminalErrors {
		count = maxTerminalErrors
	}
	terminalErrors := make([]string, 0, count+1)
	for _, message := range result.TerminalErrors[:count] {
		terminalErrors = append(terminalErrors, sanitizeDiagnostic(message, 512))
	}
	if len(result.TerminalErrors) > maxTerminalErrors {
		terminalErrors = append(terminalErrors, "[TRUNCATED: additional terminal errors omitted]")
	}
	err := &RunError{
		Kind: kind, Stage: stage, Cause: cause, ExitCode: exitCode, CauseExcerpt: causeExcerpt,
		StderrExcerpt: excerpt, TerminalSubtype: result.TerminalSubtype, TerminalIsError: result.TerminalIsError,
		TerminalReason: result.TerminalReason, TerminalErrors: terminalErrors,
		EventCount: result.EventCount, NumTurns: result.NumTurns, MaxTurns: req.MaxTurns,
		TimeoutSeconds: int((req.Timeout + time.Second - 1) / time.Second),
		Model:          req.Model, ArgumentNames: ArgumentNames(args),
	}
	if c.Logger != nil {
		c.Logger.Error("claude invocation failed",
			"stage", err.Stage,
			"exit_code", exitCodeValue(err.ExitCode),
			"cause_excerpt", err.CauseExcerpt,
			"stderr_excerpt", err.StderrExcerpt,
			"terminal_subtype", err.TerminalSubtype,
			"terminal_is_error", err.TerminalIsError,
			"terminal_reason", err.TerminalReason,
			"terminal_errors", err.TerminalErrors,
			"event_count", err.EventCount,
			"num_turns", err.NumTurns,
			"max_turns", err.MaxTurns,
			"model", err.Model,
			"argument_names", err.ArgumentNames,
		)
	}
	return err
}

func processExitCode(err error) *int {
	if err == nil {
		code := 0
		return &code
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		return &code
	}
	return nil
}

func exitCodeValue(code *int) any {
	if code == nil {
		return nil
	}
	return *code
}

func sanitizeDiagnostic(value string, maxBytes int) string {
	value = security.RedactDiagnostic(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' {
			return r
		}
		return -1
	}, value)
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value + "…"
}

func ArgumentNames(args []string) []string {
	names := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-p" || strings.HasPrefix(arg, "--") {
			names = append(names, arg)
		}
	}
	return names
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
