package alb

import (
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/eventing"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type ALBDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) ALBAPI
}

func NewALBDriver(auth authservice.AuthClient) *ALBDriver {
	return NewALBDriverWithFactory(auth, func(cfg aws.Config) ALBAPI {
		return NewALBAPI(awsclient.NewELBv2Client(cfg))
	})
}

func NewALBDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) ALBAPI) *ALBDriver {
	if factory == nil {
		factory = func(cfg aws.Config) ALBAPI {
			return NewALBAPI(awsclient.NewELBv2Client(cfg))
		}
	}
	return &ALBDriver{auth: auth, apiFactory: factory}
}

func (d *ALBDriver) ServiceName() string { return ServiceName }

func (d *ALBDriver) Provision(ctx restate.ObjectContext, spec ALBSpec) (ALBOutputs, error) {
	ctx.Log().Info("provisioning ALB", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return ALBOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return ALBOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ALBState](ctx, drivers.StateKey)
	if err != nil {
		return ALBOutputs{}, err
	}
	state.Desired = spec
	state.Status = types.StatusProvisioning
	state.Mode = types.ModeManaged
	state.Error = ""
	state.Generation++

	current, found, err := d.lookupCurrent(ctx, api, state.Outputs.LoadBalancerArn, spec.Name)
	if err != nil {
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return ALBOutputs{}, err
	}
	if found && hasImmutableChange(spec, current) {
		err := fmt.Errorf("ALB %q requires replacement because immutable fields changed (scheme); delete and re-apply to recreate it", spec.Name)
		state.Observed = current
		state.Outputs = outputsFromObserved(current)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return ALBOutputs{}, restate.TerminalError(err, 409)
	}

	if !found {
		outputs, runErr := restate.Run(ctx, func(rc restate.RunContext) (ALBOutputs, error) {
			arn, dnsName, hostedZoneId, vpcId, createErr := api.CreateALB(rc, spec)
			if createErr != nil {
				if IsDuplicate(createErr) || IsInvalidConfig(createErr) {
					return ALBOutputs{}, restate.TerminalError(createErr, 409)
				}
				if IsTooMany(createErr) {
					return ALBOutputs{}, restate.TerminalError(createErr, 503)
				}
				return ALBOutputs{}, createErr
			}
			return ALBOutputs{
				LoadBalancerArn:       arn,
				DnsName:               dnsName,
				HostedZoneId:          hostedZoneId,
				VpcId:                 vpcId,
				CanonicalHostedZoneId: hostedZoneId,
			}, nil
		})
		if runErr != nil {
			state.Status = types.StatusError
			state.Error = runErr.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ALBOutputs{}, runErr
		}
		// Wait for ALB to become active
		current, err = d.waitForActive(ctx, api, outputs.LoadBalancerArn)
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = outputs
			restate.Set(ctx, drivers.StateKey, state)
			return ALBOutputs{}, err
		}
	} else {
		if err := d.correctDrift(ctx, api, current.LoadBalancerArn, spec, current); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Observed = current
			state.Outputs = outputsFromObserved(current)
			restate.Set(ctx, drivers.StateKey, state)
			return ALBOutputs{}, err
		}
		current, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeALB(rc, current.LoadBalancerArn)
			if descErr != nil {
				if IsNotFound(descErr) {
					return ObservedState{}, restate.TerminalError(descErr, 404)
				}
				return ObservedState{}, descErr
			}
			return obs, nil
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return ALBOutputs{}, err
		}
	}

	state.Observed = current
	state.Outputs = outputsFromObserved(current)
	state.Status = types.StatusReady
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *ALBDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (ALBOutputs, error) {
	ctx.Log().Info("importing ALB", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return ALBOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[ALBState](ctx, drivers.StateKey)
	if err != nil {
		return ALBOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeALB(rc, ref.ResourceID)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: ALB %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return ALBOutputs{}, err
	}
	spec := specFromObserved(observed)
	spec.Account = ref.Account
	spec.Region = region
	state.Desired = spec
	state.Observed = observed
	state.Outputs = outputsFromObserved(observed)
	state.Status = types.StatusReady
	state.Mode = defaultImportMode(ref.Mode)
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return state.Outputs, nil
}

func (d *ALBDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting ALB", "key", restate.Key(ctx))
	state, err := restate.Get[ALBState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete ALB %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.LoadBalancerArn), 409)
	}
	if state.Outputs.LoadBalancerArn == "" {
		restate.Set(ctx, drivers.StateKey, ALBState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

	// Disable deletion protection before deleting
	if state.Observed.DeletionProtection {
		_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyAttributes(rc, state.Outputs.LoadBalancerArn, map[string]string{"deletion_protection.enabled": "false"})
		})
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			restate.Set(ctx, drivers.StateKey, state)
			return err
		}
	}
	_, err = restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
		runErr := api.DeleteALB(rc, state.Outputs.LoadBalancerArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsResourceInUse(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("ALB %s is still in use", state.Outputs.LoadBalancerArn), 409)
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
	restate.Set(ctx, drivers.StateKey, ALBState{Status: types.StatusDeleted})
	return nil
}

func (d *ALBDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[ALBState](ctx, drivers.StateKey)
	if err != nil {
		return types.ReconcileResult{}, err
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return types.ReconcileResult{}, restate.TerminalError(err, 400)
	}
	state.ReconcileScheduled = false
	if state.Status != types.StatusReady && state.Status != types.StatusError {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	if state.Outputs.LoadBalancerArn == "" {
		restate.Set(ctx, drivers.StateKey, state)
		return types.ReconcileResult{}, nil
	}
	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) { return time.Now().UTC().Format(time.RFC3339), nil })
	if err != nil {
		return types.ReconcileResult{}, err
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeALB(rc, state.Outputs.LoadBalancerArn)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(descErr, 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		if IsNotFound(err) {
			state.Status = types.StatusError
			state.Error = fmt.Sprintf("ALB %s was deleted externally", state.Outputs.LoadBalancerArn)
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
		return types.ReconcileResult{Drift: drift}, nil
	}
	if drift && state.Mode == types.ModeManaged {
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		correctionErr := d.correctDrift(ctx, api, observed.LoadBalancerArn, state.Desired, observed)
		if correctionErr != nil {
			restate.Set(ctx, drivers.StateKey, state)
			d.scheduleReconcile(ctx, &state)
			return types.ReconcileResult{Drift: true, Correcting: true, Error: correctionErr.Error()}, nil
		}
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventCorrected, "")
		return types.ReconcileResult{Drift: true, Correcting: true}, nil
	}
	if drift && state.Mode == types.ModeObserved {
		restate.Set(ctx, drivers.StateKey, state)
		d.scheduleReconcile(ctx, &state)
		drivers.ReportDriftEvent(ctx, ServiceName, eventing.DriftEventDetected, "")
		return types.ReconcileResult{Drift: true}, nil
	}
	restate.Set(ctx, drivers.StateKey, state)
	d.scheduleReconcile(ctx, &state)
	return types.ReconcileResult{}, nil
}

func (d *ALBDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[ALBState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

func (d *ALBDriver) GetOutputs(ctx restate.ObjectSharedContext) (ALBOutputs, error) {
	state, err := restate.Get[ALBState](ctx, drivers.StateKey)
	if err != nil {
		return ALBOutputs{}, err
	}
	return state.Outputs, nil
}

func (d *ALBDriver) correctDrift(ctx restate.ObjectContext, api ALBAPI, arn string, desired ALBSpec, observed ObservedState) error {
	desired = applyDefaults(desired)
	if desired.IpAddressType != observed.IpAddressType {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetIpAddressType(rc, arn, desired.IpAddressType)
		})
		if err != nil {
			return fmt.Errorf("set IP address type: %w", err)
		}
	}
	desiredSubnets := resolveSubnets(desired)
	if !sortedStringsEqual(desiredSubnets, observed.Subnets) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetSubnets(rc, arn, resolveSubnetMappings(desired))
		})
		if err != nil {
			return fmt.Errorf("set subnets: %w", err)
		}
	}
	if !sortedStringsEqual(sortedCopy(desired.SecurityGroups), observed.SecurityGroups) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.SetSecurityGroups(rc, arn, desired.SecurityGroups)
		})
		if err != nil {
			return fmt.Errorf("set security groups: %w", err)
		}
	}
	attrs := buildAttributeMap(desired)
	observedAttrs := buildAttributeMapFromObserved(observed)
	if !mapsEqual(attrs, observedAttrs) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyAttributes(rc, arn, attrs)
		})
		if err != nil {
			return fmt.Errorf("modify attributes: %w", err)
		}
	}
	if !tagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, arn, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
}

func buildAttributeMapFromObserved(observed ObservedState) map[string]string {
	spec := ALBSpec{
		DeletionProtection: observed.DeletionProtection,
		IdleTimeout:        observed.IdleTimeout,
		AccessLogs:         observed.AccessLogs,
	}
	return buildAttributeMap(spec)
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func (d *ALBDriver) waitForActive(ctx restate.ObjectContext, api ALBAPI, arn string) (ObservedState, error) {
	for {
		observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeALB(rc, arn)
		})
		if err != nil {
			return ObservedState{}, err
		}
		if observed.State == "active" {
			return observed, nil
		}
		if observed.State == "failed" {
			return ObservedState{}, restate.TerminalError(fmt.Errorf("ALB entered failed state"), 500)
		}
		if err := restate.Sleep(ctx, 10*time.Second); err != nil {
			return ObservedState{}, err
		}
	}
}

func (d *ALBDriver) scheduleReconcile(ctx restate.ObjectContext, state *ALBState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *ALBDriver) apiForAccount(ctx restate.ObjectContext, account string) (ALBAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("ALBDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ALB account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *ALBDriver) lookupCurrent(ctx restate.ObjectContext, api ALBAPI, arn, name string) (ObservedState, bool, error) {
	if arn != "" {
		observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeALB(rc, arn)
			if descErr != nil {
				if IsNotFound(descErr) {
					return ObservedState{}, nil
				}
				return ObservedState{}, descErr
			}
			return obs, nil
		})
		if err != nil {
			return ObservedState{}, false, err
		}
		if observed.LoadBalancerArn != "" {
			return observed, true, nil
		}
	}
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeALB(rc, name)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, nil
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return ObservedState{}, false, err
	}
	return observed, observed.LoadBalancerArn != "", nil
}

func applyDefaults(spec ALBSpec) ALBSpec {
	if spec.Scheme == "" {
		spec.Scheme = "internet-facing"
	}
	if spec.IpAddressType == "" {
		spec.IpAddressType = "ipv4"
	}
	if spec.IdleTimeout == 0 {
		spec.IdleTimeout = 60
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	return spec
}

func validateSpec(spec ALBSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(spec.Subnets) == 0 && len(spec.SubnetMappings) == 0 {
		return fmt.Errorf("at least 2 subnets are required")
	}
	if len(spec.Subnets) > 0 && len(spec.Subnets) < 2 {
		return fmt.Errorf("at least 2 subnets are required")
	}
	if len(spec.SubnetMappings) > 0 && len(spec.SubnetMappings) < 2 {
		return fmt.Errorf("at least 2 subnet mappings are required")
	}
	if len(spec.SecurityGroups) == 0 {
		return fmt.Errorf("at least 1 security group is required")
	}
	return nil
}

func hasImmutableChange(desired ALBSpec, observed ObservedState) bool {
	return desired.Scheme != observed.Scheme
}

func specFromObserved(observed ObservedState) ALBSpec {
	return applyDefaults(ALBSpec{
		Name:               observed.Name,
		Scheme:             observed.Scheme,
		IpAddressType:      observed.IpAddressType,
		Subnets:            observed.Subnets,
		SecurityGroups:     observed.SecurityGroups,
		AccessLogs:         observed.AccessLogs,
		DeletionProtection: observed.DeletionProtection,
		IdleTimeout:        observed.IdleTimeout,
		Tags:               filterPraxisTags(observed.Tags),
	})
}

func outputsFromObserved(observed ObservedState) ALBOutputs {
	return ALBOutputs{
		LoadBalancerArn:       observed.LoadBalancerArn,
		DnsName:               observed.DnsName,
		HostedZoneId:          observed.HostedZoneId,
		VpcId:                 observed.VpcId,
		CanonicalHostedZoneId: observed.HostedZoneId,
	}
}

func defaultImportMode(mode types.Mode) types.Mode {
	if mode == "" {
		return types.ModeObserved
	}
	return mode
}
