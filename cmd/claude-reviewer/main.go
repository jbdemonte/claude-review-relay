package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jbd/claude-reviewer/internal/apperr"
	"github.com/jbd/claude-reviewer/internal/claude"
	"github.com/jbd/claude-reviewer/internal/config"
	gitservice "github.com/jbd/claude-reviewer/internal/git"
	mcpserver "github.com/jbd/claude-reviewer/internal/mcp"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	"github.com/jbd/claude-reviewer/internal/session"
	"github.com/jbd/claude-reviewer/internal/smoke"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if command == "doctor" {
		smokeRequested, err := parseDoctorOptions(os.Args[2:])
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		report := config.Doctor(ctx, cfg)
		if smokeRequested {
			err := smoke.RunReview(ctx, cfg, logger)
			check := config.Check{Name: "production_review_smoke_test", OK: err == nil, Detail: fmt.Sprintf("model=%s fallback_model=%s effort=%s max_turns=%d", cfg.DefaultModel, cfg.DefaultFallbackModel, cfg.DefaultEffort, cfg.DefaultMaxTurns)}
			if err != nil {
				report.OK = false
				check.Detail = err.Error()
				var appErr *apperr.Error
				if errors.As(err, &appErr) {
					check.Details = appErr.Details
				}
			}
			report.Checks = append(report.Checks, check)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		if !report.OK {
			return fmt.Errorf("doctor found one or more problems")
		}
		return nil
	}
	if command != "serve" {
		return fmt.Errorf("unknown command %q (expected serve or doctor)", command)
	}
	var client claude.Client
	binary, resolveErr := cfg.ResolveClaudeBinary()
	if resolveErr != nil {
		client = claude.FailedClient{Err: claude.ErrNotFound}
	} else {
		client = &claude.CLIClient{Binary: binary, Timeout: cfg.Timeout(), MaxOutputBytes: cfg.MaxOutputBytes, Logger: logger, CheckAuthentication: true}
	}
	store := session.NewJSONStore(cfg.SessionsPath())
	service := reviewer.NewService(store, gitservice.NewService(cfg.MaxDiffBytes), client, cfg.DefaultModel, cfg.DefaultFallbackModel, cfg.DefaultEffort, cfg.DefaultMaxTurns, cfg.TimeoutSeconds, logger)
	server := mcpserver.New(service)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx)
}

func parseDoctorOptions(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "--review-smoke-test" {
		return true, nil
	}
	return false, fmt.Errorf("unknown doctor option (expected --review-smoke-test)")
}
