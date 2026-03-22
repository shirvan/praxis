package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"cuelang.org/go/cue/format"
	"github.com/spf13/cobra"
)

// newFmtCmd builds the `praxis fmt` subcommand.
//
// It formats CUE template files using the canonical CUE style. Files are
// formatted in place by default. Use --check to verify formatting without
// modifying files (exits non-zero if any file would change).
func newFmtCmd() *cobra.Command {
	var check bool

	cmd := &cobra.Command{
		Use:   "fmt [files or directories...]",
		Short: "Format CUE template files",
		Long: `Format CUE files using the canonical CUE style.

By default, files are formatted in place. When --check is set, no files are
modified and the command exits with code 1 if any file would change (useful
for CI gating).

Accepts file paths, directories, or glob patterns. Directories are walked
recursively to find *.cue files.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{"."}
			}

			paths, err := collectCUEFiles(args)
			if err != nil {
				return err
			}

			if len(paths) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no .cue files found")
				return nil
			}

			unformatted := 0
			for _, path := range paths {
				changed, err := formatFile(path, check)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if changed {
					unformatted++
					if check {
						fmt.Fprintln(cmd.OutOrStdout(), path)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "formatted %s\n", path)
					}
				}
			}

			if check && unformatted > 0 {
				return fmt.Errorf("%d file(s) need formatting", unformatted)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&check, "check", false,
		"Check formatting without modifying files (exit 1 if unformatted)")

	return cmd
}

// formatFile formats a single CUE file. When check is true, the file is not
// written — the function only reports whether it would change. Returns true if
// the file content differs from the formatted output.
func formatFile(path string, check bool) (bool, error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	formatted, err := format.Source(original, format.Simplify())
	if err != nil {
		return false, fmt.Errorf("parse error: %w", err)
	}

	if string(original) == string(formatted) {
		return false, nil
	}

	if !check {
		info, err := os.Stat(path)
		if err != nil {
			return true, err
		}
		if err := os.WriteFile(path, formatted, info.Mode()); err != nil {
			return true, err
		}
	}

	return true, nil
}

// collectCUEFiles expands the argument list into a deduplicated list of .cue
// file paths. Arguments can be files, directories (walked recursively), or
// glob patterns.
func collectCUEFiles(args []string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string

	add := func(path string) {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		result = append(result, path)
	}

	for _, arg := range args {
		info, err := os.Stat(arg)
		if err == nil {
			if info.IsDir() {
				if err := filepath.WalkDir(arg, func(path string, d fs.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !d.IsDir() && filepath.Ext(path) == ".cue" {
						add(path)
					}
					return nil
				}); err != nil {
					return nil, fmt.Errorf("walk %s: %w", arg, err)
				}
				continue
			}
			add(arg)
			continue
		}

		// Try as glob pattern.
		matches, globErr := filepath.Glob(arg)
		if globErr != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", arg, globErr)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files match %q", arg)
		}
		for _, m := range matches {
			if filepath.Ext(m) == ".cue" {
				add(m)
			}
		}
	}

	return result, nil
}
