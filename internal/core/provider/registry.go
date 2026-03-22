// Package provider centralizes the typed bridge between generic orchestration
// code and concrete resource drivers.
//
// The orchestrator and command service operate on generic JSON resource
// documents and generic output maps. The drivers do not: they expose strongly
// typed Go structs. This package is the one place where that type branching is
// allowed to exist.
package provider

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/praxiscloud/praxis/internal/core/auth"
	"github.com/praxiscloud/praxis/pkg/types"
)

// Adapter bridges a template resource kind to an existing typed driver.
type Adapter interface {
	// Kind returns the template kind handled by the adapter.
	Kind() string

	// ServiceName returns the Restate service name for the target driver.
	ServiceName() string

	// Scope returns the key scope that governs how user input is assembled into
	// a canonical resource key. The CLI uses this to decide whether to prepend
	// region, prompt for extra parts, or accept the name as-is.
	Scope() KeyScope

	// BuildKey derives the canonical Restate Virtual Object key from the rendered
	// resource document.
	BuildKey(resourceDoc json.RawMessage) (string, error)

	// DecodeSpec extracts the rendered document's spec and converts it into the
	// concrete Go input type expected by the driver.
	DecodeSpec(resourceDoc json.RawMessage) (any, error)

	// Provision submits a typed Provision call from inside a Restate handler and
	// returns a handle that can be waited on with restate.Wait / restate.WaitFirst.
	//
	// This is intentionally handler-native rather than ingress-client based.
	// Workflows and command handlers already run inside Restate, so they should
	// use durable service-to-service calls instead of making external HTTP calls
	// back through ingress.
	Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error)

	// Delete submits a typed Delete call from inside a Restate handler and returns
	// a handle that can be waited on alongside any other Restate futures.
	Delete(ctx restate.Context, key string) (DeleteInvocation, error)

	// NormalizeOutputs converts a concrete driver output struct into the generic
	// output map used by deployment state, the CLI, and expression hydration.
	NormalizeOutputs(raw any) (map[string]any, error)

	// Plan compares the desired driver spec with current provider state and
	// reports the high-level operation plus any field-level differences.
	Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error)

	// BuildImportKey derives the canonical Restate object key for an import flow.
	BuildImportKey(region, resourceID string) (string, error)

	// Import adopts an existing provider resource via the typed driver and
	// converts the result into the generic command-service view.
	Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error)
}

// AsyncInvocation is the common subset shared by provision and delete calls.
//
// The workflow keeps these handles in maps keyed by resource name, then waits on
// the exposed Future using Restate's native waiting primitives. When the future
// completes, the workflow asks the handle to decode either outputs or a void
// success response.
type AsyncInvocation interface {
	// ID is the durable Restate invocation ID. Persisting it later would allow
	// Core to re-attach across handler boundaries.
	ID() string

	// Future is the Restate future used by Wait / WaitFirst.
	Future() restate.Future
}

// ProvisionInvocation represents an in-flight Provision call whose success value
// is a normalized output map.
type ProvisionInvocation interface {
	AsyncInvocation

	// Outputs waits for the underlying driver call to complete, decodes the typed
	// driver output, and converts it into the generic deployment-facing map.
	Outputs() (map[string]any, error)
}

// DeleteInvocation represents an in-flight Delete call.
type DeleteInvocation interface {
	AsyncInvocation

	// Done waits for the driver delete handler to complete successfully.
	Done() error
}

// Registry is the kind-to-adapter mapping.
//
// Keeping this as a first-class type, rather than a naked map, makes the call
// sites easier to read and gives us one place for kind lookup diagnostics.
type Registry struct {
	byKind map[string]Adapter
}

// NewRegistry returns the current hardcoded adapter set for Praxis Core.
func NewRegistry() *Registry {
	accounts := auth.LoadFromEnv()
	return NewRegistryWithAdapters(
		NewS3AdapterWithRegistry(accounts),
		NewEBSAdapterWithRegistry(accounts),
		NewAMIAdapterWithRegistry(accounts),
		NewEC2AdapterWithRegistry(accounts),
		NewKeyPairAdapterWithRegistry(accounts),
		NewEIPAdapterWithRegistry(accounts),
		NewSecurityGroupAdapterWithRegistry(accounts),
		NewIGWAdapterWithRegistry(accounts),
		NewVPCAdapterWithRegistry(accounts),
	)
}

// NewRegistryWithAdapters lets higher layers provide adapters that already
// carry any extra dependencies they need, such as live AWS describe clients for
// plan operations.
func NewRegistryWithAdapters(adapters ...Adapter) *Registry {
	byKind := make(map[string]Adapter, len(adapters))
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		byKind[adapter.Kind()] = adapter
	}
	return &Registry{
		byKind: byKind,
	}
}

// Get returns the adapter for a specific resource kind.
func (r *Registry) Get(kind string) (Adapter, error) {
	if r == nil {
		return nil, fmt.Errorf("provider registry is nil")
	}
	adapter, ok := r.byKind[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported resource kind %q", kind)
	}
	return adapter, nil
}

// All returns a defensive copy of the current adapter map.
func (r *Registry) All() map[string]Adapter {
	if r == nil {
		return nil
	}
	out := make(map[string]Adapter, len(r.byKind))
	for kind, adapter := range r.byKind {
		out[kind] = adapter
	}
	return out
}

type resourceDocument struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   resourceMetadata `json:"metadata"`
	Spec       json.RawMessage  `json:"spec"`
	Outputs    map[string]any   `json:"outputs,omitempty"`
}

type resourceMetadata struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
}

func decodeResourceDocument(raw json.RawMessage) (resourceDocument, error) {
	var doc resourceDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return resourceDocument{}, fmt.Errorf("decode resource document: %w", err)
	}
	if doc.Kind == "" {
		return resourceDocument{}, fmt.Errorf("resource document kind is required")
	}
	if len(doc.Spec) == 0 {
		return resourceDocument{}, fmt.Errorf("resource document spec is required")
	}
	return doc, nil
}
