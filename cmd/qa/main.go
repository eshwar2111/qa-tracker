// Command qa is the qa-tracker binary: `qa serve` runs the web UI, every
// other subcommand is the CLI Claude uses to read bugs and record fixes.
package main

import (
	"os"

	"qa-tracker/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
