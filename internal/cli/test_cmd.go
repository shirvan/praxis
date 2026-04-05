package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// newTestCmd builds the `praxis test` top-level verb.
// Currently supports testing notification sink delivery.
func newTestCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test <resource>",
		Short: "Test an integration",
		Long: `Test verifies that an integration is working correctly.

    praxis test sink/<name>    Send a synthetic event to a notification sink`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, key, err := parseKindKey(args[0])
			if err != nil {
				return err
			}
			switch kind {
			case "sink":
				return testSink(flags, key)
			default:
				return fmt.Errorf("unsupported test resource type %q (supported: sink)", kind)
			}
		},
	}

	return cmd
}

// testSink sends a synthetic CloudEvent to the named notification sink.
func testSink(flags *rootFlags, name string) error {
	if err := flags.newClient().TestNotificationSink(context.Background(), name); err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(map[string]string{"tested": name})
	}
	flags.renderer().successLine(fmt.Sprintf("Notification sink %q accepted a synthetic event.", name))
	return nil
}
