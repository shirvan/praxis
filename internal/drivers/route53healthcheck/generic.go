package route53healthcheck

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) HealthCheckAPI
}

// NewGenericHealthCheckDriver binds Route 53 health-check behavior to the shared lifecycle kernel.
func NewGenericHealthCheckDriver(auth authservice.AuthClient) *kernel.Driver[HealthCheckSpec, HealthCheckOutputs, ObservedState] {
	return newGenericHealthCheckDriverWithFactory(auth, nil)
}

func newGenericHealthCheckDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) HealthCheckAPI) *kernel.Driver[HealthCheckSpec, HealthCheckOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) HealthCheckAPI { return NewHealthCheckAPI(awsclient.NewRoute53Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[HealthCheckSpec, HealthCheckOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec HealthCheckSpec) (HealthCheckSpec, error) {
			if _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return HealthCheckSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.ManagedKey = restate.Key(ctx)
			normalized, err := normalizeHealthCheckSpec(spec)
			if err != nil {
				return HealthCheckSpec{}, restate.TerminalError(err, 400)
			}
			return normalized, nil
		},
		Validate: func(spec HealthCheckSpec) error {
			_, err := normalizeHealthCheckSpec(spec)
			return err
		},
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) HealthCheckSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed HealthCheckOutputs) HealthCheckOutputs {
			if observed.HealthCheckId == "" {
				return seed
			}
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired HealthCheckSpec, outputs HealthCheckOutputs) (kernel.Observation[ObservedState], error) {
	if strings.TrimSpace(outputs.HealthCheckId) == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeHealthCheck(ctx, api, outputs.HealthCheckId)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired HealthCheckSpec) (kernel.CreateResult[HealthCheckOutputs], error) {
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[HealthCheckOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	healthCheckID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// CallerReference is Route 53's native idempotency token. It also recovers
		// partial creation when applying tags failed after the check was allocated.
		return api.CreateHealthCheck(rc, desired)
	}, classifyHealthCheckCreate)
	return kernel.CreateResult[HealthCheckOutputs]{SeedOutputs: HealthCheckOutputs{HealthCheckId: strings.TrimSpace(healthCheckID)}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired HealthCheckSpec, observed ObservedState, currentOutputs HealthCheckOutputs) (HealthCheckOutputs, error) {
	if desired.Type != observed.Type {
		return currentOutputs, restate.TerminalError(fmt.Errorf("health check type is immutable: observed %q, requested %q; delete and reprovision", observed.Type, desired.Type), 409)
	}
	if observed.RequestInterval != 0 && desired.RequestInterval != observed.RequestInterval {
		return currentOutputs, restate.TerminalError(fmt.Errorf("health check requestInterval is immutable: observed %d, requested %d; delete and reprovision", observed.RequestInterval, desired.RequestInterval), 409)
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	healthCheckID := observed.HealthCheckId
	if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		// Refresh the optimistic-concurrency version inside the same durable retry
		// callback. A HealthCheckVersionMismatch retry must not reuse the version
		// that already lost the race.
		latest, describeErr := api.DescribeHealthCheck(rc, healthCheckID)
		if describeErr != nil {
			return restate.Void{}, describeErr
		}
		return restate.Void{}, api.UpdateHealthCheck(rc, healthCheckID, latest, desired)
	}, classifyHealthCheckMutation); err != nil {
		return currentOutputs, fmt.Errorf("update health check configuration: %w", err)
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, healthCheckID, desired.Tags)
		}, classifyHealthCheckMutation); err != nil {
			return currentOutputs, fmt.Errorf("update health check tags: %w", err)
		}
	}
	return currentOutputs, nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired HealthCheckSpec, outputs HealthCheckOutputs) error {
	healthCheckID := strings.TrimSpace(outputs.HealthCheckId)
	if healthCheckID == "" {
		return nil
	}
	api, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteHealthCheck(rc, healthCheckID)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyHealthCheckMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeHealthCheck(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeHealthCheck(ctx restate.ObjectContext, api HealthCheckAPI, healthCheckID string) (kernel.Observation[ObservedState], error) {
	if healthCheckID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeHealthCheck(rc, healthCheckID)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: normalizeObservedState(observed)}, nil
	}, classifyHealthCheckObserve)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (HealthCheckAPI, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, fmt.Errorf("Route53 health-check driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve Route53 health-check account %q: %w", account, err)
	}
	return o.apiFactory(cfg), nil
}

func classifyHealthCheckObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidInput(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyHealthCheckCreate(err error) error {
	if err == nil {
		return nil
	}
	if IsAlreadyExists(err) {
		return restate.TerminalError(err, 409)
	}
	return classifyHealthCheckMutation(err)
}

func classifyHealthCheckMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	// Provider serialization and optimistic-version races are retryable.
	if IsConflict(err) {
		return err
	}
	return classifyHealthCheckObserve(err)
}

func specFromObserved(observed ObservedState) HealthCheckSpec {
	return HealthCheckSpec{
		Type: observed.Type, IPAddress: observed.IPAddress, Port: observed.Port,
		ResourcePath: observed.ResourcePath, FQDN: observed.FQDN, SearchString: observed.SearchString,
		RequestInterval: observed.RequestInterval, FailureThreshold: observed.FailureThreshold,
		ChildHealthChecks: observed.ChildHealthChecks, HealthThreshold: observed.HealthThreshold,
		CloudWatchAlarmName: observed.CloudWatchAlarmName, CloudWatchAlarmRegion: observed.CloudWatchAlarmRegion,
		InsufficientDataHealthStatus: observed.InsufficientDataHealthStatus, Disabled: observed.Disabled,
		InvertHealthCheck: observed.InvertHealthCheck, EnableSNI: observed.EnableSNI,
		Regions: observed.Regions, Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) HealthCheckOutputs {
	return HealthCheckOutputs{HealthCheckId: observed.HealthCheckId}
}
