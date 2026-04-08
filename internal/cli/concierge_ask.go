// concierge_ask.go contains the shared logic for sending a prompt to the
// concierge with a live-updating spinner and real-time tool-call progress.
//
// Both the root shorthand (praxis "...") and the explicit subcommand
// (praxis concierge ask "...") delegate to runConciergeAsk so that the
// UX is consistent regardless of entry point.
package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// envSession is the environment variable that overrides the default
	// session ID for concierge commands. Setting this allows users to
	// resume a session without passing --session on every invocation.
	envSession = "PRAXIS_SESSION"
)

// conciergeAskOpts bundles the parameters for a concierge ask invocation.
type conciergeAskOpts struct {
	Client     *Client
	Renderer   *Renderer
	Session    string
	NewSession bool
	Prompt     string
	Account    string
	Workspace  string
	JSON       bool
}

// runConciergeAsk sends a prompt to the concierge, polls for live progress,
// and renders the response. It returns the raw response for callers that need
// to inspect it (e.g. JSON encoding).
func runConciergeAsk(ctx context.Context, opts conciergeAskOpts) (*conciergeAskResponse, error) {
	newlyCreated := false
	switch {
	case opts.NewSession:
		opts.Session = generateSessionID()
		newlyCreated = true
	case opts.Session != "":
		// Explicit --session flag; use as-is.
	default:
		opts.Session = resolveSessionID()
	}
	// Persist the active session so the next invocation reuses it automatically.
	saveSessionID(opts.Session)

	// Inform the user which session is active.
	if !opts.JSON {
		r := opts.Renderer
		if newlyCreated {
			_, _ = fmt.Fprintf(r.errOut, "New session: %s\n", opts.Session)
		} else {
			_, _ = fmt.Fprintf(r.errOut, "Session: %s\n", opts.Session)
		}
	}

	req := conciergeAskRequest{
		Prompt:    opts.Prompt,
		Account:   opts.Account,
		Workspace: opts.Workspace,
		Source:    "cli",
	}

	r := opts.Renderer

	// Start the spinner on stderr.
	sp := newSpinner(r.errOut, r.theme, r.styles, "Thinking")
	sp.Start()

	// Fire the Ask request in the background.
	type askResult struct {
		resp *conciergeAskResponse
		err  error
	}
	done := make(chan askResult, 1)
	go func() {
		resp, err := opts.Client.ConciergeAsk(ctx, opts.Session, req)
		done <- askResult{resp, err}
	}()

	// Poll ConciergeProgress for live tool-call updates while Ask is running.
	rendered := 0 // number of progress entries already shown
	pollTicker := time.NewTicker(300 * time.Millisecond)
	defer pollTicker.Stop()

	var result askResult
poll:
	for {
		select {
		case result = <-done:
			break poll
		case <-pollTicker.C:
			progress, err := opts.Client.ConciergeGetProgress(ctx, opts.Session)
			if err != nil || progress == nil {
				continue
			}
			for i := rendered; i < len(progress.Entries); i++ {
				entry := progress.Entries[i]
				switch entry.Status {
				case "thinking":
					sp.SetMessage("Thinking")
				case "running":
					sp.PrintLine(r.renderProgressEntry(entry))
					sp.SetMessage("Running " + entry.Name)
				default: // "ok", "error"
					sp.PrintLine(r.renderProgressEntry(entry))
					sp.SetMessage("Thinking")
				}
			}
			rendered = len(progress.Entries)
		}
	}

	sp.Stop()

	if result.err != nil {
		return nil, result.err
	}

	if !opts.JSON {
		r.renderConciergeResponse(result.resp, rendered > 0)
	}

	return result.resp, nil
}

// generateSessionID returns a short random hex string suitable for session keys.
func generateSessionID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixMilli())
	}
	return hex.EncodeToString(b)
}

// resolveSessionID determines the session ID to use. Resolution order:
//  1. PRAXIS_SESSION environment variable
//  2. State file (~/.praxis/session)
//  3. Generate a new random ID
func resolveSessionID() string {
	if v := os.Getenv(envSession); v != "" {
		return v
	}
	if v, err := os.ReadFile(sessionStatePath()); err == nil {
		if s := strings.TrimSpace(string(v)); s != "" {
			return s
		}
	}
	return generateSessionID()
}

// saveSessionID writes the session ID to the state file so the next
// invocation in any shell reuses it automatically.
func saveSessionID(id string) {
	p := sessionStatePath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, []byte(id), 0o600)
}

// sessionStatePath returns ~/.praxis/session.
func sessionStatePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".praxis", "session")
}
