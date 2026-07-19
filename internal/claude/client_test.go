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
	args := BuildArgs(Request{SessionID: "A", Prompt: "follow-up", MaxTurns: 12})
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
	if args[len(args)-1] != "follow-up" {
		t.Fatalf("prompt is not final arg: %v", args)
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
