package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

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
func SetMigratorContext(m *TemplateMigrator, cfg ConciergeConfiguration, key string) {
	globalMigrator = m
	globalConfig = cfg
	globalResolvedKey = key
}

func toolMigrateTerraform(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	return runMigration(ctx, "terraform", argsJSON)
}

func migrateCloudFormation(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	return runMigration(ctx, "cloudformation", argsJSON)
}

func migrateCrossplane(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	return runMigration(ctx, "crossplane", argsJSON)
}

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
