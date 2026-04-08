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
	"github.com/charmbracelet/glamour"

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
	case "pending", "running", "provisioning", "updating", "applying", "deleting":
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

// renderMarkdown renders a markdown string with terminal-aware styling using
// glamour. In plain mode (--plain, JSON, piped output) it writes the raw text.
func (r *Renderer) renderMarkdown(md string) {
	if !r.styles {
		_, _ = fmt.Fprintln(r.out, md)
		return
	}

	style := glamour.DarkStyleConfig
	if !lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		style = glamour.LightStyleConfig
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		_, _ = fmt.Fprintln(r.out, md)
		return
	}

	rendered, err := renderer.Render(md)
	if err != nil {
		_, _ = fmt.Fprintln(r.out, md)
		return
	}

	_, _ = fmt.Fprint(r.out, rendered)
}

// renderProgressEntry formats a single progress entry for live display
// during Ask execution. Written to stderr via the spinner's PrintLine.
func (r *Renderer) renderProgressEntry(entry conciergeProgressEntry) string {
	switch entry.Status {
	case "running":
		if r.styles {
			return fmt.Sprintf("  %s %s",
				r.theme.Muted.Render("▸"),
				r.theme.Muted.Render(entry.Name))
		}
		return fmt.Sprintf("  ... %s", entry.Name)
	case "ok":
		if r.styles {
			return fmt.Sprintf("  %s %s",
				r.theme.Success.Render("✓"),
				r.theme.Value.Render(entry.Name))
		}
		return fmt.Sprintf("  OK  %s", entry.Name)
	default: // "error"
		if r.styles {
			return fmt.Sprintf("  %s %s: %s",
				r.theme.Error.Render("✗"),
				r.theme.Value.Render(entry.Name),
				r.theme.Muted.Render(entry.Error))
		}
		return fmt.Sprintf("  ERR %s: %s", entry.Name, entry.Error)
	}
}

// renderToolLog prints the tool call log with styled indicators. Each tool
// invocation is shown with a status icon (✓ for success, ✗ for error) and duration.
func (r *Renderer) renderToolLog(tools []conciergeToolLog) {
	if len(tools) == 0 {
		return
	}

	_, _ = fmt.Fprintln(r.out)
	if r.styles {
		_, _ = fmt.Fprintln(r.out, r.theme.Muted.Render("── Tool Calls ──"))
	} else {
		_, _ = fmt.Fprintln(r.out, "-- Tool Calls --")
	}

	for _, t := range tools {
		dur := formatToolDuration(t.DurationMs)
		if t.Status == "ok" {
			if r.styles {
				icon := r.theme.Success.Render("✓")
				name := r.theme.Value.Render(t.Name)
				if dur != "" {
					_, _ = fmt.Fprintf(r.out, "  %s %s %s\n", icon, name, r.theme.Muted.Render(dur))
				} else {
					_, _ = fmt.Fprintf(r.out, "  %s %s\n", icon, name)
				}
			} else {
				if dur != "" {
					_, _ = fmt.Fprintf(r.out, "  OK  %s %s\n", t.Name, dur)
				} else {
					_, _ = fmt.Fprintf(r.out, "  OK  %s\n", t.Name)
				}
			}
		} else {
			if r.styles {
				icon := r.theme.Error.Render("✗")
				name := r.theme.Value.Render(t.Name)
				errMsg := r.theme.Muted.Render(t.Error)
				_, _ = fmt.Fprintf(r.out, "  %s %s: %s\n", icon, name, errMsg)
			} else {
				_, _ = fmt.Fprintf(r.out, "  ERR %s: %s\n", t.Name, t.Error)
			}
		}
	}
	_, _ = fmt.Fprintln(r.out)
}

// renderConciergeResponse renders the full concierge response: model header,
// tool log, markdown-rendered response body, and a status footer with usage stats.
// When toolsStreamed is true, the tool log section is skipped because the CLI
// already rendered tool calls in real time via progress polling.
func (r *Renderer) renderConciergeResponse(resp *conciergeAskResponse, toolsStreamed ...bool) {
	streamed := len(toolsStreamed) > 0 && toolsStreamed[0]
	// Header: model/provider info + session ID.
	if resp.Provider != "" || resp.Model != "" {
		label := resp.Model
		if resp.Provider != "" && resp.Model != "" {
			label = resp.Provider + "/" + resp.Model
		} else if resp.Provider != "" {
			label = resp.Provider
		}
		if resp.SessionID != "" {
			label += " (session: " + resp.SessionID + ")"
		}
		if r.styles {
			_, _ = fmt.Fprintf(r.out, "%s %s\n",
				r.theme.Muted.Render("▸"),
				r.theme.Muted.Render(label))
		} else {
			_, _ = fmt.Fprintf(r.out, "> %s\n", label)
		}
	}

	// Tool call log (if any tools were invoked and not already streamed live).
	if !streamed {
		r.renderToolLog(resp.ToolLog)
	}

	// Response body with markdown rendering.
	r.renderMarkdown(resp.Response)

	// Footer: turn count, tool calls, tokens, and duration.
	if resp.TurnCount > 0 || resp.Usage.TotalTokens > 0 || resp.DurationMs > 0 {
		var parts []string

		if resp.TurnCount > 0 {
			parts = append(parts, fmt.Sprintf("%d turns", resp.TurnCount))
		}

		toolCount := len(resp.ToolLog)
		if toolCount > 0 {
			parts = append(parts, fmt.Sprintf("%d tool calls", toolCount))
		}

		if resp.Usage.TotalTokens > 0 {
			parts = append(parts, fmt.Sprintf("%s tokens", formatTokenCount(resp.Usage.TotalTokens)))
		}

		if resp.DurationMs > 0 {
			parts = append(parts, formatDurationMs(resp.DurationMs))
		}

		if len(parts) > 0 {
			footer := joinParts(parts)
			if r.styles {
				_, _ = fmt.Fprintln(r.out, r.theme.Muted.Render(footer))
			} else {
				_, _ = fmt.Fprintln(r.out, footer)
			}
		}
	}
}

// formatToolDuration returns a compact duration string for a tool call.
// Returns empty string if duration is zero (not tracked).
func formatToolDuration(ms int64) string {
	if ms <= 0 {
		return ""
	}
	switch {
	case ms < 1000:
		return fmt.Sprintf("(%dms)", ms)
	default:
		return fmt.Sprintf("(%.1fs)", float64(ms)/1000)
	}
}

// formatTokenCount formats a token count with thousand separators.
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// formatDurationMs converts milliseconds to a compact duration string.
func formatDurationMs(ms int64) string {
	switch {
	case ms < 1000:
		return fmt.Sprintf("%dms", ms)
	case ms < 60000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		m := ms / 60000
		s := (ms % 60000) / 1000
		return fmt.Sprintf("%dm%ds", m, s)
	}
}

// joinParts joins string parts with " · " separator.
func joinParts(parts []string) string {
	return strings.Join(parts, " \u00b7 ")
}
