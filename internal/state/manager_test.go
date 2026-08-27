package state

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rcwsr/treesync/internal/git"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func setupWorktree(t *testing.T) *git.Worktree {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "init")

	w, err := git.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestDetachAndRestoreRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setupWorktree(t)
	commonDir, err := w.CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(commonDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if branch, err := w.CurrentBranch(ctx); err != nil || branch != "main" {
		t.Fatalf("branch before detach = %q, err = %v", branch, err)
	}

	if _, err := mgr.Detach(ctx, w, ""); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if branch, err := w.CurrentBranch(ctx); err != nil || branch != "" {
		t.Fatalf("expected detached HEAD after Detach, branch = %q, err = %v", branch, err)
	}
	if _, err := os.Stat(filepath.Join(mgr.Dir, "state.json")); err != nil {
		t.Fatalf("expected state.json to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mgr.Dir, "watch.pid")); err != nil {
		t.Fatalf("expected watch.pid to exist: %v", err)
	}

	if err := mgr.Restore(ctx, w, logger); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if branch, err := w.CurrentBranch(ctx); err != nil || branch != "main" {
		t.Fatalf("branch after restore = %q, err = %v", branch, err)
	}
	if _, err := os.Stat(filepath.Join(mgr.Dir, "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected state.json removed after restore, stat err = %v", err)
	}
}

func TestRecoverIfStaleNoOpWhenNothingToRecover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setupWorktree(t)
	commonDir, err := w.CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(commonDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := mgr.RecoverIfStale(ctx, w, logger); err != nil {
		t.Fatalf("RecoverIfStale with no prior state should be a no-op, got: %v", err)
	}
}

func TestRecoverIfStaleRestoresAfterCrash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setupWorktree(t)
	commonDir, err := w.CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(commonDir)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if _, err := mgr.Detach(ctx, w, ""); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	// Simulate a crash: overwrite the pidfile with a PID that can't be alive, without
	// running the normal Restore/cleanup path.
	if err := os.WriteFile(filepath.Join(mgr.Dir, "watch.pid"), []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RecoverIfStale(ctx, w, logger); err != nil {
		t.Fatalf("RecoverIfStale: %v", err)
	}
	if branch, err := w.CurrentBranch(ctx); err != nil || branch != "main" {
		t.Fatalf("branch after crash recovery = %q, err = %v", branch, err)
	}
}

func TestLockPreventsConcurrentHolders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := setupWorktree(t)
	commonDir, err := w.CommonDir(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(commonDir)

	unlock, err := mgr.Lock(LockInfo{SourceDir: "/agent/worktree", SourceBranch: "agent-branch"})
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}

	_, err = mgr.Lock(LockInfo{SourceDir: "/other/worktree"})
	if err == nil {
		t.Fatal("expected second Lock to fail while the first is held")
	}
	if !strings.Contains(err.Error(), "/agent/worktree") || !strings.Contains(err.Error(), "agent-branch") {
		t.Fatalf("expected error to name the holding worktree/branch, got: %v", err)
	}

	unlock()

	unlock2, err := mgr.Lock(LockInfo{SourceDir: "/other/worktree"})
	if err != nil {
		t.Fatalf("Lock after release should succeed, got: %v", err)
	}
	unlock2()
}
