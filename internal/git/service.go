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
	ErrInvalidPathScope  = errors.New("invalid Git path scope")
	ErrDiffTooLarge      = errors.New("git diff exceeds configured limit")
)

type PathScope struct {
	Include []string
	Exclude []string
}

type GitService interface {
	ValidateRepository(context.Context, string) error
	Root(context.Context, string) (string, error)
	Diff(context.Context, string, string, PathScope) (string, error)
	UntrackedFiles(context.Context, string, PathScope) ([]string, error)
	CurrentBranch(context.Context, string) (string, error)
	HeadSHA(context.Context, string) (string, error)
	ResolveCommit(context.Context, string, string) (string, error)
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
	_, err := s.ResolveCommit(ctx, path, base)
	return err
}

func (s *Service) ResolveCommit(ctx context.Context, path, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n") {
		return "", ErrInvalidBaseRef
	}
	out, err := run(ctx, path, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidBaseRef, err)
	}
	return strings.TrimSpace(out), nil
}

func (s *Service) Diff(ctx context.Context, path, base string, scope PathScope) (string, error) {
	if err := s.validateBase(ctx, path, base); err != nil {
		return "", err
	}
	pathspecs, err := scopedPathspecs(scope)
	if err != nil {
		return "", err
	}
	args := append([]string{"diff", "--no-ext-diff", "--unified=80", base, "--"}, pathspecs...)
	cmd := exec.CommandContext(ctx, "git", args...)
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
	err = cmd.Run()
	if stdout.exceeded {
		return "", fmt.Errorf("%w: maximum is %d bytes", ErrDiffTooLarge, limit)
	}
	if err != nil {
		return "", fmt.Errorf("git diff: %w: %s", err, cleanStderr(stderr.String()))
	}
	return stdout.String(), nil
}

func (s *Service) UntrackedFiles(ctx context.Context, path string, scope PathScope) ([]string, error) {
	pathspecs, err := scopedPathspecs(scope)
	if err != nil {
		return nil, err
	}
	args := append([]string{"ls-files", "--others", "--exclude-standard", "--"}, pathspecs...)
	out, err := run(ctx, path, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func scopedPathspecs(scope PathScope) ([]string, error) {
	pathspecs := make([]string, 0, len(scope.Include)+len(scope.Exclude))
	for _, value := range scope.Include {
		if err := validateScopedPath(value); err != nil {
			return nil, err
		}
		pathspecs = append(pathspecs, ":(top,literal)"+value)
	}
	for _, value := range scope.Exclude {
		if err := validateScopedPath(value); err != nil {
			return nil, err
		}
		pathspecs = append(pathspecs, ":(top,exclude,literal)"+value)
	}
	return pathspecs, nil
}

func validateScopedPath(value string) error {
	if value == "" || filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: paths must be non-empty, repository-relative, and single-line", ErrInvalidPathScope)
	}
	for _, part := range strings.Split(filepath.ToSlash(value), "/") {
		if part == ".." {
			return fmt.Errorf("%w: parent traversal is not allowed", ErrInvalidPathScope)
		}
	}
	return nil
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
