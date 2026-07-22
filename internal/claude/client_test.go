package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const validStructured = `{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}`

func TestParseStreamExtractsSessionAndStructuredOutput(t *testing.T) {
	stream := "not-json\n" +
		`{"type":"system","subtype":"init","session_id":"A"}` + "\n" +
		`{"type":"result","subtype":"success","structured_output":` + validStructured + `}` + "\n"
	r, err := ParseStream(strings.NewReader(stream), 4096, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	_ = json.Unmarshal(r.StructuredOutput, &got)
	_ = json.Unmarshal([]byte(validStructured), &want)
	if r.SessionID != "A" || !deepEqualJSON(got, want) {
		t.Fatalf("result=%+v output=%s", r, r.StructuredOutput)
	}
}

func deepEqualJSON(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}

func TestParseStreamFindsNestedSessionAndResultText(t *testing.T) {
	stream := `{"wrapper":{"type":"system","session_id":"B"}}` + "\n" +
		`{"type":"result","result":` + quote(validStructured) + `}` + "\n"
	r, err := ParseStream(strings.NewReader(stream), 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.SessionID != "B" || len(r.StructuredOutput) == 0 {
		t.Fatalf("result=%+v", r)
	}
}

func TestParseStreamCapturesTerminalFailure(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"A"}` + "\n" +
		`{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":5,"terminal_reason":"max_turns","errors":["Reached maximum number of turns (4)"]}` + "\n"
	result, err := ParseStream(strings.NewReader(stream), 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.TerminalSubtype != "error_max_turns" || result.TerminalReason != "max_turns" || !result.TerminalIsError || result.NumTurns != 5 {
		t.Fatalf("result=%+v", result)
	}
	if len(result.TerminalErrors) != 1 || result.TerminalErrors[0] != "Reached maximum number of turns (4)" {
		t.Fatalf("terminal errors=%v", result.TerminalErrors)
	}
}

func TestParseStreamCapturesRejectedRateLimitAndResetTime(t *testing.T) {
	stream := `{"type":"system","subtype":"init","session_id":"A"}` + "\n" +
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784718600,"rateLimitType":"five_hour"},"session_id":"A"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"session_id":"A","terminal_reason":"api_error"}` + "\n"
	result, err := ParseStream(strings.NewReader(stream), 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantReset := time.Unix(1784718600, 0).UTC()
	if result.SessionID != "A" || result.RateLimitStatus != "rejected" || result.RateLimitType != "five_hour" || result.APIErrorStatus != 429 || result.RateLimitResetAt == nil || !result.RateLimitResetAt.Equal(wantReset) {
		t.Fatalf("result=%+v", result)
	}
}

func TestParseStreamKeepsLatestRateLimitStatus(t *testing.T) {
	stream := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784718600,"rateLimitType":"five_hour"}}` + "\n" +
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","resetsAt":1784737800,"rateLimitType":"five_hour"}}` + "\n"
	result, err := ParseStream(strings.NewReader(stream), 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantReset := time.Unix(1784737800, 0).UTC()
	if result.RateLimitStatus != "allowed" || result.RateLimitResetAt == nil || !result.RateLimitResetAt.Equal(wantReset) {
		t.Fatalf("result=%+v", result)
	}
}

func TestBuildArgsUsesExplicitResumeAndReadOnlyTools(t *testing.T) {
	args := BuildArgs(Request{SessionID: "A", Prompt: "follow-up", Model: "fable", FallbackModel: "opus", Effort: "max", MaxTurns: 12})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--resume A") {
		t.Fatalf("args=%v", args)
	}
	if strings.Contains(joined, "--continue") {
		t.Fatalf("args=%v", args)
	}
	if !strings.Contains(joined, "--tools Read,Glob,Grep") || !strings.Contains(joined, "Bash") {
		t.Fatalf("args=%v", args)
	}
	if !strings.Contains(joined, "--model fable --fallback-model opus --effort max") {
		t.Fatalf("model strategy missing: %v", args)
	}
	if strings.Contains(joined, "follow-up") {
		t.Fatalf("prompt leaked into argv: %v", args)
	}
}

func TestCLIClientProcessFailureAndTimeout(t *testing.T) {
	dir := t.TempDir()
	fail := writeScript(t, dir, "fail", "#!/bin/sh\necho 'API_TOKEN=very-secret-value' >&2\nexit 7\n")
	client := &CLIClient{Binary: fail, Timeout: time.Second, MaxOutputBytes: 4096}
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1})
	var runErr *RunError
	if !errors.Is(err, ErrProcess) || !errors.As(err, &runErr) {
		t.Fatalf("err=%v", err)
	}
	details := runErr.PublicDetails()
	if details["stage"] != StageProcessExit || details["exit_code"] != 7 || details["stderr_excerpt"] != "API_TOKEN=[REDACTED]" {
		t.Fatalf("details=%v", details)
	}
	slow := writeScript(t, dir, "slow", "#!/bin/sh\nsleep 5\n")
	client.Binary, client.Timeout = slow, 50*time.Millisecond
	_, err = client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1})
	if !errors.Is(err, ErrTimeout) || !errors.As(err, &runErr) || runErr.Stage != StageProcessExit {
		t.Fatalf("err=%v", err)
	}
}

func TestCLIClientClassifiesQuotaBeforeGenericProcessFailure(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%s\n' '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784718600,"rateLimitType":"five_hour"},"session_id":"A"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"session_id":"A","terminal_reason":"api_error"}'
exit 1
`
	client := &CLIClient{Binary: writeScript(t, dir, "quota", script), Timeout: time.Second, MaxOutputBytes: 4096}
	result, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", Model: "fable", MaxTurns: 1})
	var runErr *RunError
	if !errors.Is(err, ErrQuotaExceeded) || !errors.As(err, &runErr) || result.SessionID != "A" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	details := runErr.PublicDetails()
	if details["api_error_status"] != 429 || details["rate_limit_type"] != "five_hour" || details["retry_at"] != time.Unix(1784718600, 0).UTC().Format(time.RFC3339) {
		t.Fatalf("details=%v", details)
	}
}

func TestCLIClientKeepsSuccessfulOutputAfterRejectedRateLimitEvent(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%s\n' '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1784718600,"rateLimitType":"five_hour"},"session_id":"A"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"A","structured_output":{"verdict":"approve","summary":"recovered","findings":[],"missing_tests":[]}}'
`
	client := &CLIClient{Binary: writeScript(t, dir, "quota-recovered", script), Timeout: time.Second, MaxOutputBytes: 4096}
	result, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", Model: "fable", MaxTurns: 1})
	if err != nil || len(result.StructuredOutput) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunErrorDoesNotExposeInformationalRetryAtForOtherFailures(t *testing.T) {
	retryAt := time.Unix(1784718600, 0).UTC()
	err := (&RunError{Kind: ErrMaxTurns, RetryAt: &retryAt, RateLimitType: "five_hour"}).PublicDetails()
	if err["retry_at"] != nil || err["rate_limit_type"] != nil {
		t.Fatalf("details=%v", err)
	}
}

func TestCLIClientUsesPerRequestTimeout(t *testing.T) {
	dir := t.TempDir()
	slow := writeScript(t, dir, "request-timeout", "#!/bin/sh\nsleep 5\n")
	client := &CLIClient{Binary: slow, Timeout: time.Second, MaxOutputBytes: 4096}
	started := time.Now()
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1, Timeout: 30 * time.Millisecond})
	var runErr *RunError
	if !errors.Is(err, ErrTimeout) || !errors.As(err, &runErr) {
		t.Fatalf("err=%v", err)
	}
	if runErr.TimeoutSeconds != 1 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("timeout details=%+v elapsed=%v", runErr, time.Since(started))
	}
}

func TestCLIClientClassifiesParentCancellation(t *testing.T) {
	dir := t.TempDir()
	slow := writeScript(t, dir, "canceled", "#!/bin/sh\nsleep 5\n")
	client := &CLIClient{Binary: slow, Timeout: time.Second, MaxOutputBytes: 4096}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	_, err := client.Run(ctx, Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1})
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestCLIClientReportsMaxTurnsWithActionableDetails(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%s\n' '{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":5,"terminal_reason":"max_turns","session_id":"A","errors":["Reached maximum number of turns (4)"]}'
exit 1
`
	client := &CLIClient{Binary: writeScript(t, dir, "max-turns", script), Timeout: time.Second, MaxOutputBytes: 4096}
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", Model: "sonnet", FallbackModel: "opus", Effort: "high", MaxTurns: 4})
	var runErr *RunError
	if !errors.Is(err, ErrMaxTurns) || !errors.As(err, &runErr) {
		t.Fatalf("err=%v", err)
	}
	details := runErr.PublicDetails()
	if details["terminal_reason"] != "max_turns" || details["terminal_subtype"] != "error_max_turns" || details["num_turns"] != 5 || details["max_turns"] != 4 || details["model"] != "sonnet" {
		t.Fatalf("details=%v", details)
	}
	wantArgs := []string{"-p", "--output-format", "--verbose", "--permission-mode", "--tools", "--disallowedTools", "--max-turns", "--model", "--fallback-model", "--effort"}
	if got := details["argument_names"]; !equalStrings(got.([]string), wantArgs) {
		t.Fatalf("argument_names=%v", got)
	}
}

func TestCLIClientReportsMaxTurnsWhenClaudeExitsZero(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%s\n' '{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":4,"terminal_reason":"max_turns","session_id":"A","errors":["limit reached"]}'
`
	client := &CLIClient{Binary: writeScript(t, dir, "max-turns-zero", script), Timeout: time.Second, MaxOutputBytes: 4096}
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 4})
	var runErr *RunError
	if !errors.Is(err, ErrMaxTurns) || !errors.As(err, &runErr) || runErr.ExitCode == nil || *runErr.ExitCode != 0 {
		t.Fatalf("err=%v runErr=%+v", err, runErr)
	}
}

func TestCLIClientKeepsStructuredOutputAtTurnLimit(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"terminal_reason":"max_turns","session_id":"A","structured_output":{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}}'
`
	client := &CLIClient{Binary: writeScript(t, dir, "turn-limit-success", script), Timeout: time.Second, MaxOutputBytes: 4096}
	result, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 4})
	if err != nil || len(result.StructuredOutput) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunErrorCapsTerminalErrors(t *testing.T) {
	messages := make([]string, 20)
	for i := range messages {
		messages[i] = "error"
	}
	client := &CLIClient{}
	err := client.runError(Request{MaxTurns: 1}, []string{"-p"}, StreamResult{TerminalErrors: messages}, ErrProcess, StageProcessExit, errors.New("failed"), nil, "")
	var runErr *RunError
	if !errors.As(err, &runErr) || len(runErr.TerminalErrors) != 6 || !strings.HasPrefix(runErr.TerminalErrors[5], "[TRUNCATED:") {
		t.Fatalf("err=%v terminal_errors=%v", err, runErr.TerminalErrors)
	}
}

func TestSanitizeDiagnosticBoundsAndControlCharacters(t *testing.T) {
	input := strings.Repeat("a", 1023) + "é" + "\x00\x01\x1b"
	got := sanitizeDiagnostic(input, 1024)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || strings.ContainsAny(got, "\x00\x01\x1b") {
		t.Fatalf("sanitized diagnostic is invalid: %q", got)
	}
	if len(strings.TrimSuffix(got, "…")) != 1023 {
		t.Fatalf("unexpected truncation length: %d", len(strings.TrimSuffix(got, "…")))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCLIClientSendsLargePromptOnStdin(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
bytes=$(wc -c | tr -d ' ')
[ "$bytes" -gt 1048576 ] || exit 9
printf '%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%s\n' '{"type":"result","session_id":"A","structured_output":{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}}'
`
	client := &CLIClient{Binary: writeScript(t, dir, "stdin", script), Timeout: 5 * time.Second, MaxOutputBytes: 4096}
	result, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: strings.Repeat("x", 1200*1024), MaxTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "A" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCLIClientKillsProcessWhenOutputLimitExceeded(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\nwhile :; do printf 'not-json-output-not-json-output-not-json-output\\n'; done\n"
	client := &CLIClient{Binary: writeScript(t, dir, "overflow", script), Timeout: 5 * time.Second, MaxOutputBytes: 1024}
	started := time.Now()
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1})
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("output overflow was not stopped promptly: %v", elapsed)
	}
}

func TestCLIClientRejectsChangedResumedSessionID(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"B"}'
printf '%s\n' '{"type":"result","session_id":"B","structured_output":{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}}'
`
	client := &CLIClient{Binary: writeScript(t, dir, "changed-session", script), Timeout: time.Second, MaxOutputBytes: 4096}
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", SessionID: "A", MaxTurns: 1})
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err=%v", err)
	}
}

func TestCLIClientCachesSuccessfulAuthentication(t *testing.T) {
	dir := t.TempDir()
	countPath := filepath.Join(dir, "auth-count")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = auth ]; then echo checked >> '" + countPath + "'; printf '%s\\n' '{\"loggedIn\":true}'; exit 0; fi\n" +
		"cat >/dev/null\n" +
		"printf '%s\\n' '{\"type\":\"system\",\"session_id\":\"A\"}'\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"session_id\":\"A\",\"structured_output\":{" + validStructured[1:] + "}'\n"
	client := &CLIClient{Binary: writeScript(t, dir, "auth-cache", script), Timeout: time.Second, MaxOutputBytes: 4096, CheckAuthentication: true}
	for range 2 {
		if _, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "checked"); got != 1 {
		t.Fatalf("auth checks=%d", got)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
