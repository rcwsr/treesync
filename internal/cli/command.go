// Package cli wires cobra subcommands (watch, sync, stop, status) on top of the
// internal git/sync/state/watcher packages.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rcwsr/treesync/internal/git"
)

// NewRootCmd builds the treesync root command with its watch/sync/stop/status
// subcommands attached.
func NewRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:          "treesync",
		Short:        "Live-sync a git worktree's changes into another worktree on a detached HEAD",
		Version:      version,
		SilenceUsage: true,
	}
	root.AddCommand(newWatchCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newStatusCmd())
	return root
}

// resolveSourceTarget resolves up to two positional args into worktrees, defaulting
// source to the worktree containing cwd and target to the repo's main worktree (the
// first entry `git worktree list` always reports).
func resolveSourceTarget(ctx context.Context, args []string) (source, target *git.Worktree, err error) {
	sourceDir := "."
	if len(args) > 0 && args[0] != "" {
		sourceDir = args[0]
	}
	source, err = git.Open(ctx, sourceDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving source: %w", err)
	}

	targetDir := ""
	if len(args) > 1 && args[1] != "" {
		targetDir = args[1]
	}
	if targetDir == "" {
		targetDir, err = git.FindMainWorktree(ctx, source.Dir)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving default target (main checkout): %w", err)
		}
	}
	target, err = git.Open(ctx, targetDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving target: %w", err)
	}

	if source.Dir == target.Dir {
		return nil, nil, fmt.Errorf("source and target both resolved to %s; pass an explicit target", source.Dir)
	}
	same, err := git.SameRepo(ctx, source, target)
	if err != nil {
		return nil, nil, err
	}
	if !same {
		return nil, nil, fmt.Errorf("source (%s) and target (%s) are not worktrees of the same repository", source.Dir, target.Dir)
	}
	return source, target, nil
}

// resolveTarget resolves a single optional target arg (used by stop/status), defaulting
// to the main worktree of the repo containing cwd.
func resolveTarget(ctx context.Context, arg string) (*git.Worktree, error) {
	if arg == "" {
		var err error
		arg, err = git.FindMainWorktree(ctx, ".")
		if err != nil {
			return nil, fmt.Errorf("resolving default target (main checkout): %w", err)
		}
	}
	return git.Open(ctx, arg)
}
