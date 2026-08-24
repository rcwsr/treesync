// Package integration exercises `treesync watch` end to end against real temporary git
// worktrees: initial sync, incremental create/modify/delete propagation, detached-HEAD
// safety, graceful stop+restore, and crash recovery.
package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	build := exec.Command("go", "build", "-o", binPath, "github.com/rcwsr-dev/treesync/cmd/treesync")
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

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
