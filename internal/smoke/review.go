package smoke

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jbd/claude-reviewer/internal/claude"
	"github.com/jbd/claude-reviewer/internal/config"
	gitservice "github.com/jbd/claude-reviewer/internal/git"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	"github.com/jbd/claude-reviewer/internal/session"
)

// RunReview exercises the same Claude invocation and review pipeline as the MCP
// tool against an isolated repository containing a one-line diff.
func RunReview(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	dir, err := os.MkdirTemp("", "claude-reviewer-smoke-*")
	if err != nil {
		return fmt.Errorf("create smoke repository: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := runGit(ctx, dir, "init", "-q"); err != nil {
		return err
	}
	path := filepath.Join(dir, "value.go")
	if err := os.WriteFile(path, []byte("package smoke\n\nfunc Value() int { return 1 }\n"), 0o600); err != nil {
		return fmt.Errorf("write smoke baseline: %w", err)
	}
	if err := runGit(ctx, dir, "add", "value.go"); err != nil {
		return err
	}
	if err := runGit(ctx, dir, "-c", "user.name=Claude Reviewer Smoke Test", "-c", "user.email=smoke@example.invalid", "commit", "-q", "-m", "baseline"); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte("package smoke\n\nfunc Value() int { return 2 }\n"), 0o600); err != nil {
		return fmt.Errorf("write smoke change: %w", err)
	}

	binary, err := cfg.ResolveClaudeBinary()
	if err != nil {
		return fmt.Errorf("resolve Claude binary: %w", err)
	}
	client := &claude.CLIClient{
		Binary: binary, Timeout: cfg.Timeout(), MaxOutputBytes: cfg.MaxOutputBytes,
		Logger: logger, CheckAuthentication: true,
	}
	store := session.NewJSONStore(filepath.Join(dir, "data", "sessions.json"))
	service := reviewer.NewService(store, gitservice.NewService(cfg.MaxDiffBytes), client, cfg.DefaultModel, cfg.DefaultFallbackModel, cfg.DefaultEffort, cfg.DefaultMaxTurns, cfg.TimeoutSeconds, logger)
	service.AsyncTimeoutSeconds = cfg.AsyncTimeoutSeconds
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	service.WorkerContext = workerCtx
	defer func() {
		service.BeginShutdown()
		cancelWorkers()
		service.WaitForWorkers()
	}()
	started, err := service.StartReview(ctx, reviewer.ReviewDiffInput{
		RepositoryPath:    dir,
		Goal:              "Verify the complete production review invocation against this isolated one-line Go diff.",
		ReviewFocus:       []string{"correctness"},
		AdditionalContext: "This is a diagnostic smoke test. The repository contains one small file and no external context is required.",
		TestResults:       "Diagnostic fixture only; no tests are required.",
	})
	if err != nil {
		return fmt.Errorf("start production review smoke test: %w", err)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, err := service.GetReviewStatus(ctx, started.ReviewID)
		if err != nil {
			return fmt.Errorf("poll production review smoke test: %w", err)
		}
		if status.Status != session.ReviewStatusPending {
			if status.Status != session.ReviewStatusOpen || status.Response == nil {
				return fmt.Errorf("production review smoke test ended with status %s and error %s", status.Status, status.LastErrorCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, output)
	}
	return nil
}
