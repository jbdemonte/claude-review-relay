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
	diff, err := s.Diff(ctx, repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if diff == "" {
		t.Fatal("empty diff")
	}
	files, err := s.UntrackedFiles(ctx, repo)
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
	if _, err := NewService(1024).Diff(ctx, repo, "--output=/tmp/oops"); !errors.Is(err, ErrInvalidBaseRef) {
		t.Fatalf("err=%v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(strings.Repeat("a long changed line that remains textual\n", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(100).Diff(ctx, repo, "HEAD"); !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestServiceRejectsNonRepository(t *testing.T) {
	_, err := NewService(100).Root(context.Background(), t.TempDir())
	if !errors.Is(err, ErrInvalidRepository) {
		t.Fatalf("err=%v", err)
	}
}
