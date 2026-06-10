# Extend CLI

**Description**: Add new CLI commands or subcommands to the Praxis CLI.

**When to Use**: Adding new user-facing functionality to the `praxis` command.

**Prerequisites**:
- Read [docs/CLI.md](../../docs/CLI.md) for CLI conventions
- Understand Cobra command patterns

---

## Steps

### 1. Create Command File

File: `internal/cli/{command}.go`

```go
package cli

import (
    "fmt"
    "github.com/spf13/cobra"
)

func new{Command}Cmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "{command} [args]",
        Short: "One-line description",
        Long:  "Longer description for help text.",
        Args:  cobra.ExactArgs(1), // or NoArgs, MinimumArgs, etc.
        RunE: func(cmd *cobra.Command, args []string) error {
            // Get client
            client, err := getClient(cmd)
            if err != nil {
                return err
            }
            
            // Get flags
            name, _ := cmd.Flags().GetString("name")
            
            // Call backend
            result, err := client.{Operation}(cmd.Context(), /* request */)
            if err != nil {
                return formatError(err)
            }
            
            // Output
            output, _ := cmd.Flags().GetString("output")
            if output == "json" {
                return printJSON(result)
            }
            return printTable(result)
        },
    }
    
    // Add flags
    cmd.Flags().StringP("name", "n", "", "Resource name")
    cmd.MarkFlagRequired("name")
    
    return cmd
}
```

### 2. Register in Root Command

File: `internal/cli/root.go` (or wherever commands are assembled)

```go
rootCmd.AddCommand(new{Command}Cmd())
```

### 3. Add Client Method (if needed)

File: `internal/cli/client.go`

```go
func (c *Client) {Operation}(ctx context.Context, req types.{Request}) (types.{Response}, error) {
    return restateCall[types.{Request}, types.{Response}](
        ctx, c.endpoint, "PraxisCommandService", "{Operation}", req,
    )
}
```

### 4. Create Tests

File: `internal/cli/{command}_test.go`

---

## Conventions

- **Verb-first grammar**: `praxis <verb> [noun] [flags]`
- **Global flags**: `--endpoint`, `-o/--output`, `--plain`, `--region`, `--account`
- **Output formats**: table (default) + JSON (`-o json`)
- **Styled output**: Use styling helpers, respect `--plain` flag
- **Error formatting**: Use `formatError()` for consistent error display
- **Confirmation**: Use `confirmAction()` for destructive operations

## Verification

1. `go build ./cmd/praxis/...` — compiles
2. `go test ./internal/cli/... -run {Command} -v` — tests pass
3. `bin/praxis {command} --help` — help text displays correctly
