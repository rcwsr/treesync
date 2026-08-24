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

	"github.com/rcwsr-dev/treesync/internal/git"
)

// TargetState is what a target was checked out to before treesync detached it.
type TargetState struct {
	OriginalBranch string    `json:"original_branch,omitempty"` // empty if it was already detached
	OriginalHead   string    `json:"original_head"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
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

func (m *Manager) statePath() string    { return filepath.Join(m.Dir, "state.json") }
func (m *Manager) pidPath() string      { return filepath.Join(m.Dir, "watch.pid") }
func (m *Manager) manifestPath() string { return filepath.Join(m.Dir, "manifest.json") }

// Detach snapshots target's current branch/HEAD, records this process's PID, and
// switches target onto a detached HEAD so sync never touches its real branch.
func (m *Manager) Detach(ctx context.Context, target *git.Worktree) (*TargetState, error) {
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
	st := &TargetState{OriginalBranch: branch, OriginalHead: head, PID: os.Getpid(), StartedAt: time.Now()}
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
// detached) and removes the state/pid files once the target is verified clean.
func (m *Manager) Restore(ctx context.Context, target *git.Worktree) error {
	st, err := m.loadState()
	if err != nil {
		return err
	}
	ref := st.OriginalBranch
	if ref == "" {
		ref = st.OriginalHead
	}
	// Force-checkout: sync may have left tracked files modified relative to ref's tree
	// (a plain checkout leaves modified files untouched when ref is the same commit that
	// was already checked out), and clean removes any untracked files sync copied in —
	// safe because target was required to be clean (no untracked non-ignored files)
	// before it was ever detached.
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
	_ = os.Remove(m.statePath())
	_ = os.Remove(m.pidPath())
	// The manifest records what was last mirrored into target; once target is restored
	// to its original checkout, those records no longer describe what's on disk, so a
	// future sync must start from a clean slate rather than skip files it thinks already
	// match.
	_ = os.Remove(m.manifestPath())
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
// exiting cleanly (e.g. kill -9 or a crash). It errors if that session's process is
// still alive, since two watchers must never detach the same target concurrently.
func (m *Manager) RecoverIfStale(ctx context.Context, target *git.Worktree, logger *slog.Logger) error {
	if _, err := os.Stat(m.statePath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if running, pid := m.IsRunning(); running {
		return fmt.Errorf("treesync is already watching %s (pid %d)", target.Dir, pid)
	} else if pid != 0 {
		logger.Warn("recovering target from a previous treesync session that did not exit cleanly", "target", target.Dir, "pid", pid)
	}
	return m.Restore(ctx, target)
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
