package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReviewLeaseExcludesOtherStoreInstances(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "leases")
	lease, err := AcquireReviewLease(dir, "R")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireReviewLease(dir, "R"); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("err=%v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	reopened, err := AcquireReviewLease(dir, "R")
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveReviewLeaseFile(dir, "R"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(reviewLeasePath(dir, "R")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease file still exists: %v", err)
	}
	if err := reopened.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestReviewLeaseExcludesSubprocess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "leases")
	lease, err := AcquireReviewLease(dir, "R")
	if err != nil {
		t.Fatal(err)
	}
	runLeaseHelper(t, dir, "busy")
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	runLeaseHelper(t, dir, "available")
}

func TestReviewLeaseSubprocessHelper(t *testing.T) {
	if os.Getenv("CLAUDE_REVIEWER_LEASE_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	dir := os.Getenv("CLAUDE_REVIEWER_LEASE_DIR")
	want := os.Getenv("CLAUDE_REVIEWER_LEASE_EXPECT")
	lease, err := AcquireReviewLease(dir, "R")
	if want == "busy" {
		if !errors.Is(err, ErrLeaseBusy) {
			t.Fatalf("err=%v, want ErrLeaseBusy", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

func runLeaseHelper(t *testing.T, dir, expect string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewLeaseSubprocessHelper$")
	cmd.Env = append(os.Environ(),
		"CLAUDE_REVIEWER_LEASE_HELPER=1",
		"CLAUDE_REVIEWER_LEASE_DIR="+dir,
		"CLAUDE_REVIEWER_LEASE_EXPECT="+expect,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("lease helper failed: %v: %s", err, output)
	}
}
