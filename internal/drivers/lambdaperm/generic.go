package lambdaperm

import (
	"encoding/json"
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
	apiFactory func(aws.Config) PermissionAPI
}

// NewGenericLambdaPermissionDriver returns the Lambda permission lifecycle
// implementation backed by the shared generic kernel.
func NewGenericLambdaPermissionDriver(auth authservice.AuthClient) *kernel.Driver[LambdaPermissionSpec, LambdaPermissionOutputs, ObservedState] {
	return newGenericLambdaPermissionDriverWithFactory(auth, nil)
}

func newGenericLambdaPermissionDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) PermissionAPI) *kernel.Driver[LambdaPermissionSpec, LambdaPermissionOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) PermissionAPI {
			return NewPermissionAPI(awsclient.NewLambdaClient(cfg))
		}
	}
	ops := &genericOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[LambdaPermissionSpec, LambdaPermissionOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec LambdaPermissionSpec) (LambdaPermissionSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return LambdaPermissionSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec.Region = region
			return applyDefaults(spec), nil
		},
		Validate: validateProvisionSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) LambdaPermissionSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: outputsFromObserved,
		FieldDiffs:          ComputeFieldDiffs,
		HasDrift:            HasDrift,
	})
}

func (o *genericOperations) Observe(ctx restate.ObjectContext, desired LambdaPermissionSpec, outputs LambdaPermissionOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	functionName := strings.TrimSpace(outputs.FunctionName)
	statementID := strings.TrimSpace(outputs.StatementId)
	recoveredByIdentity := false
	if functionName == "" || statementID == "" {
		functionName = strings.TrimSpace(desired.FunctionName)
		statementID = strings.TrimSpace(desired.StatementId)
		recoveredByIdentity = functionName != "" && statementID != ""
	}
	if functionName == "" || statementID == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	observation, err := observePermission(ctx, api, functionName, statementID)
	if observation.Exists {
		observation.Value.recoveredByIdentity = recoveredByIdentity
	}
	return observation, err
}

func (o *genericOperations) Create(ctx restate.ObjectContext, desired LambdaPermissionSpec) (kernel.CreateResult[LambdaPermissionOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[LambdaPermissionOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	statement, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		// AddPermission has no idempotency token. Re-observe inside the durable
		// callback so a replay after a lost response adopts only an exact match.
		observed, getErr := api.GetPermission(rc, desired.FunctionName, desired.StatementId)
		if getErr == nil {
			if HasDrift(desired, observed) {
				return "", restate.TerminalError(fmt.Errorf(
					"lambda permission %s already exists on %s with different configuration",
					desired.StatementId, desired.FunctionName,
				), 409)
			}
			return marshalObservedStatement(observed), nil
		}
		if !IsNotFound(getErr) {
			return "", getErr
		}
		return api.AddPermission(rc, desired)
	}, classifyPermissionMutation)
	return kernel.CreateResult[LambdaPermissionOutputs]{SeedOutputs: LambdaPermissionOutputs{
		StatementId: desired.StatementId, FunctionName: desired.FunctionName, Statement: statement,
	}}, err
}

func (o *genericOperations) Converge(ctx restate.ObjectContext, desired LambdaPermissionSpec, observed ObservedState, currentOutputs LambdaPermissionOutputs) (LambdaPermissionOutputs, error) {
	if desired.FunctionName != observed.FunctionName {
		return currentOutputs, restate.TerminalError(fmt.Errorf(
			"functionName is immutable; delete and reprovision the Lambda permission to move it from %s to %s",
			observed.FunctionName, desired.FunctionName,
		), 409)
	}
	if desired.StatementId != observed.StatementId {
		return currentOutputs, restate.TerminalError(fmt.Errorf(
			"statementId is immutable; delete and reprovision the Lambda permission to change it from %s to %s",
			observed.StatementId, desired.StatementId,
		), 409)
	}
	if observed.recoveredByIdentity {
		return currentOutputs, restate.TerminalError(fmt.Errorf(
			"lambda permission %s already exists on %s with different configuration",
			desired.StatementId, desired.FunctionName,
		), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		removeErr := api.RemovePermission(rc, observed.FunctionName, observed.StatementId)
		if IsNotFound(removeErr) {
			removeErr = nil
		}
		return restate.Void{}, removeErr
	}, classifyPermissionMutation); err != nil {
		return currentOutputs, err
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		existing, getErr := api.GetPermission(rc, desired.FunctionName, desired.StatementId)
		if getErr == nil {
			if HasDrift(desired, existing) {
				return "", restate.TerminalError(fmt.Errorf(
					"lambda permission %s was replaced concurrently with different configuration",
					desired.StatementId,
				), 409)
			}
			return marshalObservedStatement(existing), nil
		}
		if !IsNotFound(getErr) {
			return "", getErr
		}
		return api.AddPermission(rc, desired)
	}, classifyPermissionMutation)
	return currentOutputs, err
}

func (o *genericOperations) Delete(ctx restate.ObjectContext, desired LambdaPermissionSpec, outputs LambdaPermissionOutputs) error {
	if outputs.FunctionName == "" || outputs.StatementId == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		removeErr := api.RemovePermission(rc, outputs.FunctionName, outputs.StatementId)
		if IsNotFound(removeErr) {
			removeErr = nil
		}
		return restate.Void{}, removeErr
	}, classifyPermissionMutation)
	return err
}

func (o *genericOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	functionName, statementID, err := splitImportResourceID(ref.ResourceID)
	if err != nil {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(err, 400)
	}
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observePermission(ctx, api, functionName, statementID)
}

func observePermission(ctx restate.ObjectContext, api PermissionAPI, functionName, statementID string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.GetPermission(rc, functionName, statementID)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: observed}, nil
	}, classifyPermissionObserve)
}

func outputsFromObserved(observed ObservedState, seed LambdaPermissionOutputs) LambdaPermissionOutputs {
	seed.StatementId = observed.StatementId
	seed.FunctionName = observed.FunctionName
	seed.Statement = marshalObservedStatement(observed)
	return seed
}

func marshalObservedStatement(observed ObservedState) string {
	statement := policyStatement{
		Sid: observed.StatementId, Principal: observed.Principal, Action: observed.Action,
	}
	if observed.Condition != "" {
		var condition any
		if json.Unmarshal([]byte(observed.Condition), &condition) == nil {
			statement.Condition = condition
		}
	}
	raw, err := json.Marshal(statement)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (o *genericOperations) apiForAccount(ctx restate.Context, account string) (PermissionAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("LambdaPermission driver is not configured with an auth client")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Lambda permission account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func classifyPermissionObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyPermissionMutation(err error) error {
	if err == nil {
		return nil
	}
	if restate.IsTerminalError(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsNotFound(err) {
		return restate.TerminalError(err, 404)
	}
	if IsConflict(err) || IsPreconditionFailed(err) {
		return restate.TerminalError(err, 409)
	}
	if IsInvalidParameter(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func applyDefaults(spec LambdaPermissionSpec) LambdaPermissionSpec {
	if strings.TrimSpace(spec.Action) == "" {
		spec.Action = "lambda:InvokeFunction"
	}
	return spec
}

func validateProvisionSpec(spec LambdaPermissionSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.FunctionName) == "" {
		return fmt.Errorf("functionName is required")
	}
	if strings.TrimSpace(spec.StatementId) == "" {
		return fmt.Errorf("statementId is required")
	}
	if strings.TrimSpace(spec.Principal) == "" {
		return fmt.Errorf("principal is required")
	}
	return nil
}

func specFromObserved(observed ObservedState) LambdaPermissionSpec {
	return applyDefaults(LambdaPermissionSpec{
		FunctionName: observed.FunctionName, StatementId: observed.StatementId,
		Action: observed.Action, Principal: observed.Principal, SourceArn: observed.SourceArn,
		SourceAccount: observed.SourceAccount, EventSourceToken: observed.EventSourceToken,
	})
}

func splitImportResourceID(resourceID string) (string, string, error) {
	parts := strings.SplitN(resourceID, "~", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("import resource ID must be functionName~statementId")
	}
	return parts[0], parts[1], nil
}
