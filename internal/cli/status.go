package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rcwsr/treesync/internal/state"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [target]",
		Short: "Show whether treesync is watching target, and its last sync state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) > 0 {
				arg = args[0]
			}
			return runStatus(cmd.Context(), arg)
		},
	}
}

func runStatus(ctx context.Context, targetArg string) error {
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
	head, _ := target.CurrentHead(ctx)

	if !running {
		branch, _ := target.CurrentBranch(ctx)
		fmt.Printf("not running for target %s (%s)\n", target.Dir, branchOr(branch, head))
		return nil
	}

	st, err := mgr.LoadStateForStatus()
	if err != nil {
		fmt.Printf("running (pid %d) but state is unreadable: %v\n", pid, err)
		return nil
	}
	fmt.Printf("running (pid %d) since %s\n", pid, st.StartedAt.Format(time.RFC3339))
	fmt.Printf("target %s is detached; original checkout: %s\n", target.Dir, branchOr(st.OriginalBranch, st.OriginalHead))
	fmt.Printf("current target HEAD: %s\n", head)
	return nil
}

func branchOr(branch, head string) string {
	if branch != "" {
		return branch
	}
	return "detached at " + head
}
