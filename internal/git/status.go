package git

import (
	"context"
	"strings"
)

// IsClean reports whether the worktree has no uncommitted, staged, or untracked changes.
func (w *Worktree) IsClean(ctx context.Context) (bool, error) {
	out, err := run(ctx, w.Dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}
