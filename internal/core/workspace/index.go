package workspace

import (
	"sort"

	restate "github.com/restatedev/sdk-go"
)

const (
	WorkspaceIndexServiceName = "WorkspaceIndex"
	WorkspaceIndexGlobalKey   = "global"
)

// WorkspaceIndex is a single-key Virtual Object that maintains the set
// of known workspace names. This follows the same pattern as DeploymentIndex.
type WorkspaceIndex struct{}

func (WorkspaceIndex) ServiceName() string { return WorkspaceIndexServiceName }

// Register adds a workspace name to the global set.
func (WorkspaceIndex) Register(ctx restate.ObjectContext, name string) error {
	names, _ := restate.Get[map[string]bool](ctx, "names")
	if names == nil {
		names = make(map[string]bool)
	}
	names[name] = true
	restate.Set(ctx, "names", names)
	return nil
}

// Deregister removes a workspace name from the global set.
func (WorkspaceIndex) Deregister(ctx restate.ObjectContext, name string) error {
	names, _ := restate.Get[map[string]bool](ctx, "names")
	if names == nil {
		return nil
	}
	delete(names, name)
	restate.Set(ctx, "names", names)
	return nil
}

// List returns all workspace names in sorted order.
func (WorkspaceIndex) List(ctx restate.ObjectSharedContext, _ restate.Void) ([]string, error) {
	names, _ := restate.Get[map[string]bool](ctx, "names")
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}
