package cli

import (
	"os"

	lipgloss "charm.land/lipgloss/v2"
)

// Theme holds reusable styles for human-readable CLI output.
type Theme struct {
	StatusReady   lipgloss.Style
	StatusError   lipgloss.Style
	StatusPending lipgloss.Style
	StatusMuted   lipgloss.Style

	DiffCreate lipgloss.Style
	DiffUpdate lipgloss.Style
	DiffDelete lipgloss.Style

	Header  lipgloss.Style
	Key     lipgloss.Style
	Value   lipgloss.Style
	Success lipgloss.Style
	Error   lipgloss.Style
	Warning lipgloss.Style
	Muted   lipgloss.Style
	Prompt  lipgloss.Style

	TableHeader lipgloss.Style
	TableBorder lipgloss.Style
	TableCell   lipgloss.Style
	TableAltRow lipgloss.Style
}

func newTheme() *Theme {
	readyColor := lipgloss.Color("42")
	warnColor := lipgloss.Color("214")
	errorColor := lipgloss.Color("203")
	mutedColor := lipgloss.Color("244")
	borderColor := lipgloss.Color("240")
	textColor := lipgloss.Color("252")

	if !lipgloss.HasDarkBackground(os.Stdin, os.Stdout) {
		readyColor = lipgloss.Color("28")
		warnColor = lipgloss.Color("130")
		errorColor = lipgloss.Color("160")
		mutedColor = lipgloss.Color("239")
		borderColor = lipgloss.Color("246")
		textColor = lipgloss.Color("235")
	}

	return &Theme{
		StatusReady:   lipgloss.NewStyle().Foreground(readyColor).Bold(true),
		StatusError:   lipgloss.NewStyle().Foreground(errorColor).Bold(true),
		StatusPending: lipgloss.NewStyle().Foreground(warnColor).Bold(true),
		StatusMuted:   lipgloss.NewStyle().Foreground(mutedColor),

		DiffCreate: lipgloss.NewStyle().Foreground(readyColor).Bold(true),
		DiffUpdate: lipgloss.NewStyle().Foreground(warnColor).Bold(true),
		DiffDelete: lipgloss.NewStyle().Foreground(errorColor).Bold(true),

		Header:  lipgloss.NewStyle().Foreground(textColor).Bold(true),
		Key:     lipgloss.NewStyle().Foreground(mutedColor).Bold(true),
		Value:   lipgloss.NewStyle().Foreground(textColor),
		Success: lipgloss.NewStyle().Foreground(readyColor).Bold(true),
		Error:   lipgloss.NewStyle().Foreground(errorColor).Bold(true),
		Warning: lipgloss.NewStyle().Foreground(warnColor).Bold(true),
		Muted:   lipgloss.NewStyle().Foreground(mutedColor),
		Prompt:  lipgloss.NewStyle().Foreground(warnColor).Bold(true),

		TableHeader: lipgloss.NewStyle().Foreground(textColor).Bold(true).Padding(0, 1),
		TableBorder: lipgloss.NewStyle().Foreground(borderColor),
		TableCell:   lipgloss.NewStyle().Padding(0, 1),
		TableAltRow: lipgloss.NewStyle().Padding(0, 1).Foreground(mutedColor),
	}
}

func plainTheme() *Theme {
	plain := lipgloss.NewStyle()
	return &Theme{
		StatusReady:   plain,
		StatusError:   plain,
		StatusPending: plain,
		StatusMuted:   plain,
		DiffCreate:    plain,
		DiffUpdate:    plain,
		DiffDelete:    plain,
		Header:        plain,
		Key:           plain,
		Value:         plain,
		Success:       plain,
		Error:         plain,
		Warning:       plain,
		Muted:         plain,
		Prompt:        plain,
		TableHeader:   plain,
		TableBorder:   plain,
		TableCell:     plain,
		TableAltRow:   plain,
	}
}
