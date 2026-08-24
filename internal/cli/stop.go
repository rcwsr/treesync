package cli

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/rcwsr/treesync/internal/state"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop [target]",
		Short: "Stop a running treesync watch and restore the target's original checkout",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			return runStop(cmd.Context(), arg)
		},
	}
}

func runStop(ctx context.Context, targetArg string) error {
	target, err := resolveTarget(ctx, targetArg)
	if err != nil {
		return err
	}
	commonDir, err := target.CommonDir(ctx)
	if err != nil {
		return err
	}
	mgr := state.New(commonDir)

	running, pid := mgr.IsRunning()
	if !running {
		return fmt.Errorf("no running treesync watch found for target %s", target.Dir)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signaling pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if running, _ := mgr.IsRunning(); !running {
			fmt.Printf("stopped treesync watch (pid %d), target %s restored\n", pid, target.Dir)
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("force-killing pid %d: %w", pid, err)
	}
	fmt.Printf("force-killed treesync watch (pid %d); if target %s wasn't restored, run `git checkout <branch>` manually or re-run `treesync watch` to auto-recover\n", pid, target.Dir)
	return nil
}
