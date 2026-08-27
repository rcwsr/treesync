package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rcwsr/treesync/internal/logging"
	"github.com/rcwsr/treesync/internal/state"
	"github.com/rcwsr/treesync/internal/syncengine"
	"github.com/rcwsr/treesync/internal/watcher"
)

func newWatchCmd() *cobra.Command {
	var debounceMs int
	var force bool
	var logLevel string
	cmd := &cobra.Command{
		Use:   "watch [source] [target]",
		Short: "Continuously mirror source's working tree into target on a detached HEAD",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd.Context(), args, debounceMs, force, logLevel)
		},
	}
	cmd.Flags().IntVar(&debounceMs, "debounce-ms", 200, "debounce window (ms) for batching file changes")
	cmd.Flags().BoolVar(&force, "force", false, "allow watching into a target with uncommitted changes (auto-stashes them)")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	return cmd
}

func runWatch(parent context.Context, args []string, debounceMs int, force bool, logLevel string) error {
	logger := logging.New(logLevel)

	source, target, err := resolveSourceTarget(parent, args)
	if err != nil {
		return err
	}

	commonDir, err := target.CommonDir(parent)
	if err != nil {
		return err
	}
	mgr := state.New(commonDir)

	sourceBranch, _ := source.CurrentBranch(parent)
	unlock, err := mgr.Lock(state.LockInfo{SourceDir: source.Dir, SourceBranch: sourceBranch})
	if err != nil {
		return fmt.Errorf("%s: %w", target.Dir, err)
	}
	defer unlock()

	if err := mgr.RecoverIfStale(parent, target, logger); err != nil {
		return err
	}

	clean, err := target.IsClean(parent)
	if err != nil {
		return err
	}
	autoStashRef := ""
	if !clean {
		if !force {
			return fmt.Errorf("target %s has uncommitted changes; commit/stash them or pass --force", target.Dir)
		}
		if _, err := target.Run(parent, "stash", "push", "--include-untracked", "--message", "treesync: auto-stash before watch"); err != nil {
			return fmt.Errorf("auto-stashing target's changes: %w", err)
		}
		// Record the stash commit SHA (not stash@{0}, which shifts as other stashes
		// are pushed or popped) so restore can later pop exactly this stash.
		ref, err := target.Run(parent, "rev-parse", "refs/stash")
		if err != nil {
			return fmt.Errorf("resolving auto-stash commit: %w", err)
		}
		autoStashRef = strings.TrimSpace(ref)
		logger.Warn("stashed target's uncommitted changes before detaching", "target", target.Dir, "stash", autoStashRef)
	}

	if _, err := mgr.Detach(parent, target, autoStashRef); err != nil {
		return fmt.Errorf("detaching target: %w", err)
	}
	logger.Info("target detached", "target", target.Dir)

	// The auto-stash and the detach both rewrote the target's working tree, so the
	// persisted manifest no longer describes it; drop it to force a full re-sync.
	if err := mgr.InvalidateManifest(); err != nil {
		return fmt.Errorf("invalidating manifest: %w", err)
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if err := mgr.Restore(context.Background(), target, logger); err != nil {
			logger.Error("failed to restore target", "err", err)
			return
		}
		logger.Info("target restored", "target", target.Dir)
	}
	defer restore()

	engine := syncengine.NewEngine(source, target, mgr.Dir)

	res, err := engine.Sync(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Interrupted before the first sync finished; restore still runs via defer.
			return nil
		}
		return fmt.Errorf("initial sync: %w", err)
	}
	logger.Info("initial sync complete", "created", res.Created, "modified", res.Modified, "deleted", res.Deleted)

	fw, err := watcher.New(source.Dir, time.Duration(debounceMs)*time.Millisecond, logger)
	if err != nil {
		return fmt.Errorf("starting watcher: %w", err)
	}

	watchErr := make(chan error, 1)
	go func() { watchErr <- fw.Run(ctx) }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-watchErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("watcher: %w", err)
			}
			return nil
		case _, ok := <-fw.Events():
			if !ok {
				return nil
			}
			res, err := engine.Sync(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				logger.Error("sync failed", "err", err)
				continue
			}
			if res.Created > 0 || res.Modified > 0 || res.Deleted > 0 {
				logger.Info("synced", "created", res.Created, "modified", res.Modified, "deleted", res.Deleted, "duration", res.Duration)
			}
		}
	}
}
