package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldUseStyles(t *testing.T) {
	tests := []struct {
		name      string
		format    OutputFormat
		plain     bool
		noColor   bool
		stdoutTTY bool
		want      bool
	}{
		{name: "styled tty output", format: OutputTable, stdoutTTY: true, want: true},
		{name: "plain flag disables styles", format: OutputTable, plain: true, stdoutTTY: true, want: false},
		{name: "json disables styles", format: OutputJSON, stdoutTTY: true, want: false},
		{name: "no color disables styles", format: OutputTable, noColor: true, stdoutTTY: true, want: false},
		{name: "non tty disables styles", format: OutputTable, stdoutTTY: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldUseStyles(tt.format, tt.plain, tt.noColor, tt.stdoutTTY))
		})
	}
}

func TestResolveResourceKey_GlobalScope(t *testing.T) {
	flags := &rootFlags{region: "us-east-1"}
	// S3Bucket is global — key is passed through unchanged.
	key := flags.resolveResourceKey("S3Bucket", "my-bucket")
	assert.Equal(t, "my-bucket", key)
}

func TestResolveResourceKey_CustomScope(t *testing.T) {
	flags := &rootFlags{region: "us-east-1"}
	// SecurityGroup is custom — key is passed through unchanged.
	key := flags.resolveResourceKey("SecurityGroup", "vpc-123~web-sg")
	assert.Equal(t, "vpc-123~web-sg", key)
}

func TestResolveResourceKey_RegionScope_PrependRegion(t *testing.T) {
	flags := &rootFlags{region: "us-west-2"}
	key := flags.resolveResourceKey("EC2Instance", "my-function")
	assert.Equal(t, "us-west-2~my-function", key)
}

func TestResolveResourceKey_RegionScope_NoRegionSet(t *testing.T) {
	flags := &rootFlags{region: ""}
	// No region configured — key returned as-is.
	key := flags.resolveResourceKey("Lambda", "my-function")
	assert.Equal(t, "my-function", key)
}

func TestResolveResourceKey_RegionScope_AlreadyQualified(t *testing.T) {
	flags := &rootFlags{region: "us-east-1"}
	// If user already included ~ in the key, don't prepend region.
	key := flags.resolveResourceKey("Lambda", "us-west-2~my-function")
	assert.Equal(t, "us-west-2~my-function", key)
}

func TestNewRootCmd_UsesOutputEnvDefault(t *testing.T) {
	t.Setenv(envOutput, "json")
	t.Setenv("HOME", t.TempDir())

	root := NewRootCmd()
	flag := root.PersistentFlags().Lookup("output")
	require.NotNil(t, flag)
	assert.Equal(t, "json", flag.DefValue)
	assert.Equal(t, "json", flag.Value.String())
}

func TestNewRootCmd_UsesWorkspaceEnvBeforeConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "from-config"}))
	t.Setenv(envWorkspace, "from-env")

	NewRootCmd()
	require.NotNil(t, currentRootFlags)
	assert.Equal(t, "from-env", currentRootFlags.activeWorkspace())
}

func TestNewRootCmd_UsesWorkspaceConfigFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "from-config"}))
	t.Setenv(envWorkspace, "")

	NewRootCmd()
	require.NotNil(t, currentRootFlags)
	assert.Equal(t, "from-config", currentRootFlags.activeWorkspace())
}

func TestLoadActiveWorkspace_TrimsConfigValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configPath := filepath.Join(home, ".praxis", "config.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0700))
	require.NoError(t, os.WriteFile(configPath, []byte(`{"activeWorkspace":"  dev  "}`), 0600))
	assert.Equal(t, "dev", loadActiveWorkspace())
}
