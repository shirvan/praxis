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
	parts := strings.SplitN(msg, "\n  hint: ", 2)
	fmt.Fprintf(os.Stderr, "\n  ✗ Authentication Error\n")
	fmt.Fprintf(os.Stderr, "    %s\n", parts[0])
	if len(parts) == 2 {
		fmt.Fprintf(os.Stderr, "\n    Hint: %s\n", parts[1])
	}
}
