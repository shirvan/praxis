package concierge

import restate "github.com/restatedev/sdk-go"

// ToolDef describes a tool available to the Concierge LLM. Tools are the mechanism
// by which the LLM interacts with the Praxis platform. The LLM sees the tool's Name,
// Description, and Parameters schema, and can choose to invoke any tool during the
// conversation loop.
//
// Tools are divided into categories:
//   - Read tools (tools_read.go):     Query state — no side effects, no approval needed
//   - Write tools (tools_write.go):   Mutate infrastructure — RequiresApproval=true
//   - Explain tools (tools_explain.go): Provide guidance — no side effects
//   - Migration tools (tools_migrate.go): Convert Terraform/CloudFormation/Crossplane to CUE
//
// When RequiresApproval is true, the session creates a Restate awakeable and suspends
// until a human approves or rejects the action via ApprovalRelay.
type ToolDef struct {
	Name             string                       // Tool name as seen by the LLM (e.g., "applyTemplate")
	Description      string                       // Natural language description for the LLM's tool selection
	Parameters       map[string]any               // JSON Schema defining the tool's input parameters
	RequiresApproval bool                         // If true, execution is gated by human approval via awakeable
	Execute          ToolExecuteFunc              // The function that performs the actual work
	DescribeAction   func(argsJSON string) string // Optional: generates a human-readable description for approval prompts
}

// ToolExecuteFunc is the function signature for tool execution. All tools receive:
//   - ctx: Restate context for making durable cross-service calls to Praxis services
//   - args: JSON-encoded arguments from the LLM, matching the tool's Parameters schema
//   - session: Current session state (provides account/workspace context)
//
// Returns the tool result as a string (added to conversation as a "tool" role message).
type ToolExecuteFunc func(ctx restate.Context, args string, session SessionState) (string, error)

// ToolRegistry holds all registered tools, indexed by name for O(1) lookup.
// The registry is populated at startup and is immutable during operation.
type ToolRegistry struct {
	tools map[string]*ToolDef // Map of tool name → definition
}

// NewToolRegistry creates and populates the tool registry with all tool categories.
// Called once during ConciergeSession construction.
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

// Definitions returns the tool schemas for the LLM. These are included in every
// ChatRequest so the LLM knows what tools are available and their parameter schemas.
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
