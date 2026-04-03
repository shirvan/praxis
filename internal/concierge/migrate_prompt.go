package concierge

import _ "embed"

// migrationPromptTemplate is the specialized prompt template embedded from
// prompts/migration.txt. It contains format-string placeholders (%s) that are
// filled in by TemplateMigrator.Migrate() with:
//  1. Source format name ("terraform", "cloudformation", "crossplane")
//  2. Resource type mapping table (source type → Praxis kind)
//  3. Example template (placeholder for future use)
//  4. Source inventory JSON (extracted resource types and counts)
//  5. Raw source content to convert
//
// The prompt instructs the LLM to generate valid Praxis CUE that uses the correct
// resource kinds, cross-resource reference syntax, and variable patterns.
//
//go:embed prompts/migration.txt
var migrationPromptTemplate string
