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
	"maps"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/pkg/types"
)

// LookupFilter contains the supported selectors for read-only data source lookup.
// Templates declare data sources in a top-level `data {}` block; each entry is
// unmarshalled into a LookupFilter and forwarded to the matching driver's Lookup
// handler. At least one field must be set — the driver will use whichever
// combination the underlying AWS API supports (ID > Name > Tag).
type LookupFilter struct {
	// Region is the AWS region to query. When empty the driver falls back to
	// the account's default region.
	Region string `json:"region,omitempty"`
	// ID is a provider-native resource identifier (e.g. "ami-0abc123").
	ID string `json:"id,omitempty"`
	// Name matches the AWS "Name" tag (or the native name field for services
	// that have one, like S3 bucket names).
	Name string `json:"name,omitempty"`
	// Tag is an arbitrary key/value set used for tag-based filtering.
	Tag map[string]string `json:"tag,omitempty"`
}

// Adapter bridges a template resource kind (e.g. "S3Bucket", "EC2Instance")
// to the concrete Restate Virtual Object driver that manages that AWS resource.
//
// The adapter pattern exists because the orchestrator and command service work
// with generic JSON documents, while each driver exposes strongly typed Go
// structs. Every Adapter implementation lives in this package and handles the
// type-casting, key derivation, and Restate call wiring for exactly one kind.
//
// Flow: template evaluation → JSON resource doc → Adapter.DecodeSpec → typed
// Go struct → Adapter.Provision (Restate service-to-service call) → driver VO.
type Adapter interface {
	// Kind returns the template kind handled by the adapter (e.g. "S3Bucket").
	// This is the value that appears in the resource document's "kind" field.
	Kind() string

	// ServiceName returns the Restate Virtual Object service name that hosts
	// the driver for this resource kind. The registry uses this to dispatch
	// durable service-to-service calls from the orchestrator to the driver.
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
// When the orchestrator workflow fans out resource operations in parallel, each
// adapter returns an AsyncInvocation handle. The workflow stores these in a map
// keyed by resource name, then uses restate.Wait / restate.WaitFirst to wait
// for completion. Because the invocation ID is part of Restate's journal, the
// workflow can survive restarts and re-attach to in-flight driver calls.
type AsyncInvocation interface {
	// ID returns the durable Restate invocation ID. This ID uniquely identifies
	// the driver call across process restarts and can be used with
	// restate.AttachInvocation to re-attach from a different handler.
	ID() string

	// Future returns the Restate future used by restate.Wait / restate.WaitFirst
	// to await completion of the underlying driver call.
	Future() restate.Future
}

// ProvisionInvocation represents an in-flight Provision call whose success value
// is a normalized output map.
//
// When the restate.Future resolves, Outputs() deserializes the driver's typed
// response and passes it through the adapter's NormalizeOutputs to produce the
// generic map[string]any that the deployment state stores.
type ProvisionInvocation interface {
	AsyncInvocation

	// Outputs waits for the underlying driver call to complete, decodes the typed
	// driver output, and converts it into the generic deployment-facing map.
	// The returned map is stored in deployment state and made available to
	// downstream resources via output expression hydration.
	Outputs() (map[string]any, error)
}

// DeleteInvocation represents an in-flight Delete call.
// Unlike ProvisionInvocation, delete operations produce no outputs.
type DeleteInvocation interface {
	AsyncInvocation

	// Done waits for the driver delete handler to complete. Returns nil on
	// success or the error surfaced by the driver.
	Done() error
}

// Registry is the central kind → Adapter mapping.
//
// At startup, NewRegistry populates this map with one Adapter per supported
// AWS resource kind. The orchestrator, command service, and CLI all call
// Registry.Get(kind) to obtain the adapter that knows how to build keys,
// decode specs, submit durable provisioning calls, and normalize outputs
// for that particular resource type.
//
// Keeping this as a first-class type, rather than a naked map, makes the call
// sites easier to read and gives us one place for kind lookup diagnostics.
type Registry struct {
	// byKind maps a template kind string (e.g. "S3Bucket") to the concrete
	// adapter that bridges JSON resource documents to typed driver calls.
	byKind map[string]Adapter
}

// NewRegistry returns the current hardcoded adapter set for Praxis Core.
// Each adapter is constructed with an AuthClient so that the adapter can inject
// per-account AWS credentials when making durable Restate service-to-service
// calls to the underlying driver Virtual Object.
func NewRegistry(auth authservice.AuthClient) *Registry {
	return NewRegistryWithAdapters(
		NewS3AdapterWithAuth(auth),
		NewEBSAdapterWithAuth(auth),
		NewAMIAdapterWithAuth(auth),
		NewACMCertificateAdapterWithAuth(auth),
		NewEC2AdapterWithAuth(auth),
		NewECRRepositoryAdapterWithAuth(auth),
		NewECRLifecyclePolicyAdapterWithAuth(auth),
		NewKeyPairAdapterWithAuth(auth),
		NewIAMRoleAdapterWithAuth(auth),
		NewIAMPolicyAdapterWithAuth(auth),
		NewIAMUserAdapterWithAuth(auth),
		NewIAMGroupAdapterWithAuth(auth),
		NewIAMInstanceProfileAdapterWithAuth(auth),
		NewLogGroupAdapterWithAuth(auth),
		NewMetricAlarmAdapterWithAuth(auth),
		NewDashboardAdapterWithAuth(auth),
		NewEIPAdapterWithAuth(auth),
		NewNATGatewayAdapterWithAuth(auth),
		NewNetworkACLAdapterWithAuth(auth),
		NewRoute53HostedZoneAdapterWithAuth(auth),
		NewRoute53RecordAdapterWithAuth(auth),
		NewRoute53HealthCheckAdapterWithAuth(auth),
		NewRouteTableAdapterWithAuth(auth),
		NewSecurityGroupAdapterWithAuth(auth),
		NewESMAdapterWithAuth(auth),
		NewLambdaAdapterWithAuth(auth),
		NewLambdaLayerAdapterWithAuth(auth),
		NewLambdaPermissionAdapterWithAuth(auth),
		NewSubnetAdapterWithAuth(auth),
		NewIGWAdapterWithAuth(auth),
		NewVPCPeeringAdapterWithAuth(auth),
		NewVPCAdapterWithAuth(auth),
		NewDBSubnetGroupAdapterWithAuth(auth),
		NewDBParameterGroupAdapterWithAuth(auth),
		NewRDSInstanceAdapterWithAuth(auth),
		NewAuroraClusterAdapterWithAuth(auth),
		NewALBAdapterWithAuth(auth),
		NewNLBAdapterWithAuth(auth),
		NewTargetGroupAdapterWithAuth(auth),
		NewListenerAdapterWithAuth(auth),
		NewListenerRuleAdapterWithAuth(auth),
		NewSNSTopicAdapterWithAuth(auth),
		NewSNSSubscriptionAdapterWithAuth(auth),
		NewSQSAdapterWithAuth(auth),
		NewSQSQueuePolicyAdapterWithAuth(auth),
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

// Get returns the adapter for a specific resource kind. Returns an error if
// the kind is not registered, which typically means the template references an
// unsupported AWS resource type.
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

// All returns a defensive copy of the current adapter map. Used by introspection
// endpoints (e.g. listing supported resource kinds in the CLI).
func (r *Registry) All() map[string]Adapter {
	if r == nil {
		return nil
	}
	out := make(map[string]Adapter, len(r.byKind))
	maps.Copy(out, r.byKind)
	return out
}

// resourceDocument is the canonical envelope that wraps every resource emitted
// by the CUE template engine. Adapters unmarshal it to extract the spec (which
// gets decoded into a driver-specific Go struct) and the metadata (which
// provides the resource name and labels for key derivation).
type resourceDocument struct {
	APIVersion string           `json:"apiVersion"`
	Kind       string           `json:"kind"`
	Metadata   resourceMetadata `json:"metadata"`
	Spec       json.RawMessage  `json:"spec"`
	Outputs    map[string]any   `json:"outputs,omitempty"`
}

// resourceMetadata holds the name and labels from the resource document's
// metadata block. The name is used to build canonical Restate Virtual Object
// keys; labels are passed through to the driver for AWS resource tagging.
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
