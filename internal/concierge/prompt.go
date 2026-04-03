package concierge

import _ "embed"

// systemPrompt is the system-level instruction embedded from prompts/system.txt.
// It defines the Concierge's persona, capabilities, and behavioral guidelines.
// This prompt is always the first message in every conversation and is preserved
// across history trimming. It instructs the LLM on:
//   - Available tools and when to use them
//   - Praxis concepts (templates, deployments, resources, workspaces)
//   - Approval flow for write operations
//   - CUE template syntax and conventions
//   - How to handle errors and suggest fixes
//
//go:embed prompts/system.txt
var systemPrompt string
