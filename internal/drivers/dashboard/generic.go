package dashboard

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) DashboardAPI
}

// NewGenericDashboardDriver is the Dashboard lifecycle implementation.
func NewGenericDashboardDriver(auth authservice.AuthClient) *kernel.Driver[DashboardSpec, DashboardOutputs, ObservedState] {
	return NewGenericDashboardDriverWithFactory(auth, nil)
}

func NewGenericDashboardDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DashboardAPI) *kernel.Driver[DashboardSpec, DashboardOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) DashboardAPI { return NewDashboardAPI(awsclient.NewCloudWatchClient(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[DashboardSpec, DashboardOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec DashboardSpec) (DashboardSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return DashboardSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) DashboardSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ DashboardOutputs) DashboardOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired DashboardSpec, outputs DashboardOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.DashboardName
	if name == "" {
		name = desired.DashboardName
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.GetDashboard(rc, name)
		return kernel.Observation[ObservedState]{Exists: found, Value: observed}, runErr
	}, classifyDashboardMutation)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired DashboardSpec) (kernel.CreateResult[DashboardOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[DashboardOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) ([]ValidationMessage, error) {
		return api.PutDashboard(rc, desired)
	}, classifyDashboardMutation)
	return kernel.CreateResult[DashboardOutputs]{SeedOutputs: DashboardOutputs{DashboardName: desired.DashboardName}}, err
}

func (o *genericOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next DashboardSpec, _ ObservedState) error {
	switch {
	case previous.Account != next.Account:
		return restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.DashboardName != next.DashboardName:
		return restate.TerminalError(fmt.Errorf("dashboardName is immutable; delete and reprovision to change it"), 409)
	default:
		return nil
	}
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired DashboardSpec, _ ObservedState) error {
	_, err := o.Create(ctx, desired)
	return err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired DashboardSpec, outputs DashboardOutputs) error {
	if outputs.DashboardName == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteDashboard(rc, outputs.DashboardName)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyDashboardMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.GetDashboard(rc, ref.ResourceID)
		return kernel.Observation[ObservedState]{Exists: found, Value: observed}, runErr
	}, classifyDashboardMutation)
}

func (o *genericOperations) apiForAccount(ctx restate.ObjectContext, account string) (DashboardAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("generic Dashboard driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Dashboard account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyDashboardMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsDashboardInvalidInput(err) || IsInvalidParam(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func outputsFromObserved(observed ObservedState) DashboardOutputs {
	return DashboardOutputs{DashboardArn: observed.DashboardArn, DashboardName: observed.DashboardName}
}

func specFromObserved(observed ObservedState) DashboardSpec {
	return DashboardSpec{DashboardName: observed.DashboardName, DashboardBody: observed.DashboardBody}
}

func applyDefaults(spec DashboardSpec) DashboardSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.DashboardName = strings.TrimSpace(spec.DashboardName)
	spec.DashboardBody = strings.TrimSpace(spec.DashboardBody)
	return spec
}

func validateSpec(spec DashboardSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.DashboardName == "" {
		return fmt.Errorf("dashboardName is required")
	}
	if spec.DashboardBody == "" {
		return fmt.Errorf("dashboardBody is required")
	}
	return nil
}
