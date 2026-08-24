package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rcwsr/treesync/internal/cli"
)

// version is set via -ldflags "-X main.version=..." by GoReleaser at build time.
var version = "dev"

func main() {
	if err := cli.NewRootCmd(version).ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "treesync:", err)
		os.Exit(1)
	}
}
