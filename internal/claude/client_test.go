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
	fail := writeScript(t, dir, "fail", "#!/bin/sh\necho safe-error >&2\nexit 7\n")
	client := &CLIClient{Binary: fail, Timeout: time.Second, MaxOutputBytes: 4096}
	_, err := client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1})
	if !errors.Is(err, ErrProcess) || !strings.Contains(err.Error(), "safe-error") {
		t.Fatalf("err=%v", err)
	}
	slow := writeScript(t, dir, "slow", "#!/bin/sh\nsleep 5\n")
	client.Binary, client.Timeout = slow, 50*time.Millisecond
	_, err = client.Run(context.Background(), Request{RepositoryPath: dir, Prompt: "p", MaxTurns: 1})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err=%v", err)
	}
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
