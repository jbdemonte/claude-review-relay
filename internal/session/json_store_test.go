package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONStorePersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "sessions.json")
	now := time.Now().UTC().Round(time.Second)
	want := ReviewSession{ReviewID: "R", ClaudeSessionID: "A", RepositoryPath: "/repo", Status: ReviewStatusOpen, CreatedAt: now, UpdatedAt: now}
	if err := NewJSONStore(path).Create(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := NewJSONStore(path).Get(context.Background(), "R")
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaudeSessionID != "A" || !got.CreatedAt.Equal(now) {
		t.Fatalf("got %#v", got)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v err=%v", st.Mode().Perm(), err)
	}
}

func TestJSONStoreUpdateDeleteAndListOrder(t *testing.T) {
	s := NewJSONStore(filepath.Join(t.TempDir(), "sessions.json"))
	ctx := context.Background()
	now := time.Now()
	for _, v := range []ReviewSession{{ReviewID: "old", UpdatedAt: now}, {ReviewID: "new", UpdatedAt: now.Add(time.Minute)}} {
		if err := s.Create(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].ReviewID != "new" {
		t.Fatalf("order: %#v", list)
	}
	v := list[0]
	v.Status = ReviewStatusClosed
	if err := s.Update(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestJSONStoreReadsLegacyObjectShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	legacy := `{"version":1,"sessions":{"R":{"claude_session_id":"A","repository_path":"/repo","status":"open","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewJSONStore(path).Get(context.Background(), "R")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewID != "R" || got.ClaudeSessionID != "A" {
		t.Fatalf("got=%+v", got)
	}
}
