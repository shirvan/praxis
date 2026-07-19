package drivers

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/authservice"
)

type testAPIError struct{ code string }

func (e *testAPIError) Error() string                 { return e.code + ": test error" }
func (e *testAPIError) ErrorCode() string             { return e.code }
func (e *testAPIError) ErrorMessage() string          { return "test error" }
func (e *testAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestClassifyAWS(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		assert.NoError(t, ClassifyAWS(nil, PassThroughError))
	})

	t.Run("already terminal is preserved without invoking resource classifier", func(t *testing.T) {
		terminal := restate.TerminalError(fmt.Errorf("already classified"), 422)
		called := false
		got := ClassifyAWS(terminal, func(err error) error {
			called = true
			return err
		})
		assert.Same(t, terminal, got)
		assert.False(t, called)
	})

	t.Run("throttling remains retryable", func(t *testing.T) {
		throttled := &testAPIError{code: "ThrottlingException"}
		called := false
		got := ClassifyAWS(throttled, func(error) error {
			called = true
			return restate.TerminalError(fmt.Errorf("wrong"), 409)
		})
		assert.Same(t, throttled, got)
		assert.False(t, restate.IsTerminalError(got))
		assert.False(t, called)
	})

	for _, tt := range []struct {
		name string
		code string
		want restate.Code
	}{
		{name: "access denied", code: "AccessDeniedException", want: 403},
		{name: "expired credentials", code: "ExpiredTokenException", want: 401},
		{name: "common validation", code: "ValidationException", want: 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyAWS(&testAPIError{code: tt.code}, PassThroughError)
			require.True(t, restate.IsTerminalError(got))
			assert.Equal(t, tt.want, restate.ErrorCode(got))
		})
	}

	t.Run("hard quota is consistently unavailable", func(t *testing.T) {
		got := ClassifyAWS(&testAPIError{code: "VpcLimitExceeded"}, PassThroughError)
		require.True(t, restate.IsTerminalError(got))
		assert.Equal(t, restate.Code(503), restate.ErrorCode(got))
	})

	t.Run("resource-specific classification takes precedence", func(t *testing.T) {
		conflict := &testAPIError{code: "ResourceInUseException"}
		got := ClassifyAWS(conflict, func(err error) error {
			return restate.TerminalError(err, 409)
		})
		require.True(t, restate.IsTerminalError(got))
		assert.Equal(t, restate.Code(409), restate.ErrorCode(got))
	})

	t.Run("unknown provider failures remain retryable", func(t *testing.T) {
		transient := fmt.Errorf("connection reset")
		got := ClassifyAWS(transient, PassThroughError)
		assert.Same(t, transient, got)
		assert.False(t, restate.IsTerminalError(got))
	})
}

func TestClassifyCredentialError(t *testing.T) {
	t.Run("auth status is preserved", func(t *testing.T) {
		err := fmt.Errorf("resolve account: %w", &authservice.AuthError{
			Code: authservice.ErrCodeUnknownAccount, Account: "missing", Message: "unknown account",
		})
		got := ClassifyCredentialError(err)
		require.True(t, restate.IsTerminalError(got))
		assert.Equal(t, restate.Code(404), restate.ErrorCode(got))
	})

	t.Run("retryable credential resolution stays bare", func(t *testing.T) {
		err := &authservice.AuthError{Code: authservice.ErrCodeConfigLoad, Message: "temporary config load failure"}
		got := ClassifyCredentialError(err)
		assert.Same(t, err, got)
		assert.False(t, restate.IsTerminalError(got))
	})

	t.Run("already terminal is preserved", func(t *testing.T) {
		terminal := restate.TerminalError(fmt.Errorf("classified"), 418)
		assert.Same(t, terminal, ClassifyCredentialError(terminal))
	})

	t.Run("local configuration failure remains terminal", func(t *testing.T) {
		got := ClassifyCredentialError(fmt.Errorf("driver is not configured"))
		require.True(t, restate.IsTerminalError(got))
		assert.Equal(t, restate.Code(400), restate.ErrorCode(got))
	})
}

// This source-level guard makes the explicit-classifier contract apply to all
// drivers, including code paths that are not exercised by a focused unit test.
func TestEveryRunAWSCallHasExplicitClassifier(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	driverRoot := filepath.Dir(filename)
	fset := token.NewFileSet()

	err := filepath.WalkDir(driverRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || filepath.Base(path) == filepath.Base(filename) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, isCall := node.(*ast.CallExpr)
			if !isCall || !isRunAWSCall(call.Fun) || len(call.Args) != 3 {
				return true
			}
			if ident, isIdent := call.Args[2].(*ast.Ident); isIdent && ident.Name == "nil" {
				t.Errorf("%s: RunAWS must have an explicit non-nil classifier", fset.Position(call.Pos()))
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}

func isRunAWSCall(expr ast.Expr) bool {
	switch fun := expr.(type) {
	case *ast.Ident:
		return fun.Name == "RunAWS"
	case *ast.SelectorExpr:
		return fun.Sel.Name == "RunAWS"
	case *ast.IndexExpr:
		return isRunAWSCall(fun.X)
	case *ast.IndexListExpr:
		return isRunAWSCall(fun.X)
	default:
		return false
	}
}
