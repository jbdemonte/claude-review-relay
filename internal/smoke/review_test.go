package smoke

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jbd/claude-reviewer/internal/config"
)

func TestRunReviewUsesCompletePipelineWithFakeClaude(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	script := `#!/bin/sh
if [ "$1" = "auth" ]; then
  printf '%s\n' '{"loggedIn":true}'
  exit 0
fi
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"SMOKE"}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"SMOKE","structured_output":{"verdict":"approve","summary":"ok","findings":[],"missing_tests":[]}}'
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		ClaudeBinary: fake, DefaultModel: "fable", DefaultFallbackModel: "opus",
		DefaultEffort: "max", DefaultMaxTurns: 12, TimeoutSeconds: 5,
		MaxDiffBytes: 1024 * 1024, MaxOutputBytes: 1024 * 1024, DataDir: dir,
	}
	if err := RunReview(context.Background(), cfg, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledClaudeReview(t *testing.T) {
	if os.Getenv("CLAUDE_REVIEWER_INTEGRATION") != "1" {
		t.Skip("set CLAUDE_REVIEWER_INTEGRATION=1 to test the installed Claude Code production invocation")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutSeconds+30)*time.Second)
	defer cancel()
	if err := RunReview(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil))); err != nil {
		t.Fatal(err)
	}
}
