package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Worktree is a single git worktree (a checkout with its own working tree, sharing a
// repository with any sibling worktrees).
type Worktree struct {
	Dir string // absolute path to the worktree's top-level directory
}

// Open resolves path to the git worktree that contains it.
func Open(ctx context.Context, path string) (*Worktree, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("%s is not inside a git worktree: %w", path, err)
	}
	return &Worktree{Dir: strings.TrimSpace(out)}, nil
}

// Run executes an arbitrary git subcommand with this worktree as its working directory.
func (w *Worktree) Run(ctx context.Context, args ...string) (string, error) {
	return run(ctx, w.Dir, args...)
}

// CommonDir returns the shared ".git" directory for this worktree's repository — the
// same path for every worktree of a given repo, so it doubles as a stable place to keep
// per-repo treesync state.
func (w *Worktree) CommonDir(ctx context.Context) (string, error) {
	out, err := run(ctx, w.Dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(w.Dir, dir)
	}
	return filepath.Clean(dir), nil
}

// SameRepo reports whether a and b are worktrees of the same repository.
func SameRepo(ctx context.Context, a, b *Worktree) (bool, error) {
	ca, err := a.CommonDir(ctx)
	if err != nil {
		return false, err
	}
	cb, err := b.CommonDir(ctx)
	if err != nil {
		return false, err
	}
	return ca == cb, nil
}

// FindMainWorktree returns the path of the repository's primary worktree — git always
// lists it first from `git worktree list`.
func FindMainWorktree(ctx context.Context, repoDir string) (string, error) {
	out, err := run(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("no worktrees found for %s", repoDir)
}

// CurrentBranch returns the checked-out branch name, or "" if HEAD is detached.
func (w *Worktree) CurrentBranch(ctx context.Context) (string, error) {
	out, err := run(ctx, w.Dir, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "", nil // non-zero exit here means detached HEAD, not a real error
	}
	return strings.TrimSpace(out), nil
}

// CurrentHead returns the commit hash HEAD currently points to.
func (w *Worktree) CurrentHead(ctx context.Context) (string, error) {
	out, err := run(ctx, w.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Detach checks out the current commit directly, leaving HEAD detached so nothing
// commits to (or moves) the branch that was checked out before.
func (w *Worktree) Detach(ctx context.Context) error {
	_, err := run(ctx, w.Dir, "checkout", "--detach", "--quiet")
	return err
}

// Checkout switches this worktree to ref (a branch name or commit hash).
func (w *Worktree) Checkout(ctx context.Context, ref string) error {
	_, err := run(ctx, w.Dir, "checkout", "--quiet", ref)
	return err
}

// ForceCheckout switches this worktree to ref, discarding uncommitted modifications to
// tracked files — unlike Checkout, this works even when ref is already checked out.
func (w *Worktree) ForceCheckout(ctx context.Context, ref string) error {
	_, err := run(ctx, w.Dir, "checkout", "--quiet", "--force", ref)
	return err
}

// Clean removes untracked files and directories (but never gitignored ones).
func (w *Worktree) Clean(ctx context.Context) error {
	_, err := run(ctx, w.Dir, "clean", "-fd", "--quiet")
	return err
}

// TrackedFiles returns every path git has indexed, relative to the worktree root.
func (w *Worktree) TrackedFiles(ctx context.Context) ([]string, error) {
	out, err := run(ctx, w.Dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	return splitNul(out), nil
}

// UntrackedFiles returns paths present on disk but not indexed and not gitignored.
func (w *Worktree) UntrackedFiles(ctx context.Context) ([]string, error) {
	out, err := run(ctx, w.Dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return splitNul(out), nil
}
