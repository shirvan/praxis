package igw

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
	apiFactory func(aws.Config) IGWAPI
}

// NewGenericIGWDriver binds Internet Gateway behavior to the generic lifecycle kernel.
func NewGenericIGWDriver(auth authservice.AuthClient) *kernel.Driver[IGWSpec, IGWOutputs, ObservedState] {
	return newGenericIGWDriverWithFactory(auth, nil)
}

func newGenericIGWDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IGWAPI) *kernel.Driver[IGWSpec, IGWOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) IGWAPI { return NewIGWAPI(awsclient.NewEC2Client(cfg)) }
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[IGWSpec, IGWOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec IGWSpec) (IGWSpec, error) {
			if _, _, err := ops.apiForAccount(ctx, spec.Account); err != nil {
				return IGWSpec{}, drivers.ClassifyCredentialError(err)
			}
			if spec.Tags == nil {
				spec.Tags = map[string]string{}
			}
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate:       validateProvisionSpec,
		ValidateImport: validateImportSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) IGWSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			spec.Region = observed.Region
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ IGWOutputs) IGWOutputs {
			return outputsFromObserved(observed)
		},
		HasDrift: HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired IGWSpec, outputs IGWOutputs) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	internetGatewayID := strings.TrimSpace(outputs.InternetGatewayId)
	if internetGatewayID == "" && desired.ManagedKey != "" {
		internetGatewayID, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyIGWFind)
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		if internetGatewayID != "" {
			return kernel.Observation[ObservedState]{}, restate.TerminalError(
				formatManagedKeyConflict(desired.ManagedKey, internetGatewayID), 409,
			)
		}
	}
	if internetGatewayID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return observeInternetGateway(ctx, api, region, internetGatewayID)
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired IGWSpec) (kernel.CreateResult[IGWOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[IGWOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	internetGatewayID, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// EC2 offers no create token for Internet Gateways. Rechecking the managed
		// key in the durable callback prevents a retry after an ambiguous response
		// from creating a duplicate gateway.
		if desired.ManagedKey != "" {
			existing, findErr := api.FindByManagedKey(rc, desired.ManagedKey)
			if findErr != nil || existing != "" {
				return existing, findErr
			}
		}
		return api.CreateInternetGateway(rc, desired)
	}, classifyIGWCreate)
	return kernel.CreateResult[IGWOutputs]{SeedOutputs: IGWOutputs{InternetGatewayId: internetGatewayID}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired IGWSpec, observed ObservedState) error {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	internetGatewayID := observed.InternetGatewayId
	if internetGatewayID == "" {
		return restate.TerminalError(fmt.Errorf("cannot converge internet gateway without an internetGatewayId"), 500)
	}

	if desired.VpcId != observed.AttachedVpcId {
		if observed.AttachedVpcId != "" {
			if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
				detachErr := api.DetachFromVpc(rc, internetGatewayID, observed.AttachedVpcId)
				if IsNotAttached(detachErr) {
					detachErr = nil
				}
				return restate.Void{}, detachErr
			}, classifyIGWMutation); err != nil {
				return fmt.Errorf("detach from VPC: %w", err)
			}
		}

		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			attachErr := api.AttachToVpc(rc, internetGatewayID, desired.VpcId)
			if !IsAlreadyAttached(attachErr) {
				return restate.Void{}, attachErr
			}
			// Resource.AlreadyAssociated is ambiguous: it can mean our attach
			// completed or that the target VPC is owned by a different IGW.
			current, describeErr := api.DescribeInternetGateway(rc, internetGatewayID)
			if describeErr == nil && current.AttachedVpcId == desired.VpcId {
				return restate.Void{}, nil
			}
			if describeErr != nil {
				return restate.Void{}, describeErr
			}
			return restate.Void{}, restate.TerminalError(fmt.Errorf(
				"cannot attach internet gateway %s to VPC %s: the VPC already has an internet gateway attached",
				internetGatewayID, desired.VpcId,
			), 409)
		}, classifyIGWMutation); err != nil {
			return fmt.Errorf("attach to VPC: %w", err)
		}
	}

	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, internetGatewayID, desired.Tags)
		}, classifyIGWMutation); err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired IGWSpec, outputs IGWOutputs) error {
	internetGatewayID := strings.TrimSpace(outputs.InternetGatewayId)
	if internetGatewayID == "" {
		return nil
	}
	api, region, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	current, err := observeInternetGateway(ctx, api, region, internetGatewayID)
	if err != nil || !current.Exists {
		return err
	}
	if current.Value.AttachedVpcId != "" {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			detachErr := api.DetachFromVpc(rc, internetGatewayID, current.Value.AttachedVpcId)
			if IsNotAttached(detachErr) {
				detachErr = nil
			}
			return restate.Void{}, detachErr
		}, classifyIGWMutation); err != nil {
			return err
		}
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteInternetGateway(rc, internetGatewayID)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyIGWMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, region, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeInternetGateway(ctx, api, region, strings.TrimSpace(ref.ResourceID))
}

func observeInternetGateway(ctx restate.ObjectContext, api IGWAPI, region, internetGatewayID string) (kernel.Observation[ObservedState], error) {
	observation, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, describeErr := api.DescribeInternetGateway(rc, internetGatewayID)
		if IsNotFound(describeErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if describeErr != nil {
			return kernel.Observation[ObservedState]{}, describeErr
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyIGWObserve)
	if observation.Exists {
		observation.Value.Region = region
	}
	return observation, err
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (IGWAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("InternetGateway driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve internet gateway account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateProvisionSpec(spec IGWSpec) error {
	if err := validateImportSpec(spec); err != nil {
		return err
	}
	if spec.VpcId == "" {
		return fmt.Errorf("vpcId is required")
	}
	return nil
}

func validateImportSpec(spec IGWSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	return nil
}

func specFromObserved(observed ObservedState) IGWSpec {
	return IGWSpec{VpcId: observed.AttachedVpcId, Tags: drivers.FilterPraxisTags(observed.Tags)}
}

func outputsFromObserved(observed ObservedState) IGWOutputs {
	state := "detached"
	if observed.AttachedVpcId != "" {
		state = "available"
	}
	return IGWOutputs{
		InternetGatewayId: observed.InternetGatewayId, VpcId: observed.AttachedVpcId,
		OwnerId: observed.OwnerId, State: state,
	}
}

func formatManagedKeyConflict(managedKey, internetGatewayID string) error {
	return fmt.Errorf(
		"internet gateway name %q in this region is already managed by Praxis (internetGatewayId: %s); remove the existing resource or use a different metadata.name",
		managedKey, internetGatewayID,
	)
}

func classifyIGWObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyIGWFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyIGWObserve(err)
}

func classifyIGWCreate(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 500)
	}
	return classifyIGWMutation(err)
}

func classifyIGWMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsInvalidParam(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsAlreadyAttached(err) {
		return restate.TerminalError(err, 409)
	}
	// DependencyViolation and provider availability errors are retryable.
	return err
}
