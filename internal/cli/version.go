package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newVersionCmd builds the `praxis version` subcommand.
//
// It prints the CLI version and build date — useful for debugging and support.
func newVersionCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.outputFormat() == OutputJSON {
				return printJSON(map[string]string{"version": version, "buildDate": buildDate})
			}
			fmt.Printf("praxis %s (built %s)\n", version, buildDate)
			return nil
		},
	}
}
