// Package integration exercises `treesync watch` end to end against real temporary git
// worktrees: initial sync, incremental create/modify/delete propagation, detached-HEAD
// safety, graceful stop+restore, and crash recovery.
package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "treesync-bin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "treesync")
	build := exec.Command("go", "build", "-o", binPath, "github.com/rcwsr/treesync/cmd/treesync")
	if out, err := build.CombinedOutput(); err != nil {
		panic("building treesync: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// safeBuffer is a concurrency-safe io.Writer, since os/exec's stdout/stderr copying
// goroutines and the test's own polling both touch the buffer.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// setupRepo creates a fresh repo on branch "main" with one commit, plus a second
// worktree "source" branched off it, and returns (repoRoot, sourceDir).
func setupRepo(t *testing.T) (repoRoot, sourceDir string) {
	t.Helper()
	repoRoot = t.TempDir()
	runGit(t, repoRoot, "init", "-q", "-b", "main")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoRoot, "add", ".")
	runGit(t, repoRoot, "commit", "-q", "-m", "init")

	sourceDir = filepath.Join(t.TempDir(), "source")
	runGit(t, repoRoot, "worktree", "add", "-q", "-b", "agent-branch", sourceDir)
	return repoRoot, sourceDir
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func fileContains(path, want string) bool {
	data, err := os.ReadFile(path)
	return err == nil && string(data) == want
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

func isDetached(dir string) bool {
	cmd := exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD")
	cmd.Dir = dir
	return cmd.Run() != nil // non-zero exit means detached
}

func TestWatchSyncAndRestore(t *testing.T) {
	t.Parallel()
	repoRoot, sourceDir := setupRepo(t)
	manifestPath := filepath.Join(repoRoot, ".git", "treesync", "manifest.json")
	statePath := filepath.Join(repoRoot, ".git", "treesync", "state.json")
	pidPath := filepath.Join(repoRoot, ".git", "treesync", "watch.pid")

	var out safeBuffer
	cmd := exec.Command(binPath, "watch", sourceDir, repoRoot, "--debounce-ms=50")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting watch: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	waitFor(t, 5*time.Second, "initial sync (manifest.json)", func() bool {
		_, err := os.Stat(manifestPath)
		return err == nil
	})
	waitFor(t, 5*time.Second, "target detached", func() bool { return isDetached(repoRoot) })

	// create
	newFile := filepath.Join(sourceDir, "agent-created.txt")
	if err := os.WriteFile(newFile, []byte("agent output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "created file synced to target", func() bool {
		return fileContains(filepath.Join(repoRoot, "agent-created.txt"), "agent output\n")
	})

	// modify a tracked file
	if err := os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte("hello\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "modified file synced to target", func() bool {
		return fileContains(filepath.Join(repoRoot, "README.md"), "hello\nmore\n")
	})

	// delete
	if err := os.Remove(newFile); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "deleted file removed from target", func() bool {
		return fileMissing(filepath.Join(repoRoot, "agent-created.txt"))
	})

	// stop gracefully (Ctrl+C) and verify restore
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch process exited with error: %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watch process did not exit after SIGINT\noutput:\n%s", out.String())
	}

	branch := runGit(t, repoRoot, "symbolic-ref", "--short", "HEAD")
	if got := trimNL(branch); got != "main" {
		t.Fatalf("target branch after restore = %q, want %q", got, "main")
	}
	if status := runGit(t, repoRoot, "status", "--porcelain"); status != "" {
		t.Fatalf("target not clean after restore:\n%s", status)
	}
	if !fileMissing(statePath) || !fileMissing(pidPath) {
		t.Fatalf("expected state/pid files removed after restore")
	}
}

func TestCrashRecovery(t *testing.T) {
	t.Parallel()
	repoRoot, sourceDir := setupRepo(t)

	cmd1 := exec.Command(binPath, "watch", sourceDir, repoRoot, "--debounce-ms=50")
	if err := cmd1.Start(); err != nil {
		t.Fatalf("starting first watch: %v", err)
	}
	t.Cleanup(func() {
		if cmd1.Process != nil {
			_ = cmd1.Process.Kill()
			_ = cmd1.Wait()
		}
	})

	waitFor(t, 5*time.Second, "target detached", func() bool { return isDetached(repoRoot) })

	// simulate a crash: no signal, no cleanup
	if err := cmd1.Process.Kill(); err != nil {
		t.Fatalf("killing first watch: %v", err)
	}
	_ = cmd1.Wait()

	if !isDetached(repoRoot) {
		t.Fatalf("expected target to remain detached after crash")
	}

	var out2 safeBuffer
	cmd2 := exec.Command(binPath, "watch", sourceDir, repoRoot, "--debounce-ms=50", "--log-level=debug")
	cmd2.Stdout = &out2
	cmd2.Stderr = &out2
	if err := cmd2.Start(); err != nil {
		t.Fatalf("starting recovery watch: %v", err)
	}
	t.Cleanup(func() {
		if cmd2.Process != nil {
			_ = cmd2.Process.Kill()
			_ = cmd2.Wait()
		}
	})

	waitFor(t, 5*time.Second, "recovery log message", func() bool {
		return bytes.Contains([]byte(out2.String()), []byte("recovering target from a previous treesync session"))
	})
	waitFor(t, 5*time.Second, "target re-detached by recovery watch", func() bool { return isDetached(repoRoot) })

	if err := cmd2.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd2.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("recovery watch exited with error: %v\noutput:\n%s", err, out2.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("recovery watch did not exit after SIGINT\noutput:\n%s", out2.String())
	}

	branch := runGit(t, repoRoot, "symbolic-ref", "--short", "HEAD")
	if got := trimNL(branch); got != "main" {
		t.Fatalf("target branch after recovery+restore = %q, want %q", got, "main")
	}
}

func TestSyncRefusesWhileWatching(t *testing.T) {
	t.Parallel()
	repoRoot, sourceDir := setupRepo(t)

	cmd := exec.Command(binPath, "watch", sourceDir, repoRoot, "--debounce-ms=50")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting watch: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitFor(t, 5*time.Second, "target detached", func() bool { return isDetached(repoRoot) })

	syncCmd := exec.Command(binPath, "sync", sourceDir, repoRoot, "--force")
	out, err := syncCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected `sync` to refuse a target already being watched, output:\n%s", out)
	}
	if !bytes.Contains(out, []byte(sourceDir)) || !bytes.Contains(out, []byte("agent-branch")) {
		t.Fatalf("expected error to name the watching worktree/branch, got:\n%s", out)
	}
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// TestWatchForceResyncsAfterAutoStash is the primary regression guard: a one-shot sync
// followed by `watch --force` must actually re-sync the target's content. Before the
// fix, the auto-stash + detach wiped the synced content while the persisted manifest
// still matched the unchanged source, so the initial sync was an empty no-op and the
// target was left on pristine HEAD content.
func TestWatchForceResyncsAfterAutoStash(t *testing.T) {
	t.Parallel()
	repoRoot, sourceDir := setupRepo(t)
	statePath := filepath.Join(repoRoot, ".git", "treesync", "state.json")

	// Diverge source from the target's committed content so the one-shot sync below
	// leaves the target dirty (uncommitted) when `watch` starts.
	agentContent := "hello\nfrom agent\n"
	if err := os.WriteFile(filepath.Join(sourceDir, "README.md"), []byte(agentContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// One-shot sync: copies source content into the target and persists the manifest.
	if out, err := exec.Command(binPath, "sync", sourceDir, repoRoot).CombinedOutput(); err != nil {
		t.Fatalf("one-shot sync: %v\n%s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(repoRoot, "README.md")); err != nil || string(got) != agentContent {
		t.Fatalf("target README after one-shot sync = %q (err %v), want %q", got, err, agentContent)
	}

	// `watch --force` auto-stashes the sync's changes and detaches, wiping the synced
	// content. The stale manifest must be invalidated or the initial sync will diff
	// the unchanged source against it, no-op, and leave pristine HEAD content behind.
	var out safeBuffer
	cmd := exec.Command(binPath, "watch", sourceDir, repoRoot, "--force", "--debounce-ms=50")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting watch: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	waitFor(t, 5*time.Second, "initial sync complete", func() bool {
		return bytes.Contains([]byte(out.String()), []byte("initial sync complete"))
	})

	// The regression itself: the target's file content, not the counters, must match
	// the source.
	if got, err := os.ReadFile(filepath.Join(repoRoot, "README.md")); err != nil || string(got) != agentContent {
		t.Fatalf("target README after watch --force = %q (err %v), want %q (stale manifest no-op?)", got, err, agentContent)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch process exited with error: %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watch process did not exit after SIGINT\noutput:\n%s", out.String())
	}

	// The stop path pops the auto-stash, so the stashed sync content comes back and no
	// treesync stash entry is left behind.
	if got, err := os.ReadFile(filepath.Join(repoRoot, "README.md")); err != nil || string(got) != agentContent {
		t.Fatalf("target README after stop = %q (err %v), want %q (auto-stash not restored?)", got, err, agentContent)
	}
	if stashList := runGit(t, repoRoot, "stash", "list"); strings.Contains(stashList, "treesync: auto-stash") {
		t.Fatalf("auto-stash still in stash list after stop:\n%s", stashList)
	}
	if !fileMissing(statePath) {
		t.Fatal("expected state file removed after restore")
	}
}

// TestStopRestoresAutoStash verifies that `stop` pops the auto-stash created by
// `watch --force` on a dirty target, restoring the user's uncommitted changes by their
// recorded SHA — robust to the stash index shifting, which a stash@{0} handle would not
// be — without touching the user's own stashes.
func TestStopRestoresAutoStash(t *testing.T) {
	t.Parallel()
	repoRoot, sourceDir := setupRepo(t)

	// Pre-seed an unrelated stash so the auto-stash is pushed on top of it.
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("unrelated work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoRoot, "stash", "push", "--message", "unrelated work")

	// Now dirty the target with the user's WIP: a tracked modification plus an
	// untracked file (the auto-stash is pushed with --include-untracked).
	wipContent := "user's wip\n"
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte(wipContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "wip.txt"), []byte("untracked wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out safeBuffer
	cmd := exec.Command(binPath, "watch", sourceDir, repoRoot, "--force", "--debounce-ms=50")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting watch: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	waitFor(t, 5*time.Second, "initial sync complete", func() bool {
		return bytes.Contains([]byte(out.String()), []byte("initial sync complete"))
	})

	// Push another stash while the watch runs, the way the user's own stash activity
	// would, shifting the auto-stash off stash@{0}.
	if err := os.WriteFile(filepath.Join(repoRoot, "stray.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoRoot, "stash", "push", "--include-untracked", "--message", "shift")

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch process exited with error: %v\noutput:\n%s", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watch process did not exit after SIGINT\noutput:\n%s", out.String())
	}

	if branch := trimNL(runGit(t, repoRoot, "symbolic-ref", "--short", "HEAD")); branch != "main" {
		t.Fatalf("target branch after stop = %q, want %q", branch, "main")
	}
	// The user's uncommitted changes must be back on disk.
	if got, err := os.ReadFile(filepath.Join(repoRoot, "README.md")); err != nil || string(got) != wipContent {
		t.Fatalf("target README after stop = %q (err %v), want %q (auto-stash not restored?)", got, err, wipContent)
	}
	if got, err := os.ReadFile(filepath.Join(repoRoot, "wip.txt")); err != nil || string(got) != "untracked wip\n" {
		t.Fatalf("target wip.txt after stop = %q (err %v), want the untracked WIP file back", got, err)
	}
	// The auto-stash is gone from the stash list; the user's own stashes are untouched.
	stashList := runGit(t, repoRoot, "stash", "list")
	if strings.Contains(stashList, "treesync: auto-stash") {
		t.Fatalf("auto-stash still in stash list after stop:\n%s", stashList)
	}
	if !strings.Contains(stashList, "unrelated work") || !strings.Contains(stashList, "shift") {
		t.Fatalf("expected the user's own stashes to be untouched, got:\n%s", stashList)
	}
}
