package cli

import (
	"fmt"
	"os"
	"strings"
)

// IsAuthErrorMessage detects auth errors from their serialized string form.
// After Restate RPC serialization, typed *AuthError becomes a string with
// the [AUTH_CODE] prefix.
func IsAuthErrorMessage(msg string) bool {
	return strings.Contains(msg, "[AUTH_")
}

// FormatAuthError renders an auth error with visual emphasis.
// Extracts the hint line (if present) and prints it separately.
func FormatAuthError(msg string) {
	renderer := defaultRenderer()
	parts := strings.SplitN(msg, "\n  hint: ", 2)
	header := "  ✗ Authentication Error"
	if renderer.styles {
		header = renderer.theme.Error.Render(header)
	}
	fmt.Fprintf(os.Stderr, "\n%s\n", header)
	fmt.Fprintf(os.Stderr, "    %s\n", parts[0])
	if len(parts) == 2 {
		hint := "Hint:"
		if renderer.styles {
			hint = renderer.theme.Warning.Render(hint)
		}
		fmt.Fprintf(os.Stderr, "\n    %s %s\n", hint, parts[1])
	}
}

func PrintError(msg string) {
	renderer := defaultRenderer()
	prefix := "✗"
	if renderer.styles {
		prefix = renderer.theme.Error.Render(prefix)
	}
	fmt.Fprintf(os.Stderr, "%s %s\n", prefix, msg)
}
