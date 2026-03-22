package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatFile_AlreadyFormatted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.cue")
	content := `package test

x: 1
y: "hello"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	changed, err := formatFile(path, false)
	require.NoError(t, err)
	assert.False(t, changed)

	// Content unchanged.
	got, _ := os.ReadFile(path)
	assert.Equal(t, content, string(got))
}

func TestFormatFile_NeedsFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messy.cue")

	// Badly formatted: inconsistent spacing, no trailing newline.
	content := `package test
x:     1
y:    "hello"
z:  true`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	changed, err := formatFile(path, false)
	require.NoError(t, err)
	assert.True(t, changed)

	got, _ := os.ReadFile(path)
	assert.Contains(t, string(got), "x:")
	// Should end with newline after formatting.
	assert.True(t, len(got) > 0 && got[len(got)-1] == '\n')
}

func TestFormatFile_CheckMode_NoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messy.cue")

	content := `package test
x:     1`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	changed, err := formatFile(path, true)
	require.NoError(t, err)
	assert.True(t, changed)

	// File should NOT be modified in check mode.
	got, _ := os.ReadFile(path)
	assert.Equal(t, content, string(got))
}

func TestFormatFile_InvalidCUE(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.cue")

	require.NoError(t, os.WriteFile(path, []byte(`{{{not valid cue`), 0o644))

	_, err := formatFile(path, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse error")
}

func TestCollectCUEFiles_Directory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.cue"), []byte("x: 1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.cue"), []byte("y: 2\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.json"), []byte("{}"), 0o644))

	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "d.cue"), []byte("z: 3\n"), 0o644))

	files, err := collectCUEFiles([]string{dir})
	require.NoError(t, err)
	assert.Len(t, files, 3) // a.cue, b.cue, sub/d.cue — c.json excluded
}

func TestCollectCUEFiles_ExplicitFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.cue")
	require.NoError(t, os.WriteFile(path, []byte("x: 1\n"), 0o644))

	files, err := collectCUEFiles([]string{path})
	require.NoError(t, err)
	assert.Len(t, files, 1)
	assert.Contains(t, files[0], "test.cue")
}

func TestCollectCUEFiles_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.cue")
	require.NoError(t, os.WriteFile(path, []byte("x: 1\n"), 0o644))

	files, err := collectCUEFiles([]string{path, path})
	require.NoError(t, err)
	assert.Len(t, files, 1)
}

func TestCollectCUEFiles_NoMatch(t *testing.T) {
	_, err := collectCUEFiles([]string{"/nonexistent/path/*.cue"})
	assert.Error(t, err)
}

func TestFmtCmd_Integration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "template.cue")
	content := `package test

variables: {
	name:     string
	env:    "dev" |   "prod"
}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	// Run the full command.
	root := NewRootCmd()
	root.SetArgs([]string{"fmt", path})
	err := root.Execute()
	require.NoError(t, err)

	got, _ := os.ReadFile(path)
	// Should be reformatted (alignment normalized).
	assert.NotEqual(t, content, string(got))
	assert.Contains(t, string(got), "variables:")
}

func TestFmtCmd_CheckMode_ExitError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "messy.cue")
	require.NoError(t, os.WriteFile(path, []byte(`package test
x:     1`), 0o644))

	root := NewRootCmd()
	root.SetArgs([]string{"fmt", "--check", path})
	err := root.Execute()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "need formatting")
}

func TestFmtCmd_CheckMode_Clean(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.cue")
	content := `package test

x: 1
y: "hello"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	root := NewRootCmd()
	root.SetArgs([]string{"fmt", "--check", path})
	err := root.Execute()
	assert.NoError(t, err)
}
