package template

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateBytes_SaaSPlatform(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas", "aws"))
	require.NoError(t, err)
	eng := NewEngine(absSchemaDir)

	tmplPath, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "stacks", "saas-platform.cue"))
	require.NoError(t, err)
	content, err := os.ReadFile(tmplPath)
	require.NoError(t, err)

	vars := map[string]any{
		"name": "acme", "environment": "prod",
		"domainName": "acme.example.com", "hostedZoneId": "Z0123456789ABCDEF",
		"availabilityZones": []string{"us-east-1a", "us-east-1b"},
		"storageBuckets":    []string{"assets", "uploads", "backups"},
	}

	specs, err := eng.EvaluateBytes(content, vars)
	require.NoError(t, err, "EvaluateBytes should succeed")
	t.Logf("got %d resources", len(specs))
}

// TestEvaluateBytes_SaaSPlatform_FullSchemaDir uses the top-level schemas/ directory
// (matching Docker's PRAXIS_SCHEMA_DIR=/schemas) to reproduce the Docker runtime path.
func TestEvaluateBytes_SaaSPlatform_FullSchemaDir(t *testing.T) {
	absSchemaDir, err := filepath.Abs(filepath.Join("..", "..", "..", "schemas"))
	require.NoError(t, err)
	eng := NewEngine(absSchemaDir)

	tmplPath, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "stacks", "saas-platform.cue"))
	require.NoError(t, err)
	content, err := os.ReadFile(tmplPath)
	require.NoError(t, err)

	vars := map[string]any{
		"name": "acme", "environment": "prod",
		"domainName": "acme.example.com", "hostedZoneId": "Z0123456789ABCDEF",
		"availabilityZones": []string{"us-east-1a", "us-east-1b"},
		"storageBuckets":    []string{"assets", "uploads", "backups"},
	}

	specs, err := eng.EvaluateBytes(content, vars)
	require.NoError(t, err, "EvaluateBytes (full schemaDir) should succeed")
	t.Logf("got %d resources", len(specs))
}
