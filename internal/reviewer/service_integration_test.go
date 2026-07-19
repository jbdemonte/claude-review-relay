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
		return NewService(session.NewJSONStore(storePath), gitservice.NewService(1024*1024), client, "test", "fallback", "max", 3, nil)
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
	s := NewService(store, gitservice.NewService(1024), claude.FailedClient{Err: errors.New("must not run")}, "test", "fallback", "max", 1, nil)
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
	s := NewService(session.NewJSONStore(filepath.Join(t.TempDir(), "sessions.json")), gitservice.NewService(1024*1024), claude.FailedClient{Err: errors.New("must not run")}, "fable", "opus", "max", 1, nil)
	_, err := s.ReviewDiff(context.Background(), ReviewDiffInput{RepositoryPath: repo, Goal: "test", Effort: "ultra"})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != "invalid_request" {
		t.Fatalf("err=%v", err)
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
