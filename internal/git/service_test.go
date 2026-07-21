package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	commands := [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}}
	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "a.txt"}, {"commit", "-m", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func TestServiceDiffAndUntracked(t *testing.T) {
	repo := makeRepo(t)
	s := NewService(1024 * 1024)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := s.Diff(ctx, repo, "HEAD", PathScope{})
	if err != nil {
		t.Fatal(err)
	}
	if diff == "" {
		t.Fatal("empty diff")
	}
	files, err := s.UntrackedFiles(ctx, repo, PathScope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "new.txt" {
		t.Fatalf("files=%v", files)
	}
}

func TestServiceInvalidBaseRefAndLimit(t *testing.T) {
	repo := makeRepo(t)
	ctx := context.Background()
	if _, err := NewService(1024).Diff(ctx, repo, "--output=/tmp/oops", PathScope{}); !errors.Is(err, ErrInvalidBaseRef) {
		t.Fatalf("err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(strings.Repeat("a long changed line that remains textual\n", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(100).Diff(ctx, repo, "HEAD", PathScope{}); !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceRejectsNonRepository(t *testing.T) {
	_, err := NewService(100).Root(context.Background(), t.TempDir())
	if !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceAppliesLiteralPathScopeToDiffAndUntrackedFiles(t *testing.T) {
	repo := makeRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "b.txt"}, {"commit", "-m", "add b"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "new.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scope := PathScope{Include: []string{"a.txt", "b.txt", "new.txt", "other.txt"}, Exclude: []string{"b.txt", "other.txt"}}
	diff, err := NewService(1024*1024).Diff(ctx, repo, "HEAD", scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "a.txt") || strings.Contains(diff, "b.txt") {
		t.Fatalf("scoped diff=%s", diff)
	}
	files, err := NewService(1024*1024).UntrackedFiles(ctx, repo, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "new.txt" {
		t.Fatalf("scoped untracked files=%v", files)
	}
}

func TestServiceRejectsUnsafePathScope(t *testing.T) {
	repo := makeRepo(t)
	for _, value := range []string{"", "../outside", "/absolute", "line\nbreak"} {
		if _, err := NewService(1024).Diff(context.Background(), repo, "HEAD", PathScope{Include: []string{value}}); !errors.Is(err, ErrInvalidPathScope) {
			t.Fatalf("path=%q err=%v", value, err)
		}
	}
}

func TestServiceSupportsExcludeOnlyAndDirectoryScopes(t *testing.T) {
	repo := makeRepo(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(repo, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "dir", "nested.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "dir/nested.txt", "b.txt"}, {"commit", "-m", "add scoped files"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	for _, name := range []string{"a.txt", "b.txt", "dir/nested.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "dir", "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(1024 * 1024)
	diff, err := service.Diff(ctx, repo, "HEAD", PathScope{Exclude: []string{"b.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "a.txt") || !strings.Contains(diff, "dir/nested.txt") || strings.Contains(diff, "b.txt") {
		t.Fatalf("exclude-only diff=%s", diff)
	}
	diff, err = service.Diff(ctx, repo, "HEAD", PathScope{Include: []string{"dir"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "dir/nested.txt") || strings.Contains(diff, "a.txt") {
		t.Fatalf("directory diff=%s", diff)
	}
	files, err := service.UntrackedFiles(ctx, repo, PathScope{Include: []string{"dir/"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "dir/new.txt" {
		t.Fatalf("directory untracked=%v", files)
	}
}
