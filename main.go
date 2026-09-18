// Command agi is the entry point for the agi CLI.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ChrisVilches/agi/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := cli.NewApp(version).Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "agi:", err)
		os.Exit(1)
	}
}
