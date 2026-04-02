package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

// MigrationContext is the structured context given to the LLM alongside the source files.
type MigrationContext struct {
	Inventory       MigrationInventory `json:"inventory"`
	SchemaSnippets  map[string]string  `json:"schemaSnippets"`
	ExampleTemplate string             `json:"exampleTemplate"`
	ReferenceRules  string             `json:"referenceRules"`
	KnownGotchas    []string           `json:"knownGotchas"`
}

// TemplateMigrator orchestrates the migration pipeline.
type TemplateMigrator struct {
	llm   *ProviderRouter
	tools *ToolRegistry
}

// NewTemplateMigrator creates a new migrator.
func NewTemplateMigrator(llm *ProviderRouter, tools *ToolRegistry) *TemplateMigrator {
	return &TemplateMigrator{llm: llm, tools: tools}
}

// Migrate runs the full migration pipeline: inventory → context → LLM → verify → retry.
func (m *TemplateMigrator) Migrate(
	ctx restate.Context,
	config ConciergeConfiguration,
	resolvedKey string,
	format string,
	source string,
) (string, error) {
	// Step 1: Inventory
	inv := BuildInventory(format, source)

	// Step 2: Build context (used for gotcha hints in the prompt)
	_ = MigrationContext{
		Inventory:      inv,
		SchemaSnippets: make(map[string]string),
		ReferenceRules: "Cross-resource references: ${resources.<name>.outputs.<field>}\nVariables: \\(variables.<name>)",
		KnownGotchas:   knownGotchas(format),
	}

	// Step 3: Build migration prompt
	invJSON, _ := json.MarshalIndent(inv, "", "  ")
	prompt := fmt.Sprintf(migrationPromptTemplate,
		format,
		FormatMappingTable(),
		"", // example template placeholder
		string(invJSON),
		source,
	)

	// Step 4: Call LLM to generate CUE
	provider := m.llm.ForConfig(config, resolvedKey)

	var lastCUE string
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		messages := []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		}

		if attempt > 0 && lastCUE != "" {
			messages = append(messages, Message{
				Role:    "user",
				Content: fmt.Sprintf("The previous CUE output had errors. Please fix them and regenerate.\n\nPrevious output:\n```\n%s\n```", lastCUE),
			})
		}

		llmResp, err := restate.Run(ctx, func(rc restate.RunContext) (LLMResponse, error) {
			return provider.ChatCompletion(rc, ChatRequest{
				Messages:    messages,
				Temperature: config.Temperature,
			})
		})
		if err != nil {
			return "", fmt.Errorf("LLM call failed: %w", err)
		}

		lastCUE = extractCUEBlock(llmResp.Content)
		if lastCUE == "" {
			lastCUE = llmResp.Content
		}

		// Step 5: Verify
		verification, err := VerifyMigrationOutput(ctx, lastCUE)
		if err != nil {
			return "", err
		}

		if verification.ParseOK && verification.SchemaOK && verification.PlanOK {
			result := fmt.Sprintf("Migration successful!\n\n```cue\n%s\n```\n\n%s", lastCUE, FormatVerificationResult(verification))
			if len(inv.UnmappedTypes) > 0 {
				result += "\n\nWarning: The following source resource types have no Praxis equivalent and were skipped or approximated:\n"
				for _, t := range inv.UnmappedTypes {
					result += fmt.Sprintf("  - %s\n", t)
				}
			}
			result += "\n\nReview the generated template before applying."
			return result, nil
		}

		// Feed errors back for retry
		prompt = fmt.Sprintf("Fix the following errors in the generated CUE template:\n\n%s\n\nOriginal CUE:\n```\n%s\n```", FormatVerificationErrors(verification), lastCUE)
	}

	// Return best attempt with errors
	return fmt.Sprintf("Migration completed with errors after %d attempts. Best attempt:\n\n```cue\n%s\n```\n\nPlease review and fix manually.", maxRetries, lastCUE), nil
}

// extractCUEBlock extracts a CUE code block from LLM output.
func extractCUEBlock(content string) string {
	// Look for ```cue ... ``` blocks.
	start := -1
	end := -1
	markers := []string{"```cue\n", "```cue\r\n", "```\n"}
	for _, marker := range markers {
		idx := indexOf(content, marker)
		if idx >= 0 {
			start = idx + len(marker)
			break
		}
	}
	if start < 0 {
		return ""
	}
	rest := content[start:]
	end = indexOf(rest, "```")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func knownGotchas(format string) []string {
	switch format {
	case "terraform":
		return []string{
			"Terraform modules are not directly translatable — inline the resources.",
			"for_each and count loops must be expanded into individual resources.",
			"Dynamic blocks should be converted to static CUE definitions.",
			"Terraform providers are not needed — Praxis handles auth via accounts.",
			"Local values (locals {}) should become CUE variables.",
		}
	case "cloudformation":
		return []string{
			"CloudFormation intrinsic functions (Fn::Ref, Fn::Join) must be converted to CUE expressions.",
			"Conditions are not directly supported — use CUE conditionals.",
			"DependsOn is handled automatically via output references.",
			"CloudFormation parameters become CUE variables.",
		}
	case "crossplane":
		return []string{
			"Crossplane compositions should be flattened to individual resources.",
			"ProviderConfigRef is not needed — Praxis handles auth via accounts.",
			"Crossplane patches become direct CUE field assignments.",
		}
	default:
		return nil
	}
}
