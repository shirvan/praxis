// spinner.go provides a lightweight terminal spinner for long-running CLI operations.
//
// The spinner displays an animated indicator with elapsed time while the user
// waits for a response (e.g., during `praxis concierge ask`). It respects the
// styled/plain mode setting and only writes to a TTY.
package cli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinnerFrames are the braille animation frames for styled output.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinner animates a progress indicator on a single terminal line.
// It overwrites itself using carriage return (\r) and is cleared when stopped.
type spinner struct {
	out     io.Writer
	theme   *Theme
	styles  bool
	message string

	mu      sync.Mutex
	running bool
	done    chan struct{}
	wg      sync.WaitGroup
	lines   []string // permanent lines printed above the spinner
}

// newSpinner creates a spinner that writes to the given writer.
// If styles is false, the spinner prints a static one-line message instead
// of animating.
func newSpinner(out io.Writer, theme *Theme, styles bool, message string) *spinner {
	return &spinner{
		out:     out,
		theme:   theme,
		styles:  styles,
		message: message,
		done:    make(chan struct{}),
	}
}

// Start begins the spinner animation in a background goroutine.
func (s *spinner) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	if !s.styles {
		// Plain mode: print a static message, no animation.
		fmt.Fprintf(s.out, "%s...\n", s.message)
		return
	}

	s.wg.Add(1)
	go s.run()
}

// Stop halts the spinner and clears its line. Blocks until the
// animation goroutine has finished writing.
func (s *spinner) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	if s.styles {
		close(s.done)
		s.wg.Wait()
	}
}

// SetMessage updates the spinner text shown on the animated line.
func (s *spinner) SetMessage(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = msg
}

// PrintLine prints a permanent line above the spinner. The spinner clears
// its current line, prints the text, then resumes animating below it.
func (s *spinner) PrintLine(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.styles {
		fmt.Fprintln(s.out, text)
		return
	}
	// Clear spinner line, print permanent text, spinner redraws on next tick.
	fmt.Fprintf(s.out, "\r\033[K%s\n", text)
	s.lines = append(s.lines, text)
}

func (s *spinner) run() {
	defer s.wg.Done()
	start := time.Now()
	frame := 0
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			// Clear the spinner line.
			fmt.Fprintf(s.out, "\r\033[K")
			return
		case <-ticker.C:
			elapsed := time.Since(start).Truncate(100 * time.Millisecond)
			s.mu.Lock()
			msg := s.message
			s.mu.Unlock()
			icon := s.theme.Muted.Render(spinnerFrames[frame%len(spinnerFrames)])
			styledMsg := s.theme.Muted.Render(msg)
			dur := s.theme.Muted.Render(formatDuration(elapsed))
			fmt.Fprintf(s.out, "\r\033[K%s %s %s", icon, styledMsg, dur)
			frame++
		}
	}
}

// formatDuration renders a duration as a compact human-readable string.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
}
