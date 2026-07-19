# Add Adapter

**Description**: Register a resource with Core through the descriptor-driven
`provider.GenericAdapter`.

**When to use**: After the driver package and schema compile.

## Contract

Every production resource uses `GenericAdapter`. Do not implement copied
Provision, Delete, Import, Plan, or output-dispatch methods. Resource-specific
planning and optional lookup/observe behavior are hooks around the embedded
generic adapter.

The generic adapter owns:

- resource-document parsing and dispatch plumbing;
- the alpha `ProvisionRequest{Spec, Lifecycle}` envelope (the generic adapter
  encodes the typed spec into the envelope's raw JSON field);
- Restate futures for Provision/Delete/Import;
- create/update/no-op plan selection and sensitive-field masking;
- Auth Service credential resolution and common plan-error classification.

## Steps

### 1. Add the descriptor

Create `internal/core/provider/{resource}_adapter.go`:

```go
type WidgetAdapter struct {
	*GenericAdapter[widget.Spec, widget.Outputs, widget.ObservedState]
}

func widgetDescriptor() GenericDescriptor[widget.Spec, widget.Outputs, widget.ObservedState] {
	return GenericDescriptor[widget.Spec, widget.Outputs, widget.ObservedState]{
		Kind:  widget.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(raw json.RawMessage, metadataName string) (widget.Spec, error) {
			var spec widget.Spec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return widget.Spec{}, fmt.Errorf("decode Widget spec: %w", err)
			}
			spec.Name = strings.TrimSpace(metadataName)
			if spec.Name == "" || strings.TrimSpace(spec.Region) == "" {
				return widget.Spec{}, fmt.Errorf("Widget metadata.name and spec.region are required")
			}
			return spec, nil
		},

		KeyFromSpec: func(spec widget.Spec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("name", spec.Name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.Name), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec widget.Spec, key, account string) widget.Spec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out widget.Outputs) map[string]any {
			return map[string]any{"arn": out.ARN, "id": out.ID}
		},

		PlanIdentity: storedPlanIdentity[widget.Spec](func(out widget.Outputs) string {
			return out.ID
		}),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[widget.Spec, widget.Outputs, widget.ObservedState] {
			return widgetProbe(widget.NewAPI(awsclient.NewWidgetClient(cfg)))
		},

		DiffFields: func(desired widget.Spec, observed widget.ObservedState, _ widget.Outputs) []types.FieldDiff {
			return widget.ComputeFieldDiffs(desired, observed)
		},
	}
}
```

`PlanProbeInput` contains the canonical key, selected identity, desired spec,
and stored outputs. Use only the fields the provider lookup needs. Translate
not-found to `(zero, false, nil)` and return all other errors unchanged so the
generic boundary classifies them.

```go
func widgetProbe(api widget.API) PlanProbeFunc[widget.Spec, widget.Outputs, widget.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[widget.Spec, widget.Outputs]) (widget.ObservedState, bool, error) {
		observed, err := api.Describe(ctx, input.Identity)
		if widget.IsNotFound(err) {
			return widget.ObservedState{}, false, nil
		}
		return observed, err == nil, err
	}
}
```

### 2. Add constructors

```go
func NewWidgetAdapterWithAuth(auth authservice.AuthClient) *WidgetAdapter {
	return &WidgetAdapter{GenericAdapter: NewGenericAdapter(widgetDescriptor(), auth)}
}

func NewWidgetAdapterWithAPI(api widget.API) *WidgetAdapter {
	return &WidgetAdapter{GenericAdapter: NewGenericAdapterWithProbe(widgetDescriptor(), widgetProbe(api))}
}
```

Optional `Lookup`, `Observe`, and timeout methods may live on the wrapper type.
They must not replace generic lifecycle or plan plumbing.

### 3. Register it

Add the production constructor to `internal/core/provider/registry.go`. The
registry and driver-pack conformance suites require one adapter for every bound
driver and require every adapter to promote the generic marker.

### 4. Test the vertical slice

Add `{resource}_adapter_test.go` covering:

- valid and invalid key construction;
- spec decoding and account/managed-key preparation;
- import key construction;
- output normalization;
- plan create, no-op, update, not-found, retryable, and terminal paths;
- any desired-spec or stored-output values consumed by the plan probe;
- sensitive diff masking, if applicable.

Run:

```bash
go test ./internal/core/provider -count=1
go test ./internal/driverpack/... -count=1
go vet ./internal/core/provider ./internal/driverpack/...
```

## Key scopes

| Scope | Typical key |
| --- | --- |
| `KeyScopeGlobal` | `name` |
| `KeyScopeRegion` | `region~name` |
| `KeyScopeCustom` | Resource-defined composite identity |

Validate every key component before calling `JoinKey`. Kind and Restate service
name must remain identical.
