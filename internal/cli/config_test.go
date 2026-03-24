package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveAndLoadCLIConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	cfg := CLIConfig{ActiveWorkspace: "dev", Endpoint: "http://localhost:9090"}
	require.NoError(t, SaveCLIConfig(cfg))

	loaded := LoadCLIConfig()
	assert.Equal(t, "dev", loaded.ActiveWorkspace)
	assert.Equal(t, "http://localhost:9090", loaded.Endpoint)
}

func TestLoadCLIConfig_Missing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := LoadCLIConfig()
	assert.Equal(t, "", cfg.ActiveWorkspace)
	assert.Equal(t, "", cfg.Endpoint)
}

func TestSaveCLIConfig_CreatesDirectoryAndPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	require.NoError(t, SaveCLIConfig(CLIConfig{ActiveWorkspace: "prod"}))

	dirInfo, err := os.Stat(filepath.Join(tmpDir, ".praxis"))
	require.NoError(t, err)
	assert.True(t, dirInfo.IsDir())

	fileInfo, err := os.Stat(filepath.Join(tmpDir, ".praxis", "config.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fileInfo.Mode().Perm())
}

func TestLoadCLIConfig_CorruptFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	dir := filepath.Join(tmpDir, ".praxis")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json"), 0600))

	cfg := LoadCLIConfig()
	assert.Equal(t, "", cfg.ActiveWorkspace)
}
