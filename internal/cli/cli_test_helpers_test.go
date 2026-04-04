package cli

import (
	"bytes"
	"testing"
)

// executeCmd runs a cobra command tree with the given args, capturing stdout
// and stderr. Returns the combined output and any error.
func executeCmd(t *testing.T, args []string, endpoint string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()

	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)

	// Inject the fake endpoint.
	allArgs := append([]string{"--endpoint", endpoint, "--plain"}, args...)
	root.SetArgs(allArgs)

	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}
