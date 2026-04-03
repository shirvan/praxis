// index.go implements the WorkspaceIndex Restate Virtual Object.
//
// WorkspaceIndex is a single-key ("global") Virtual Object that maintains
// the set of known workspace names. It follows the same singleton-index
// pattern used by DeploymentIndex: Register/Deregister mutate the set,
// and List returns all names sorted.
package workspace

import (
	"sort"

	restate "github.com/restatedev/sdk-go"
)

// WorkspaceIndexServiceName is the Restate service name for the workspace index.
const (
	WorkspaceIndexServiceName = "WorkspaceIndex"
	// WorkspaceIndexGlobalKey is the single Virtual Object key for the index.
	WorkspaceIndexGlobalKey = "global"
)

// WorkspaceIndex is a single-key Virtual Object that maintains the set
// of known workspace names. This follows the same pattern as DeploymentIndex.
type WorkspaceIndex struct{}

// ServiceName returns the Restate service registration name.
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
