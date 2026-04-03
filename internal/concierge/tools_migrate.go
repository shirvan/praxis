package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

// registerMigrationTools adds tools for converting external IaC formats (Terraform,
// CloudFormation, Crossplane) to Praxis CUE templates. Migration is LLM-guided:
//
//  1. The tool inventories the source content (regex extraction of resource types)
//  2. Builds a migration context with resource type mappings and known gotchas
//  3. Sends the source + context to the LLM with a specialized migration prompt
//  4. Verifies the generated CUE via a dry-run plan
//  5. Retries up to 3 times if verification fails, feeding errors back to the LLM
//
// Also includes a standalone validateTemplate tool for checking CUE without migration.
func (r *ToolRegistry) registerMigrationTools() {
	r.Register(&ToolDef{
		Name:        "migrateTerraform",
		Description: "Convert Terraform HCL to Praxis CUE template. LLM-guided with verification.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string", "description": "Terraform HCL source content"},
			},
			"required": []string{"source"},
		},
		Execute: toolMigrateTerraform,
	})

	r.Register(&ToolDef{
		Name:        "migrateCloudFormation",
		Description: "Convert CloudFormation JSON/YAML to Praxis CUE template. LLM-guided with verification.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string", "description": "CloudFormation template content"},
			},
			"required": []string{"source"},
		},
		Execute: migrateCloudFormation,
	})

	r.Register(&ToolDef{
		Name:        "migrateCrossplane",
		Description: "Convert Crossplane YAML manifests to Praxis CUE template. LLM-guided with verification.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{"type": "string", "description": "Crossplane manifest content"},
			},
			"required": []string{"source"},
		},
		Execute: migrateCrossplane,
	})

	r.Register(&ToolDef{
		Name:        "validateTemplate",
		Description: "Validate a CUE template against Praxis schemas without applying",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"template": map[string]any{"type": "string", "description": "CUE template source"},
			},
			"required": []string{"template"},
		},
		Execute: toolValidateTemplate,
	})
}

// The migration tools delegate to a package-level migrator that must be set
// during initialization via SetMigrator. This avoids circular dependencies
// between the tool registry and the migrator.
var globalMigrator *TemplateMigrator
var globalConfig ConciergeConfiguration
var globalResolvedKey string

// SetMigratorContext sets the migration context for the current Ask invocation.
// This uses package-level state (not ideal, but avoids circular dependencies
// between ToolRegistry and TemplateMigrator). This is safe because Restate
// guarantees single-writer per Virtual Object key — only one Ask() runs at a
// time per session.
func SetMigratorContext(m *TemplateMigrator, cfg ConciergeConfiguration, key string) {
	globalMigrator = m
	globalConfig = cfg
	globalResolvedKey = key
}

// toolMigrateTerraform converts Terraform HCL to Praxis CUE.
func toolMigrateTerraform(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	return runMigration(ctx, "terraform", argsJSON)
}

// migrateCloudFormation converts CloudFormation JSON/YAML to Praxis CUE.
func migrateCloudFormation(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	return runMigration(ctx, "cloudformation", argsJSON)
}

// migrateCrossplane converts Crossplane YAML manifests to Praxis CUE.
func migrateCrossplane(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	return runMigration(ctx, "crossplane", argsJSON)
}

// runMigration is the shared implementation for all migration tools. It parses
// the source argument and delegates to the TemplateMigrator's full pipeline.
func runMigration(ctx restate.Context, format, argsJSON string) (string, error) {
	var args struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Source == "" {
		return "Error: source is required", nil
	}

	if globalMigrator == nil {
		return "Error: migration is not available (migrator not initialized)", nil
	}

	result, err := globalMigrator.Migrate(ctx, globalConfig, globalResolvedKey, format, args.Source)
	if err != nil {
		return fmt.Sprintf("Migration failed: %s", err.Error()), nil
	}

	return result, nil
}

// toolValidateTemplate validates a CUE template against Praxis schemas without
// applying it. Runs a dry-run plan and reports parse, schema, and plan results.
func toolValidateTemplate(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Template string `json:"template"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.Template == "" {
		return "Error: template is required", nil
	}

	result, err := VerifyMigrationOutput(ctx, args.Template)
	if err != nil {
		return fmt.Sprintf("Validation error: %s", err.Error()), nil
	}

	if result.ParseOK && result.SchemaOK && result.PlanOK {
		return FormatVerificationResult(result), nil
	}

	return fmt.Sprintf("Validation failed:\n%s", FormatVerificationErrors(result)), nil
}
