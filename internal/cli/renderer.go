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

type Renderer struct {
	out    io.Writer
	errOut io.Writer
	theme  *Theme
	styles bool
}

func newRenderer(styles bool) *Renderer {
	return newRendererWithWriters(styles, os.Stdout, os.Stderr)
}

func newRendererWithWriters(styles bool, out, errOut io.Writer) *Renderer {
	theme := plainTheme()
	if styles {
		theme = newTheme()
	}
	return &Renderer{out: out, errOut: errOut, theme: theme, styles: styles}
}

func defaultRenderer() *Renderer {
	if currentRootFlags != nil {
		return currentRootFlags.renderer()
	}
	return newRenderer(shouldUseStyles(OutputTable, false, os.Getenv("NO_COLOR") != "", isTerminal(os.Stdout)))
}

func (r *Renderer) renderMuted(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Muted.Render(text)
}

func (r *Renderer) renderPrompt(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Prompt.Render(text)
}

func (r *Renderer) renderSection(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Header.Render(text)
}

func (r *Renderer) renderValue(text string) string {
	if !r.styles {
		return text
	}
	return r.theme.Value.Render(text)
}

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

func (r *Renderer) writeLabelValue(label string, width int, value string) {
	prefix := fmt.Sprintf("%-*s", width, label+":")
	if r.styles {
		prefix = r.theme.Key.Render(prefix)
	}
	_, _ = fmt.Fprintf(r.out, "%s %s\n", prefix, r.renderValue(value))
}

func (r *Renderer) writeLabelStyledValue(label string, width int, value string) {
	prefix := fmt.Sprintf("%-*s", width, label+":")
	if r.styles {
		prefix = r.theme.Key.Render(prefix)
	}
	_, _ = fmt.Fprintf(r.out, "%s %s\n", prefix, value)
}

func (r *Renderer) successLine(text string) {
	if !r.styles {
		_, _ = fmt.Fprintln(r.out, text)
		return
	}
	_, _ = fmt.Fprintln(r.out, r.theme.Success.Render("✓")+" "+text)
}

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
