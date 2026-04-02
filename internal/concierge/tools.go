package concierge

import restate "github.com/restatedev/sdk-go"

// ToolDef describes a tool available to the concierge.
type ToolDef struct {
	Name             string
	Description      string
	Parameters       map[string]any
	RequiresApproval bool
	Execute          ToolExecuteFunc
	DescribeAction   func(argsJSON string) string
}

// ToolExecuteFunc is the function signature for tool execution.
type ToolExecuteFunc func(ctx restate.Context, args string, session SessionState) (string, error)

// ToolRegistry holds all registered tools.
type ToolRegistry struct {
	tools map[string]*ToolDef
}

// NewToolRegistry creates and populates the tool registry.
func NewToolRegistry() *ToolRegistry {
	r := &ToolRegistry{tools: make(map[string]*ToolDef)}
	r.registerReadTools()
	r.registerWriteTools()
	r.registerExplainTools()
	r.registerMigrationTools()
	return r
}

// Register adds a tool to the registry.
func (r *ToolRegistry) Register(t *ToolDef) {
	r.tools[t.Name] = t
}

// Get returns a tool by name, or nil if not found.
func (r *ToolRegistry) Get(name string) *ToolDef {
	return r.tools[name]
}

// Definitions returns the tool schemas for the LLM.
func (r *ToolRegistry) Definitions() []ToolSchema {
	out := make([]ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return out
}

// Names returns all registered tool names.
func (r *ToolRegistry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	return out
}
