package reviewer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/claude"
	gitservice "github.com/jbd/claude-reviewer/internal/git"
	"github.com/jbd/claude-reviewer/internal/session"
)

func TestReviewAndResumeSurviveStoreRestartWithFakeClaude(t *testing.T) {
	repo := integrationRepo(t)
	logPath := filepath.Join(t.TempDir(), "resume.log")
	fake := filepath.Join(t.TempDir(), "claude")
	script := fmt.Sprintf(`#!/bin/sh
resume="NONE"
previous=""
for arg in "$@"; do
  if [ "$previous" = "--resume" ]; then resume="$arg"; fi
  previous="$arg"
done
prompt=$(cat)
printf '%%s\n' "$resume" >> %q
if [ "$resume" = "NONE" ]; then
  case "$prompt" in *"original goal"*) ;; *) exit 11;; esac
else
  case "$prompt" in *"original goal"*) exit 12;; esac
  case "$prompt" in *"follow only"*) ;; *) exit 13;; esac
fi
printf '%%s\n' '{"type":"system","subtype":"init","session_id":"A"}'
printf '%%s\n' '{"type":"result","subtype":"success","session_id":"A","structured_output":{"verdict":"approve","summary":"context retained","findings":[],"missing_tests":[],"previous_findings":[]}}'
`, logPath)
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "sessions.json")
	client := &claude.CLIClient{Binary: fake, Timeout: time.Second, MaxOutputBytes: 1024 * 1024}
	newService := func() *Service {
		return NewService(session.NewJSONStore(storePath), gitservice.NewService(1024*1024), client, "test", "fallback", "max", 3, 1, nil)
	}
	first, err := newService().ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "original goal"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ReviewID == "" || first.ClaudeSessionID != "A" {
		t.Fatalf("first=%+v", first)
	}
	persisted, err := session.NewJSONStore(storePath).Get(context.Background(), first.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.FallbackModel != "fallback" || persisted.Effort != "max" {
		t.Fatalf("model strategy not persisted: %+v", persisted)
	}
	second, err := newService().ContinueReview(context.Background(), ContinueReviewInput{ReviewID: first.ReviewID, Message: "follow only", RefreshDiff: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.ReviewID != first.ReviewID || second.ClaudeSessionID != "A" {
		t.Fatalf("second=%+v", second)
	}
	// A third independent service/store instance proves persistence across another restart.
	if _, err := newService().ContinueReview(context.Background(), ContinueReviewInput{ReviewID: first.ReviewID, Message: "follow only"}); err != nil {
		t.Fatal(err)
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(logBytes)); len(got) != 3 || got[0] != "NONE" || got[1] != "A" || got[2] != "A" {
		t.Fatalf("resume log=%q", logBytes)
	}
}

func TestContinueReviewRejectsRepositoryMismatch(t *testing.T) {
	repo1, repo2 := integrationRepo(t), integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	now := time.Now()
	if err := store.Create(context.Background(), session.ReviewSession{ReviewID: "R", ClaudeSessionID: "A", RepositoryPath: repo1, BaseRef: "HEAD", Model: "test", MaxTurns: 1, Status: session.ReviewStatusOpen, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	s := NewService(store, gitservice.NewService(1024), claude.FailedClient{Err: errors.New("must not run")}, "test", "fallback", "max", 1, 1, nil)
	_, err := s.ContinueReview(context.Background(), ContinueReviewInput{ReviewID: "R", Message: "x", RepositoryPath: repo2})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != "repository_mismatch" {
		t.Fatalf("err=%v", err)
	}
}

func TestSessionLockIsPerReviewAndNonBlocking(t *testing.T) {
	l := sessionLocks{values: map[string]bool{}}
	unlockA, ok := l.try("A")
	if !ok {
		t.Fatal("first lock failed")
	}
	if _, ok := l.try("A"); ok {
		t.Fatal("same review lock should be busy")
	}
	unlockB, ok := l.try("B")
	if !ok {
		t.Fatal("different review should be lockable")
	}
	unlockB()
	unlockA()
	if unlock, ok := l.try("A"); !ok {
		t.Fatal("released lock remains busy")
	} else {
		unlock()
	}
	if len(l.values) != 0 {
		t.Fatalf("released locks were not pruned: %v", l.values)
	}
}

func TestReviewDiffRejectsInvalidEffortBeforeClaude(t *testing.T) {
	repo := integrationRepo(t)
	s := NewService(session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json")), gitservice.NewService(1024*1024), claude.FailedClient{Err: errors.New("must not run")}, "fable", "opus", "max", 1, 1, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test", Effort: "ultra"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != "invalid_request" {
		t.Fatalf("err=%v", err)
	}
}

func TestReviewDiffReturnsActionableClaudeFailureWithCorrelationID(t *testing.T) {
	repo := integrationRepo(t)
	exitCode := 1
	runErr := &claude.RunError{
		Kind: claude.ErrMaxTurns, Stage: claude.StageProcessExit, ExitCode: &exitCode,
		TerminalSubtype: "error_max_turns", TerminalReason: "max_turns",
		TerminalErrors: []string{"Reached maximum number of turns (4)"},
		EventCount:     24, NumTurns: 5, MaxTurns: 4, Model: "sonnet",
		ArgumentNames: []string{"-p", "--output-format", "--max-turns"},
	}
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	s := NewService(store, gitservice.NewService(1024*1024), claude.FailedClient{Err: runErr}, "sonnet", "opus", "high", 4, 1, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test"})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "claude_max_turns" {
		t.Fatalf("err=%v", err)
	}
	if appErr.Details["stage"] != claude.StageProcessExit || appErr.Details["terminal_reason"] != "max_turns" || appErr.Details["correlation_id"] == "" {
		t.Fatalf("details=%v", appErr.Details)
	}
	persisted, err := store.Get(context.Background(), appErr.Details["review_id"].(string))
	if err != nil || persisted.Status != session.ReviewStatusFailed || persisted.LastErrorCode != "claude_max_turns" {
		t.Fatalf("failed review was not persisted: record=%+v err=%v", persisted, err)
	}
}

type staticClient struct {
	result claude.StreamResult
}

func (c staticClient) Run(context.Context, claude.Request) (claude.StreamResult, error) {
	return c.result, nil
}

type failThenClient struct {
	err         error
	firstResult claude.StreamResult
	result      claude.StreamResult
	requests    []claude.Request
}

func (c *failThenClient) Run(_ context.Context, request claude.Request) (claude.StreamResult, error) {
	c.requests = append(c.requests, request)
	if len(c.requests) == 1 {
		return c.firstResult, c.err
	}
	return c.result, nil
}

type recordingClient struct {
	result   claude.StreamResult
	requests []claude.Request
}

func (c *recordingClient) Run(_ context.Context, request claude.Request) (claude.StreamResult, error) {
	c.requests = append(c.requests, request)
	return c.result, nil
}

func TestReviewDiffLabelsResponseSchemaValidationFailure(t *testing.T) {
	repo := integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	client := staticClient{result: claude.StreamResult{SessionID: "A", StructuredOutput: []byte(`{"verdict":"invalid"}`)}}
	s := NewService(store, gitservice.NewService(1024*1024), client, "sonnet", "opus", "high", 4, 1, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test"})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "invalid_claude_output" {
		t.Fatalf("err=%v", err)
	}
	if appErr.Details["stage"] != "response_schema_validation" || appErr.Details["correlation_id"] == "" {
		t.Fatalf("details=%v", appErr.Details)
	}
}

func TestContinueReviewCanRetryAfterMaxTurnsFailure(t *testing.T) {
	repo := integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	now := time.Now().UTC()
	if err := store.Create(context.Background(), session.ReviewSession{ReviewID: "R", ClaudeSessionID: "A", RepositoryPath: repo, BaseRef: "HEAD", Model: "sonnet", FallbackModel: "opus", Effort: "high", MaxTurns: 4, Status: session.ReviewStatusOpen, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	client := &failThenClient{
		err:    &claude.RunError{Kind: claude.ErrMaxTurns, Stage: claude.StageProcessExit, TerminalReason: "max_turns", MaxTurns: 4},
		result: claude.StreamResult{SessionID: "A", StructuredOutput: []byte(`{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}`)},
	}
	s := NewService(store, gitservice.NewService(1024*1024), client, "sonnet", "opus", "high", 4, 1, nil)
	if _, err := s.ContinueReview(context.Background(), ContinueReviewInput{ReviewID: "R", Message: "first"}); err == nil {
		t.Fatal("expected max-turns failure")
	}
	if _, err := s.ContinueReview(context.Background(), ContinueReviewInput{ReviewID: "R", Message: "retry"}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || client.requests[0].SessionID != "A" || client.requests[1].SessionID != "A" {
		t.Fatalf("requests=%+v", client.requests)
	}
}

func TestInterruptedInitialReviewPersistsSessionAndCanResume(t *testing.T) {
	repo := integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	client := &failThenClient{
		err:         &claude.RunError{Kind: claude.ErrTimeout, Stage: claude.StageProcessExit},
		firstResult: claude.StreamResult{SessionID: "A"},
		result:      claude.StreamResult{SessionID: "A", StructuredOutput: []byte(`{"verdict":"approve","summary":"resumed","findings":[],"missing_tests":[]}`)},
	}
	s := NewService(store, gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test", TimeoutSeconds: 1200})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "claude_timeout" || appErr.Details["resumable"] != true || appErr.Details["claude_session_id"] != "A" {
		t.Fatalf("err=%v details=%v", err, appErr.Details)
	}
	reviewID, _ := appErr.Details["review_id"].(string)
	record, err := store.Get(context.Background(), reviewID)
	if err != nil || record.Status != session.ReviewStatusInterrupted || record.ClaudeSessionID != "A" || record.TimeoutSeconds != 1200 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	out, err := s.ContinueReview(context.Background(), ContinueReviewInput{ReviewID: reviewID, Message: "finish", TimeoutSeconds: 30})
	if err != nil || out.ClaudeSessionID != "A" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(client.requests) != 2 || client.requests[1].SessionID != "A" || client.requests[0].Timeout != 1200*time.Second || client.requests[1].Timeout != 30*time.Second {
		t.Fatalf("requests=%+v", client.requests)
	}
	record, err = store.Get(context.Background(), reviewID)
	if err != nil || record.Status != session.ReviewStatusOpen || record.LastErrorCode != "" || record.LastErrorAt != nil || record.TimeoutSeconds != 1200 {
		t.Fatalf("resumed record=%+v err=%v", record, err)
	}
}

func TestReviewTimeoutValidationAndDefaults(t *testing.T) {
	repo := integrationRepo(t)
	client := &recordingClient{result: claude.StreamResult{SessionID: "A", StructuredOutput: []byte(`{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}`)}}
	s := NewService(session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json")), gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	if _, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test", TimeoutSeconds: 1201}); err == nil {
		t.Fatal("expected timeout validation error")
	}
	if _, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test"}); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].Timeout != 240*time.Second {
		t.Fatalf("requests=%+v", client.requests)
	}
}

func TestPendingReviewWithoutClaudeSessionCannotResume(t *testing.T) {
	repo := integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	now := time.Now().UTC()
	if err := store.Create(context.Background(), session.ReviewSession{ReviewID: "R", RepositoryPath: repo, BaseRef: "HEAD", Status: session.ReviewStatusPending, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	s := NewService(store, gitservice.NewService(1024*1024), claude.FailedClient{Err: errors.New("must not run")}, "fable", "opus", "high", 20, 240, nil)
	_, err := s.ContinueReview(context.Background(), ContinueReviewInput{ReviewID: "R", Message: "resume"})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "review_not_resumable" {
		t.Fatalf("err=%v", err)
	}
}

func TestReviewDiffRejectsEmptyPathScopeBeforeClaude(t *testing.T) {
	repo := integrationRepo(t)
	client := &recordingClient{result: claude.StreamResult{SessionID: "must-not-run"}}
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	s := NewService(store, gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test", IncludePaths: []string{"missing.go"}})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "empty_review_scope" {
		t.Fatalf("err=%v", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("Claude was called: %+v", client.requests)
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%+v err=%v", records, err)
	}
}

func TestReviewDiffExplainsSensitiveOnlyScope(t *testing.T) {
	repo := integrationRepo(t)
	if err := os.Remove(filepath.Join(repo, "main.go")); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "restore", "main.go").Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &recordingClient{result: claude.StreamResult{SessionID: "must-not-run"}}
	s := NewService(session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json")), gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test", IncludePaths: []string{".env"}})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "empty_review_scope" {
		t.Fatalf("err=%v", err)
	}
	excluded, ok := appErr.Details["sensitive_excluded_files"].([]string)
	if !ok || len(excluded) != 1 || excluded[0] != ".env" || !strings.Contains(appErr.Message, "sensitive-content") {
		t.Fatalf("message=%q details=%v", appErr.Message, appErr.Details)
	}
	if len(client.requests) != 0 {
		t.Fatalf("Claude was called: %+v", client.requests)
	}
}

func TestCanceledReviewPersistsResumableSession(t *testing.T) {
	repo := integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	client := &failThenClient{err: &claude.RunError{Kind: claude.ErrCanceled, Stage: claude.StageProcessExit}, firstResult: claude.StreamResult{SessionID: "A"}}
	s := NewService(store, gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test"})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "claude_canceled" || appErr.Details["resumable"] != true {
		t.Fatalf("err=%v", err)
	}
	record, err := store.Get(context.Background(), appErr.Details["review_id"].(string))
	if err != nil || record.Status != session.ReviewStatusInterrupted || record.ClaudeSessionID != "A" || record.LastErrorCode != "claude_canceled" {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

type blockingClient struct {
	started chan struct{}
	release chan struct{}
}

type failingUpdateStore struct {
	session.SessionStore
}

func (f failingUpdateStore) Update(context.Context, session.ReviewSession) error {
	return errors.New("disk unavailable")
}

func (c *blockingClient) Run(context.Context, claude.Request) (claude.StreamResult, error) {
	close(c.started)
	<-c.release
	return claude.StreamResult{SessionID: "A", StructuredOutput: []byte(`{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}`)}, nil
}

func TestPendingReviewCannotBeClosedWhileClaudeIsRunning(t *testing.T) {
	repo := integrationRepo(t)
	store := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	client := &blockingClient{started: make(chan struct{}), release: make(chan struct{})}
	s := NewService(store, gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	done := make(chan error, 1)
	go func() {
		_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test"})
		done <- err
	}()
	<-client.started
	records, err := store.List(context.Background())
	if err != nil || len(records) != 1 || records[0].Status != session.ReviewStatusPending {
		t.Fatalf("records=%+v err=%v", records, err)
	}
	_, err = s.CloseReview(context.Background(), CloseReviewInput{ReviewID: records[0].ReviewID, DeleteClaudeSession: true})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "review_busy" {
		t.Fatalf("err=%v", err)
	}
	close(client.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCompletedInitialReviewReportsRecoveryIdentifiersOnStorageFailure(t *testing.T) {
	repo := integrationRepo(t)
	baseStore := session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	store := failingUpdateStore{SessionStore: baseStore}
	client := staticClient{result: claude.StreamResult{SessionID: "A", StructuredOutput: []byte(`{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}`)}}
	s := NewService(store, gitservice.NewService(1024*1024), client, "fable", "opus", "high", 20, 240, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test"})
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "storage_error" {
		t.Fatalf("err=%v", err)
	}
	if appErr.Details["review_id"] == "" || appErr.Details["claude_session_id"] != "A" || appErr.Details["resumable"] != false || appErr.Details["stage"] != "session_persistence" {
		t.Fatalf("details=%v", appErr.Details)
	}
}

func integrationRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "main.go"}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}
