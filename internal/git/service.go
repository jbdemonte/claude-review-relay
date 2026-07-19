package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrInvalidRepository = errors.New("invalid git repository")
	ErrInvalidBaseRef    = errors.New("invalid git base reference")
	ErrDiffTooLarge      = errors.New("git diff exceeds configured limit")
)

type GitService interface {
	ValidateRepository(context.Context, string) error
	Root(context.Context, string) (string, error)
	Diff(context.Context, string, string) (string, error)
	UntrackedFiles(context.Context, string) ([]string, error)
	CurrentBranch(context.Context, string) (string, error)
	HeadSHA(context.Context, string) (string, error)
}

type Service struct{ MaxDiffBytes int64 }

func NewService(maxDiffBytes int64) *Service { return &Service{MaxDiffBytes: maxDiffBytes} }

func (s *Service) ValidateRepository(ctx context.Context, path string) error {
	_, err := s.Root(ctx, path)
	return err
}

func (s *Service) Root(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRepository, err)
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("%w: path must be an existing directory", ErrInvalidRepository)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(abs); evalErr == nil {
		abs = evaluated
	}
	out, err := run(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRepository, err)
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", fmt.Errorf("%w: empty repository root", ErrInvalidRepository)
	}
	if evaluated, evalErr := filepath.EvalSymlinks(root); evalErr == nil {
		root = evaluated
	}
	return filepath.Clean(root), nil
}

func (s *Service) validateBase(ctx context.Context, path, base string) error {
	if base == "" || strings.HasPrefix(base, "-") || strings.ContainsAny(base, "\x00\r\n") {
		return ErrInvalidBaseRef
	}
	_, err := run(ctx, path, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBaseRef, err)
	}
	return nil
}

func (s *Service) Diff(ctx context.Context, path, base string) (string, error) {
	if err := s.validateBase(ctx, path, base); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--unified=80", base, "--")
	cmd.Dir = path
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	limit := s.MaxDiffBytes
	if limit <= 0 {
		limit = 2 * 1024 * 1024
	}
	var stdout limitedBuffer
	stdout.limit = limit
	cmd.Stdout = &stdout
	err := cmd.Run()
	if stdout.exceeded {
		return "", fmt.Errorf("%w: maximum is %d bytes", ErrDiffTooLarge, limit)
	}
	if err != nil {
		return "", fmt.Errorf("git diff: %w: %s", err, cleanStderr(stderr.String()))
	}
	return stdout.String(), nil
}

func (s *Service) UntrackedFiles(ctx context.Context, path string) ([]string, error) {
	out, err := run(ctx, path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "?? ") {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files, nil
}

func (s *Service) CurrentBranch(ctx context.Context, path string) (string, error) {
	out, err := run(ctx, path, "branch", "--show-current")
	return strings.TrimSpace(out), err
}

func (s *Service) HeadSHA(ctx context.Context, path string) (string, error) {
	out, err := run(ctx, path, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, cleanStderr(stderr.String()))
	}
	return stdout.String(), nil
}

func cleanStderr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1024 {
		s = s[:1024] + "…"
	}
	return s
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *limitedBuffer) Len() int       { return b.buffer.Len() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.exceeded {
		return len(p), nil
	}
	remaining := b.limit - int64(b.Len())
	if int64(len(p)) > remaining {
		if remaining > 0 {
			_, _ = b.buffer.Write(p[:remaining])
		}
		b.exceeded = true
		return len(p), nil
	}
	return b.buffer.Write(p)
}
