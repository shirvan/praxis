package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd builds the `praxis version` subcommand.
//
// It prints the CLI version and build date — useful for debugging and support.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("praxis %s (built %s)\n", version, buildDate)
		},
	}
}
