package dynamodbtable

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericTableOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) DynamoDBTableAPI
}

const (
	ddbReadyPollInterval = 10 * time.Second
	ddbReadyMaxAttempts  = 90
)

// NewGenericDynamoDBTableDriver is the async/readiness pilot for the shared
// lifecycle. Durable ACTIVE waits and DynamoDB field convergence remain owned
// by the resource-specific operations implementation.
func NewGenericDynamoDBTableDriver(auth authservice.AuthClient) *kernel.Driver[DynamoDBTableSpec, DynamoDBTableOutputs, ObservedState] {
	return NewGenericDynamoDBTableDriverWithFactory(auth, nil)
}

func NewGenericDynamoDBTableDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) DynamoDBTableAPI) *kernel.Driver[DynamoDBTableSpec, DynamoDBTableOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) DynamoDBTableAPI { return NewDynamoDBTableAPI(awsclient.NewDynamoDBClient(cfg)) }
	}
	ops := &genericTableOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[DynamoDBTableSpec, DynamoDBTableOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec DynamoDBTableSpec) (DynamoDBTableSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return DynamoDBTableSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			spec.Region = region
			spec.ManagedKey = restate.Key(ctx)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) DynamoDBTableSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, _ DynamoDBTableOutputs) DynamoDBTableOutputs {
			return outputsFromObserved(observed)
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   HasDrift,
	})
}

func (o *genericTableOperations) Observe(ctx restate.ObjectContext, desired DynamoDBTableSpec, outputs DynamoDBTableOutputs) (kernel.Observation[ObservedState], error) {
	name := outputs.Name
	if name == "" {
		name = desired.Name
	}
	if name == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observed, found, err := o.observeTable(ctx, api, name)
	return kernel.Observation[ObservedState]{Exists: found, Value: observed}, err
}

func (o *genericTableOperations) Create(ctx restate.ObjectContext, desired DynamoDBTableSpec) (kernel.CreateResult[DynamoDBTableOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[DynamoDBTableOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	observed, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (ObservedState, error) {
		return api.CreateTable(rc, desired)
	}, classifyTableMutation)
	if err != nil {
		return kernel.CreateResult[DynamoDBTableOutputs]{}, err
	}
	if !strings.EqualFold(observed.Status, "ACTIVE") {
		observed, err = o.waitForActive(ctx, api, desired.Name)
		if err != nil {
			return kernel.CreateResult[DynamoDBTableOutputs]{}, err
		}
	}
	return kernel.CreateResult[DynamoDBTableOutputs]{SeedOutputs: outputsFromObserved(observed)}, nil
}

func (o *genericTableOperations) ConvergeProvisionChange(_ restate.ObjectContext, previous, next DynamoDBTableSpec, _ ObservedState, currentOutputs DynamoDBTableOutputs) (DynamoDBTableOutputs, error) {
	switch {
	case previous.Account != next.Account:
		return currentOutputs, restate.TerminalError(fmt.Errorf("account is immutable; delete and reprovision to change it"), 409)
	case previous.Region != next.Region:
		return currentOutputs, restate.TerminalError(fmt.Errorf("region is immutable; delete and reprovision to change it"), 409)
	case previous.Name != next.Name:
		return currentOutputs, restate.TerminalError(fmt.Errorf("name is immutable; delete and reprovision to change it"), 409)
	case previous.HashKey != next.HashKey:
		return currentOutputs, restate.TerminalError(fmt.Errorf("hashKey is immutable; delete and reprovision to change it"), 409)
	case previous.HashKeyType != next.HashKeyType:
		return currentOutputs, restate.TerminalError(fmt.Errorf("hashKeyType is immutable; delete and reprovision to change it"), 409)
	case previous.RangeKey != next.RangeKey:
		return currentOutputs, restate.TerminalError(fmt.Errorf("rangeKey is immutable; delete and reprovision to change it"), 409)
	case previous.RangeKeyType != next.RangeKeyType:
		return currentOutputs, restate.TerminalError(fmt.Errorf("rangeKeyType is immutable; delete and reprovision to change it"), 409)
	default:
		return currentOutputs, nil
	}
}

func (o *genericTableOperations) Converge(ctx restate.ObjectContext, desired DynamoDBTableSpec, observed ObservedState, currentOutputs DynamoDBTableOutputs) (DynamoDBTableOutputs, error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if !strings.EqualFold(observed.Status, "ACTIVE") {
		observed, err = o.waitForActive(ctx, api, desired.Name)
		if err != nil {
			return currentOutputs, err
		}
	}
	return currentOutputs, o.convergeMutableFields(ctx, api, desired, observed)
}

func (o *genericTableOperations) Delete(ctx restate.ObjectContext, desired DynamoDBTableSpec, outputs DynamoDBTableOutputs) error {
	if outputs.Name == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteTable(rc, outputs.Name)
		if IsNotFound(runErr) {
			runErr = nil
		}
		return restate.Void{}, runErr
	}, classifyTableMutation)
	return err
}

func (o *genericTableOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	observed, found, err := o.observeTable(ctx, api, strings.TrimSpace(ref.ResourceID))
	return kernel.Observation[ObservedState]{Exists: found, Value: observed}, err
}

func classifyTableMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsInvalidParam(err) {
		return restate.TerminalError(err, 400)
	}
	if IsConflict(err) {
		return restate.TerminalError(err, 409)
	}
	return err
}

func (o *genericTableOperations) convergeMutableFields(ctx restate.ObjectContext, api DynamoDBTableAPI, spec DynamoDBTableSpec, observed ObservedState) error {
	if configDrift(spec, observed) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTable(rc, spec)
		}, classifyTableMutation); err != nil {
			return err
		}
	}
	toAdd, toRemove := tagDiff(spec.Tags, observed.Tags, spec.ManagedKey)
	if len(toRemove) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UntagResource(rc, observed.ARN, toRemove)
		}, classifyTableMutation); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.TagResource(rc, observed.ARN, toAdd)
		}, classifyTableMutation); err != nil {
			return err
		}
	}
	return nil
}

func (o *genericTableOperations) apiForAccount(ctx restate.ObjectContext, account string) (DynamoDBTableAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("DynamoDBTable driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve DynamoDBTable account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func (o *genericTableOperations) observeTable(ctx restate.ObjectContext, api DynamoDBTableAPI, name string) (ObservedState, bool, error) {
	result, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, found, runErr := api.DescribeTable(rc, name)
		if IsNotFound(runErr) {
			return kernel.Observation[ObservedState]{}, nil
		}
		return kernel.Observation[ObservedState]{Exists: found, Value: observed}, runErr
	}, classifyTableMutation)
	return result.Value, result.Exists, err
}

func (o *genericTableOperations) waitForActive(ctx restate.ObjectContext, api DynamoDBTableAPI, name string) (ObservedState, error) {
	for range ddbReadyMaxAttempts {
		observed, found, err := o.observeTable(ctx, api, name)
		if err != nil {
			return ObservedState{}, err
		}
		if !found {
			return ObservedState{}, restate.TerminalError(fmt.Errorf("table %s disappeared while waiting for ACTIVE", name), 404)
		}
		if strings.EqualFold(observed.Status, "ACTIVE") {
			return observed, nil
		}
		if err := restate.Sleep(ctx, ddbReadyPollInterval); err != nil {
			return ObservedState{}, err
		}
	}
	return ObservedState{}, restate.TerminalError(
		fmt.Errorf("table %s not ACTIVE after %s", name, time.Duration(ddbReadyMaxAttempts)*ddbReadyPollInterval), 500)
}

func tagDiff(desired, observed map[string]string, managedKey string) (map[string]string, []string) {
	want := mergeManagedKey(drivers.FilterPraxisTags(desired), managedKey)
	have := mergeManagedKey(drivers.FilterPraxisTags(observed), managedKey)
	toAdd := map[string]string{}
	for key, value := range want {
		if current, ok := have[key]; !ok || current != value {
			toAdd[key] = value
		}
	}
	var toRemove []string
	for key := range have {
		if _, ok := want[key]; !ok {
			toRemove = append(toRemove, key)
		}
	}
	sort.Strings(toRemove)
	return toAdd, toRemove
}

func mergeManagedKey(tags map[string]string, managedKey string) map[string]string {
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if managedKey != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

func specFromObserved(observed ObservedState) DynamoDBTableSpec {
	return DynamoDBTableSpec{
		Name: observed.Name, BillingMode: billingModeOrDefault(observed.BillingMode),
		HashKey: observed.HashKey, HashKeyType: observed.HashKeyType,
		RangeKey: observed.RangeKey, RangeKeyType: observed.RangeKeyType,
		ReadCapacity: observed.ReadCapacity, WriteCapacity: observed.WriteCapacity,
		Tags: drivers.FilterPraxisTags(observed.Tags),
	}
}

func outputsFromObserved(observed ObservedState) DynamoDBTableOutputs {
	return DynamoDBTableOutputs{ARN: observed.ARN, Name: observed.Name, Status: observed.Status, ItemCount: observed.ItemCount}
}

func applyDefaults(spec DynamoDBTableSpec) DynamoDBTableSpec {
	spec.Region = strings.TrimSpace(spec.Region)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.BillingMode = billingModeOrDefault(strings.TrimSpace(spec.BillingMode))
	spec.HashKey = strings.TrimSpace(spec.HashKey)
	spec.HashKeyType = keyTypeOrDefault(strings.TrimSpace(spec.HashKeyType))
	spec.RangeKey = strings.TrimSpace(spec.RangeKey)
	if spec.RangeKey != "" {
		spec.RangeKeyType = keyTypeOrDefault(strings.TrimSpace(spec.RangeKeyType))
	} else {
		spec.RangeKeyType = ""
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func validateSpec(spec DynamoDBTableSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if spec.HashKey == "" {
		return fmt.Errorf("hashKey is required")
	}
	if !validKeyType(spec.HashKeyType) {
		return fmt.Errorf("hashKeyType must be one of S, N, B")
	}
	if spec.RangeKey != "" && !validKeyType(spec.RangeKeyType) {
		return fmt.Errorf("rangeKeyType must be one of S, N, B")
	}
	if spec.BillingMode != BillingModePayPerRequest && spec.BillingMode != BillingModeProvisioned {
		return fmt.Errorf("billingMode must be one of %s, %s", BillingModePayPerRequest, BillingModeProvisioned)
	}
	return nil
}

func validKeyType(value string) bool { return value == "S" || value == "N" || value == "B" }
