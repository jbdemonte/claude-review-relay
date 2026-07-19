package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jbd/claude-reviewer/internal/claude"
	"github.com/jbd/claude-reviewer/internal/config"
	gitservice "github.com/jbd/claude-reviewer/internal/git"
	mcpserver "github.com/jbd/claude-reviewer/internal/mcp"
	"github.com/jbd/claude-reviewer/internal/reviewer"
	"github.com/jbd/claude-reviewer/internal/session"
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
	if command == "doctor" {
		report := config.Doctor(context.Background(), cfg)
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
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	var client claude.Client
	binary, resolveErr := cfg.ResolveClaudeBinary()
	if resolveErr != nil {
		client = claude.FailedClient{Err: claude.ErrNotFound}
	} else {
		client = &claude.CLIClient{Binary: binary, Timeout: cfg.Timeout(), MaxOutputBytes: cfg.MaxOutputBytes, Logger: logger, CheckAuthentication: true}
	}
	store := session.NewJSONStore(cfg.SessionsPath())
	service := reviewer.NewService(store, gitservice.NewService(cfg.MaxDiffBytes), client, cfg.DefaultModel, cfg.DefaultMaxTurns, logger)
	server := mcpserver.New(service)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Run(ctx)
}
