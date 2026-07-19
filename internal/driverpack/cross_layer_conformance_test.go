package driverpack_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/schemas"
)

var (
	schemaKindPattern       = regexp.MustCompile(`(?m)^\s*kind:\s*"([^"]+)"`)
	schemaVersionPattern    = regexp.MustCompile(`(?m)^\s*apiVersion:\s*"praxis\.io/alpha"`)
	schemaOutputsPattern    = regexp.MustCompile(`(?m)^\s*outputs\?:\s*\{`)
	driverServicePattern    = regexp.MustCompile(`(?m)^\s*const\s+ServiceName\s*=\s*"([^"]+)"`)
	structuredMetadataField = regexp.MustCompile(`(?m)^\s*metadata:\s*\{`)
)

// TestProductionResourceVerticalSlicesStayComplete prevents one layer from
// quietly drifting away from the supported resource inventory. A built-in
// resource must have one generic driver package, one production binding, one
// Core adapter, one exact-alpha schema with outputs, and provider integration
// coverage importing that driver package.
func TestProductionResourceVerticalSlicesStayComplete(t *testing.T) {
	root := repositoryRoot(t)
	driverImports := productionDriverImports(t, root)
	schemaKinds := productionSchemaKinds(t)
	integrationImports, integrationFiles := integrationDriverImports(t, root)

	expectedKinds := sortedSetKeys(expectedGenericDrivers)
	assert.Equal(t, expectedKinds, sortedSetKeys(schemaKinds), "schema inventory must match production drivers")
	assert.Equal(t, expectedKinds, sortedSetKeys(driverImports), "generic driver packages must match production bindings")
	require.Equal(t, len(expectedKinds), integrationFiles, "expected one integration driver file per production resource")

	for kind, importPath := range driverImports {
		t.Run(kind+"/integration", func(t *testing.T) {
			require.Containsf(t, integrationImports, importPath, "integration inventory does not import %s", importPath)
		})
	}
}

func productionSchemaKinds(t *testing.T) map[string]struct{} {
	t.Helper()
	kinds := map[string]struct{}{}
	require.NoError(t, fs.WalkDir(schemas.FS, "aws", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".cue") {
			return nil
		}
		contents, readErr := fs.ReadFile(schemas.FS, path)
		if readErr != nil {
			return readErr
		}
		match := schemaKindPattern.FindSubmatch(contents)
		require.Lenf(t, match, 2, "%s must declare exactly one resource kind", path)
		kind := string(match[1])
		_, duplicate := kinds[kind]
		require.Falsef(t, duplicate, "schema kind %q is declared more than once", kind)
		require.Regexp(t, schemaVersionPattern, string(contents), "%s must use the exact alpha API version", path)
		require.Regexp(t, structuredMetadataField, string(contents), "%s must use the standard metadata block", path)
		require.Regexp(t, schemaOutputsPattern, string(contents), "%s must declare its driver outputs", path)
		kinds[kind] = struct{}{}
		return nil
	}))
	return kinds
}

func productionDriverImports(t *testing.T, root string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "internal", "drivers"))
	require.NoError(t, err)
	result := map[string]string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, "internal", "drivers", entry.Name())
		if _, statErr := os.Stat(filepath.Join(dir, "generic.go")); statErr != nil {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(dir, "types.go"))
		require.NoErrorf(t, readErr, "read %s types", entry.Name())
		match := driverServicePattern.FindSubmatch(contents)
		require.Lenf(t, match, 2, "%s must declare ServiceName in types.go", entry.Name())
		serviceName := string(match[1])
		_, duplicate := result[serviceName]
		require.Falsef(t, duplicate, "driver service %q is declared more than once", serviceName)
		result[serviceName] = "github.com/shirvan/praxis/internal/drivers/" + entry.Name()
	}
	return result
}

func integrationDriverImports(t *testing.T, root string) (map[string]struct{}, int) {
	t.Helper()
	dir := filepath.Join(root, "tests", "integration")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	imports := map[string]struct{}{}
	files := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_driver_test.go") {
			continue
		}
		files++
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
		require.NoErrorf(t, parseErr, "parse %s imports", entry.Name())
		for _, imported := range parsed.Imports {
			path, unquoteErr := strconv.Unquote(imported.Path.Value)
			require.NoError(t, unquoteErr)
			if strings.HasPrefix(path, "github.com/shirvan/praxis/internal/drivers/") {
				imports[path] = struct{}{}
			}
		}
	}
	return imports, files
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func sortedSetKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
