// renderer.go centralises all styled output logic for the CLI.
//
// Every command that writes to the terminal goes through a Renderer.
// The Renderer holds the active Theme and decides, via the `styles` flag,
// whether to apply lipgloss formatting or emit plain undecorated text.
//
// Two output paths are supported:
//   - Styled path: lipgloss-formatted tables, colored status badges, diff markers.
//   - Plain path: ASCII tables via text/tabwriter, raw text (for --plain, JSON mode,
//     piped output, or NO_COLOR).
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"

	"github.com/shirvan/praxis/pkg/types"
)

// Renderer is the central output abstraction for the CLI. Commands call its
// methods to write label/value pairs, tables, status badges, diff markers, etc.
// It automatically delegates to the plain or styled code path depending on
// whether colored output is enabled.
type Renderer struct {
	// out is the primary output writer (typically os.Stdout).
	out io.Writer
	// errOut is the error output writer (typically os.Stderr).
	errOut io.Writer
	// theme holds the active color/style definitions.
	theme *Theme
	// styles is true when lipgloss formatting is active.
	styles bool
}

// newRenderer creates a Renderer that writes to stdout/stderr with the
// given styling preference.
func newRenderer(styles bool) *Renderer {
	return newRendererWithWriters(styles, os.Stdout, os.Stderr)
}

// newRendererWithWriters creates a Renderer with explicit output writers.
// Used in tests to capture output without writing to the real terminal.
func newRendererWithWriters(styles bool, out, errOut io.Writer) *Renderer {
	theme := plainTheme()
	if styles {
		theme = newTheme()
	}
	return &Renderer{out: out, errOut: errOut, theme: theme, styles: styles}
}

// defaultRenderer builds a Renderer from the current global root flags.
// Falls back to auto-detecting tty/color settings when no root flags are set.
func defaultRenderer() *Renderer {
	if currentRootFlags != nil {
		return currentRootFlags.renderer()
	}
	return newRenderer(shouldUseStyles(OutputTable, false, os.Getenv("NO_COLOR") != "", isTerminal(os.Stdout)))
}

// renderMuted returns text styled with the muted/grey color, or plain text
// when styling is disabled. Used for timestamps, secondary info, and hints.
func (r *Renderer) renderMuted(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Muted.Render(text)
}

// renderPrompt returns text styled as a user prompt (bold + warning color).
// Used for confirmation prompts like "Do you want to apply?".
func (r *Renderer) renderPrompt(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Prompt.Render(text)
}

// renderSection returns text styled as a section header (bold).
// Used for headings like "Outputs:", "Errors:", "Variables:".
func (r *Renderer) renderSection(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Header.Render(text)
}

// renderValue returns text styled as a normal value.
func (r *Renderer) renderValue(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Value.Render(text)
}

// renderStatus maps a deployment/resource status string to a colored badge.
// Ready/success statuses → green, pending/running → yellow, failed → red,
// cancelled/skipped → grey.
func (r *Renderer) renderStatus(status string) string {
	if !r.styles {
		return status
	}

	style := r.theme.StatusMuted
	switch strings.ToLower(status) {
	case "ready", "complete", "deleted", "success":
		style = r.theme.StatusReady
	case "pending", "running", "provisioning", "applying", "deleting":
		style = r.theme.StatusPending
	case "failed", "error":
		style = r.theme.StatusError
	case "cancelled", "canceled", "skipped":
		style = r.theme.StatusMuted
	}
	return style.Render(status)
}

// renderDiff applies diff-aware coloring: green for creates, yellow for
// updates, red for deletes. Used in plan output and event display.
func (r *Renderer) renderDiff(op types.DiffOperation, text string) string {
	if !r.styles {
		return text
	}
	style := r.theme.Muted
	switch op {
	case types.OpCreate:
		style = r.theme.DiffCreate
	case types.OpUpdate:
		style = r.theme.DiffUpdate
	case types.OpDelete:
		style = r.theme.DiffDelete
	}
	return style.Render(text)
}

// writeLabelValue writes a "Label:  value" line with the label left-padded
// to `width` characters. The label is styled as a key, the value as text.
func (r *Renderer) writeLabelValue(label string, width int, value string) {
	prefix := fmt.Sprintf("%-*s", width, label+":")
	if r.styles {
		prefix = r.theme.Key.Render(prefix)
	}
	_, _ = fmt.Fprintf(r.out, "%s %s\n", prefix, r.renderValue(value))
}

// writeLabelStyledValue is like writeLabelValue but the value has already
// been styled by the caller (e.g. a colored status badge).
func (r *Renderer) writeLabelStyledValue(label string, width int, value string) {
	prefix := fmt.Sprintf("%-*s", width, label+":")
	if r.styles {
		prefix = r.theme.Key.Render(prefix)
	}
	_, _ = fmt.Fprintf(r.out, "%s %s\n", prefix, value)
}

// successLine prints a success message, prefixed with a green "✓" when
// styles are enabled.
func (r *Renderer) successLine(text string) {
	if !r.styles {
		_, _ = fmt.Fprintln(r.out, text)
		return
	}
	_, _ = fmt.Fprintln(r.out, r.theme.Success.Render("✓")+" "+text)
}

// printTable renders a data table. In styled mode it uses lipgloss table
// rendering with borders and alternating row colors. In plain mode it falls
// back to the printPlainTable helper (text/tabwriter).
func (r *Renderer) printTable(headers []string, rows [][]string) {
	if !r.styles {
		printPlainTable(r.out, headers, rows)
		return
	}

	tbl := table.New().
		Border(lipgloss.NormalBorder()).
		BorderStyle(r.theme.TableBorder).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			switch {
			case row == table.HeaderRow:
				return r.theme.TableHeader
			case row%2 == 0:
				return r.theme.TableAltRow
			default:
				return r.theme.TableCell
			}
		})

	_, _ = fmt.Fprintln(r.out, tbl.Render())
}
