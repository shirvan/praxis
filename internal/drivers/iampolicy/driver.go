package iampolicy

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// IAMPolicyDriver is the Restate virtual object that manages the lifecycle of a single IAM policy.
type IAMPolicyDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) IAMPolicyAPI
}

// NewIAMPolicyDriver constructs a driver with the default AWS API factory.
func NewIAMPolicyDriver(auth authservice.AuthClient) *IAMPolicyDriver {
	return NewIAMPolicyDriverWithFactory(auth, func(cfg aws.Config) IAMPolicyAPI {
		return NewIAMPolicyAPI(awsclient.NewIAMClient(cfg))
	})
}

func NewIAMPolicyDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) IAMPolicyAPI) *IAMPolicyDriver {
	if factory == nil {
		factory = func(cfg aws.Config) IAMPolicyAPI {
			return NewIAMPolicyAPI(awsclient.NewIAMClient(cfg))
		}
	}
	return &IAMPolicyDriver{auth: auth, apiFactory: factory}
}

func (d *IAMPolicyDriver) ServiceName() string {
	return ServiceName
}

// Provision implements the idempotent create-or-converge pattern for IAM policies.
// If the policy doesn't exist, creates it. If it exists, converges the policy document
// (via CreatePolicyVersion) and tags to match the desired spec.
func (d *IAMPolicyDriver) Provision(ctx restate.ObjectContext, spec IAMPolicySpec) (IAMPolicyOutputs, error) {
	ctx.Log().Info("provisioning iam policy", "key", restate.Key(ctx), "policyName", spec.PolicyName)
	api, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return IAMPolicyOutputs{}, restate.TerminalError(err, 400)
	}

	spec = applyDefaults(spec)
	if spec.PolicyName == "" {
		return IAMPolicyOutputs{}, restate.TerminalError(fmt.Errorf("policyName is required"), 400)
	}
	if spec.PolicyDocument == "" {
		return IAMPolicyOutputs{}, restate.TerminalError(fmt.Errorf("policyDocument is required"), 400)
	}

	state, err := restate.Get[IAMPolicyState](ctx, drivers.StateKey)
	if err != nil {
		return IAMPolicyOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	outputs := state.Outputs
	policyExists := outputs.Arn != ""
	if policyExists {
		descResult, descErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, runErr := api.DescribePolicy(rc, outputs.Arn)
			if runErr != nil {
				if IsNotFound(runErr) {
					return ObservedState{}, restate.TerminalError(runErr, 404)
				}
				return ObservedState{}, runErr
			}
			return obs, nil
		})
		if descErr != nil || descResult.Arn == "" {
			policyExists = false
		} else {
			state.Observed = descResult
		}
	}

	if !policyExists {
		created, runErr := restate.Run(ctx, func(rc restate.RunContext) (IAMPolicyOutputs, error) {
			arn, policyID, createErr := api.CreatePolicy(rc, spec)
			if createErr != nil {
				if IsAlreadyExists(createErr) {
					return IAMPolicyOutputs{}, restate.TerminalError(createErr, 409)
				}
				if IsMalformedPolicy(createErr) {
					return IAMPolicyOutputs{}, restate.TerminalError(createErr, 400)
				}
				return IAMPolicyOutputs{}, createErr
			}
			return IAMPolicyOutputs{Arn: arn, PolicyId: policyID, PolicyName: spec.PolicyName}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMPolicyOutputs{}, runErr
		}
		outputs = created
	} else {
		if correctionErr := d.correctDrift(ctx, api, outputs.Arn, spec, state.Observed); correctionErr != nil {
			state.Status = types.StatusError
			state.Error = correctionErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return IAMPolicyOutputs{}, correctionErr
		}
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribePolicy(rc, outputs.Arn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		state.Outputs = outputs
		restate.Set(ctx, drivers.StateKey, state)
		return IAMPolicyOutputs{}, err
	}

	outputs = outputsFromObserved(observed)
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Import adopts an existing AWS IAM policy into Praxis management by looking it up by name.
func (d *IAMPolicyDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (IAMPolicyOutputs, error) {
	ctx.Log().Info("importing iam policy", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return IAMPolicyOutputs{}, restate.TerminalError(err, 400)
	}

	mode := ref.Mode
	if mode == "" {
		mode = types.ModeObserved
	}

	state, err := restate.Get[IAMPolicyState](ctx, drivers.StateKey)
	if err != nil {
		return IAMPolicyOutputs{}, err
	}
	state.Generation++

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribePolicyByName(rc, ref.ResourceID, "")
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: policy %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		return IAMPolicyOutputs{}, err
	}

	spec := specFromObserved(observed)
	spec.Account = ref.Account
	outputs := outputsFromObserved(observed)

	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputs
	state.Status = types.StatusReady
	state.Mode = mode
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	d.scheduleReconcile(ctx, &state)
	return outputs, nil
}

// Delete removes an IAM policy. It detaches the policy from all principals, deletes all
// non-default versions, and then deletes the policy itself. Idempotent against NotFound.
func (d *IAMPolicyDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting iam policy", "key", restate.Key(ctx))
	state, err := restate.Get[IAMPolicyState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete iam policy in Observed mode; re-import with --mode managed to allow deletion"), 409)
	}

	policyArn := state.Outputs.Arn
	if policyArn == "" {
		policyArn = state.Observed.Arn
	}
	if policyArn == "" {
		restate.Set(ctx, drivers.StateKey, IAMPolicyState{Status: types.StatusDeleted})
		return nil
	}

	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}

	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		if runErr := api.DetachAllPrincipals(rc, policyArn); runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		versions, runErr := api.ListPolicyVersions(rc, policyArn)
		if runErr != nil && !IsNotFound(runErr) {
			return restate.Void{}, runErr
		}
		for _, version := range versions {
			if version.IsDefaultVersion {
				continue
			}
			if deleteErr := api.DeletePolicyVersion(rc, policyArn, version.VersionID); deleteErr != nil && !IsNotFound(deleteErr) {
				return restate.Void{}, deleteErr
			}
		}
		if runErr := api.DeletePolicy(rc, policyArn); runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsDeleteConflict(runErr) {
				return restate.Void{}, restate.TerminalError(runErr, 409)
			}
			return restate.Void{}, runErr
		}
		return restate.Void{}, nil
	})
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return err
	}

	restate.Set(ctx, drivers.StateKey, IAMPolicyState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-detection handler for IAM policies.
func (d *IAMPolicyDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[IAMPolicyState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}

	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	policyArn := state.Outputs.Arn
	if policyArn == "" {
		policyArn = state.Observed.Arn
	}
	if policyArn == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return types.ReconcileResult{}, err
	}

	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, runErr := api.DescribePolicy(rc, policyArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return ObservedState{}, restate.TerminalError(runErr, 404)
			}
			return ObservedState{}, runErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("policy %s was deleted externally", policyArn)
			state.LastReconcile = now
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventExternalDelete, state.Error)
			return types.ReconcileResult{Error: state.Error}, nil
		}
		state.LastReconcile = now
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Error: err.Error()}, nil
	}

	state.Observed = observed
	state.LastReconcile = now
	drift := HasDrift(state.Desired, observed)

	if state.Status == types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		return types.ReconcileResult{Drift: drift, Correcting: false}, nil
	}

	if drift && state.Mode == types.ModeManaged {
		ctx.Log().Info("drift detected, correcting iam policy", "policyArn", policyArn)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, policyArn, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		refreshed, refreshErr := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribePolicy(rc, policyArn)
		})
		if refreshErr == nil {
			state.Observed = refreshed
			state.Outputs = outputsFromObserved(refreshed)
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}

	if drift && state.Mode == types.ModeObserved {
		ctx.Log().Info("drift detected (observed mode, not correcting)", "policyArn", policyArn)
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true, Correcting: false}, nil
	}

	state.Outputs = outputsFromObserved(observed)
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *IAMPolicyDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[IAMPolicyState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *IAMPolicyDriver) GetOutputs(ctx restate.ObjectSharedContext) (IAMPolicyOutputs, error) {
	state, err := restate.Get[IAMPolicyState](ctx, drivers.StateKey)
	if err != nil {
		return IAMPolicyOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *IAMPolicyDriver) correctDrift(ctx restate.ObjectContext, api IAMPolicyAPI, policyArn string, desired IAMPolicySpec, observed ObservedState) error {
	if !policyDocumentsEqual(desired.PolicyDocument, observed.PolicyDocument) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			if runErr := api.CreatePolicyVersion(rc, policyArn, desired.PolicyDocument); runErr != nil {
				if IsMalformedPolicy(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 400)
				}
				if IsVersionLimitExceeded(runErr) {
					return restate.Void{}, restate.TerminalError(runErr, 409)
				}
				return restate.Void{}, runErr
			}
			return restate.Void{}, nil
		})
		if err != nil {
			return fmt.Errorf("update policy document: %w", err)
		}
	}

	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, policyArn, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update policy tags: %w", err)
		}
	}
	return nil
}

func (d *IAMPolicyDriver) scheduleReconcile(ctx restate.ObjectContext, state *IAMPolicyState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").
		Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *IAMPolicyDriver) apiForAccount(ctx restate.ObjectContext, account string) (IAMPolicyAPI, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, fmt.Errorf("iam policy driver is not configured")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve IAM account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), nil
}

func applyDefaults(spec IAMPolicySpec) IAMPolicySpec {
	if spec.Path == "" {
		spec.Path = "/"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	return spec
}

func specFromObserved(obs ObservedState) IAMPolicySpec {
	return IAMPolicySpec{
		Path:           obs.Path,
		PolicyName:     obs.PolicyName,
		PolicyDocument: normalizePolicyDocument(obs.PolicyDocument),
		Description:    obs.Description,
		Tags:           filterPraxisTags(obs.Tags),
	}
}

func outputsFromObserved(obs ObservedState) IAMPolicyOutputs {
	return IAMPolicyOutputs{Arn: obs.Arn, PolicyId: obs.PolicyId, PolicyName: obs.PolicyName}
}
