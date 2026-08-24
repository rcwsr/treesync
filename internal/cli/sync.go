package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/rcwsr-dev/treesync/internal/state"
	"github.com/rcwsr-dev/treesync/internal/syncengine"
)

func newSyncCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "sync [source] [target]",
		Short: "Run a single sync pass from source into target without watching",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSync(cmd.Context(), args, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "allow syncing into a target with uncommitted changes")
	return cmd
}

func runSync(ctx context.Context, args []string, force bool) error {
	source, target, err := resolveSourceTarget(ctx, args)
	if err != nil {
		return err
	}

	commonDir, err := target.CommonDir(ctx)
	if err != nil {
		return err
	}
	mgr := state.New(commonDir)

	sourceBranch, _ := source.CurrentBranch(ctx)
	unlock, err := mgr.Lock(state.LockInfo{SourceDir: source.Dir, SourceBranch: sourceBranch})
	if err != nil {
		return fmt.Errorf("%s: %w", target.Dir, err)
	}
	defer unlock()

	clean, err := target.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean && !force {
		return fmt.Errorf("target %s has uncommitted changes; commit/stash them or pass --force", target.Dir)
	}

	engine := syncengine.NewEngine(source, target, mgr.Dir)

	res, err := engine.Sync(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("synced: %d created/updated, %d deleted (%s)\n", res.Created, res.Deleted, res.Duration)
	return nil
}
