// Package state manages the detached-HEAD safety mechanism: snapshotting a target
// worktree's original checkout before syncing into it, and restoring it afterwards
// (including recovering from a crash that left the target detached).
package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rcwsr/treesync/internal/git"
)

// TargetState is what a target was checked out to before treesync detached it.
type TargetState struct {
	OriginalBranch string    `json:"original_branch,omitempty"` // empty if it was already detached
	OriginalHead   string    `json:"original_head"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	// AutoStashRef is the commit SHA of the stash holding the target's pre-watch
	// uncommitted changes, when --force auto-stashed them. A SHA rather than
	// stash@{N} because the index shifts as other stashes are pushed or popped.
	// Empty when the target was already clean and nothing was stashed.
	AutoStashRef string `json:"auto_stash_ref,omitempty"`
}

// Manager owns the state/pid/manifest files for one target, kept under the target's
// shared .git directory so every worktree of the repo agrees on the same location.
type Manager struct {
	Dir string
}

// New builds a Manager whose state/pid files live under gitCommonDir/treesync — the
// target's shared .git directory, the same for every worktree of the repo.
func New(gitCommonDir string) *Manager {
	return &Manager{Dir: filepath.Join(gitCommonDir, "treesync")}
}

// InvalidateManifest drops the persisted manifest so the next sync re-copies every file.
// Call this after anything that rewrites the target's working tree outside of sync
// (auto-stash, checkout --detach), which leaves the manifest describing content the
// target no longer has; a diff against that stale manifest would be an empty no-op.
func (m *Manager) InvalidateManifest() error {
	if err := os.Remove(m.manifestPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing manifest at %s: %w", m.manifestPath(), err)
	}
	return nil
}

func (m *Manager) statePath() string    { return filepath.Join(m.Dir, "state.json") }
func (m *Manager) pidPath() string      { return filepath.Join(m.Dir, "watch.pid") }
func (m *Manager) manifestPath() string { return filepath.Join(m.Dir, "manifest.json") }
func (m *Manager) lockPath() string     { return filepath.Join(m.Dir, "lock") }

// LockInfo identifies the source worktree currently holding a target's lock, so a failed
// Lock attempt can report who's syncing.
type LockInfo struct {
	SourceDir    string `json:"source_dir"`
	SourceBranch string `json:"source_branch,omitempty"` // empty if source is detached
}

// Lock acquires an exclusive, non-blocking lock on target for the given source, so only
// one treesync process (watch or sync) can touch it at a time. The kernel releases it
// automatically if the holding process dies, so a crash never leaves the target locked
// out.
func (m *Manager) Lock(info LockInfo) (unlock func(), err error) {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("target is already being synced by %s", m.describeLockHolder())
	}
	if data, err := json.Marshal(info); err == nil {
		_ = f.Truncate(0)
		_, _ = f.WriteAt(data, 0)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// describeLockHolder best-effort reads whichever source worktree currently holds the
// lock, for a friendlier error message when acquisition fails.
func (m *Manager) describeLockHolder() string {
	data, err := os.ReadFile(m.lockPath())
	if err != nil {
		return "another treesync process"
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil || info.SourceDir == "" {
		return "another treesync process"
	}
	if info.SourceBranch == "" {
		return fmt.Sprintf("worktree %s (detached)", info.SourceDir)
	}
	return fmt.Sprintf("worktree %s (branch %s)", info.SourceDir, info.SourceBranch)
}

// Detach snapshots target's current branch/HEAD, records this process's PID, and
// switches target onto a detached HEAD so sync never touches its real branch.
// autoStashRef is the SHA of the stash made before detaching ("" if none); it is
// persisted so Restore can pop exactly that stash later.
func (m *Manager) Detach(ctx context.Context, target *git.Worktree, autoStashRef string) (*TargetState, error) {
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return nil, err
	}
	branch, err := target.CurrentBranch(ctx)
	if err != nil {
		return nil, err
	}
	head, err := target.CurrentHead(ctx)
	if err != nil {
		return nil, err
	}
	st := &TargetState{OriginalBranch: branch, OriginalHead: head, PID: os.Getpid(), StartedAt: time.Now(), AutoStashRef: autoStashRef}
	if err := m.saveState(st); err != nil {
		return nil, err
	}
	if err := os.WriteFile(m.pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, err
	}
	if err := target.Detach(ctx); err != nil {
		return nil, err
	}
	return st, nil
}

// Restore checks target back out onto its original branch (or commit, if it was already
// detached), pops the auto-stash that holds the user's pre-watch changes, and removes
// the state/pid files once the target is verified clean.
func (m *Manager) Restore(ctx context.Context, target *git.Worktree, logger *slog.Logger) error {
	st, err := m.loadState()
	if err != nil {
		return err
	}
	ref := st.OriginalBranch
	if ref == "" {
		ref = st.OriginalHead
	}
	// Force-checkout undoes sync's edits to tracked files (a plain checkout is a no-op
	// when ref is already checked out); clean removes whatever sync added.
	if err := target.ForceCheckout(ctx, ref); err != nil {
		return fmt.Errorf("restoring target to %s: %w (state left at %s for manual recovery)", ref, err, m.statePath())
	}
	if err := target.Clean(ctx); err != nil {
		return fmt.Errorf("cleaning target after restore: %w (state left at %s for manual recovery)", err, m.statePath())
	}
	clean, err := target.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("target %s not clean after restore; state left at %s for manual recovery", target.Dir, m.statePath())
	}
	// The clean assertion above runs *before* the pop: a successful pop legitimately
	// re-dirties the tree with the user's pre-watch changes.
	if st.AutoStashRef != "" {
		if err := m.restoreAutoStash(ctx, target, st.AutoStashRef, logger); err != nil {
			return err
		}
	}
	_ = os.Remove(m.statePath())
	_ = os.Remove(m.pidPath())
	// The manifest is now stale (it describes what sync wrote before the restore), so
	// drop it to force a full re-sync next time.
	_ = os.Remove(m.manifestPath())
	return nil
}

// restoreAutoStash pops the auto-stash recorded in state, locating it by commit SHA
// against the current stash list (stash@{N} would be the wrong handle, since the index
// shifts as other stashes are pushed or popped). If the user already popped it manually,
// log a warning and continue. On a pop conflict, fail loudly and leave the state file
// in place for manual recovery, matching Restore's other error paths.
func (m *Manager) restoreAutoStash(ctx context.Context, target *git.Worktree, sha string, logger *slog.Logger) error {
	out, err := target.Run(ctx, "stash", "list", "--format=%H")
	if err != nil {
		return fmt.Errorf("listing stashes to restore auto-stash %s: %w (state left at %s for manual recovery)", sha, err, m.statePath())
	}
	for i, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != sha {
			continue
		}
		ref := fmt.Sprintf("stash@{%d}", i)
		if _, err := target.Run(ctx, "stash", "pop", ref); err != nil {
			return fmt.Errorf("auto-stash %s (%s) could not be popped, likely a merge conflict: resolve it in %s, then `git stash drop %s` when done; state left at %s for manual recovery", sha, ref, target.Dir, ref, m.statePath())
		}
		logger.Info("restored target's auto-stashed uncommitted changes", "stash", sha)
		return nil
	}
	logger.Warn("auto-stash is no longer in the stash list (popped manually?); continuing without it", "stash", sha)
	return nil
}

// IsRunning reports whether the pidfile points at a live process.
func (m *Manager) IsRunning() (bool, int) {
	data, err := os.ReadFile(m.pidPath())
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, pid
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false, pid
	}
	return true, pid
}

// RecoverIfStale restores target from a previous session that left it detached without
// exiting cleanly (e.g. kill -9 or a crash). Callers must hold Lock before calling this,
// which is what guarantees any leftover state belongs to a dead session, not a live one.
func (m *Manager) RecoverIfStale(ctx context.Context, target *git.Worktree, logger *slog.Logger) error {
	st, err := m.loadState()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	logger.Warn("recovering target from a previous treesync session that did not exit cleanly", "target", target.Dir, "pid", st.PID)
	return m.Restore(ctx, target, logger)
}

// LoadStateForStatus exposes the persisted TargetState for `treesync status`.
func (m *Manager) LoadStateForStatus() (*TargetState, error) {
	return m.loadState()
}

func (m *Manager) saveState(st *TargetState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath())
}

func (m *Manager) loadState() (*TargetState, error) {
	data, err := os.ReadFile(m.statePath())
	if err != nil {
		return nil, fmt.Errorf("reading state: %w", err)
	}
	var st TargetState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}
