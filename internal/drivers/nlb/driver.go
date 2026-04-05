// Package nlb – driver.go
//
// This file implements the Restate Virtual Object handler for AWS Network Load Balancer (NLB).
// The driver exposes five durable handlers:
//   - Provision: create-or-update the resource and persist state
//   - Import:    adopt an existing AWS resource into Praxis management
//   - Delete:    remove the resource from AWS (managed mode only)
//   - Reconcile: periodic drift check + auto-correction (managed mode)
//   - GetStatus / GetOutputs: read-only shared handlers for status queries
//
// All mutating AWS calls are wrapped in restate.Run for durable execution,
// and reconciliation is self-scheduled via delayed Restate messages.
package nlb

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

// NLBDriver is the Restate Virtual Object handler for AWS Network Load Balancer (NLB).
// It holds an auth client (for cross-account credential resolution)
// and an API factory (swappable for testing).
type NLBDriver struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) NLBAPI
}

// NewNLBDriver creates a NLB driver wired to the given
// auth client. It uses the default AWS SDK client factory.
func NewNLBDriver(auth authservice.AuthClient) *NLBDriver {
	return NewNLBDriverWithFactory(auth, func(cfg aws.Config) NLBAPI {
		return NewNLBAPI(awsclient.NewELBv2Client(cfg))
	})
}

// NewNLBDriverWithFactory creates a NLB driver with a custom API
// factory, primarily used in tests to inject mock AWS clients.
func NewNLBDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) NLBAPI) *NLBDriver {
	if factory == nil {
		factory = func(cfg aws.Config) NLBAPI {
			return NewNLBAPI(awsclient.NewELBv2Client(cfg))
		}
	}
	return &NLBDriver{auth: auth, apiFactory: factory}
}

// ServiceName returns the Restate Virtual Object service name for registration.
func (d *NLBDriver) ServiceName() string { return ServiceName }

// Provision creates or updates a AWS Network Load Balancer (NLB). It validates the spec,
// checks for an existing resource (by ARN or name), detects immutable-field
// conflicts, and either creates a new resource or corrects drift on the
// existing one. State is persisted in Restate K/V after every step.
func (d *NLBDriver) Provision(ctx restate.ObjectContext, spec NLBSpec) (NLBOutputs, error) {
	ctx.Log().Info("provisioning NLB", "key", restate.Key(ctx))
	api, _, err := d.apiForAccount(ctx, spec.Account)
	if err != nil {
		return NLBOutputs{}, restate.TerminalError(err, 400)
	}
	spec = applyDefaults(spec)
	if err := validateSpec(spec); err != nil {
		return NLBOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
	if err != nil {
		return NLBOutputs{}, err
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
		return NLBOutputs{}, err
	}
	if found && hasImmutableChange(spec, current) {
		err := fmt.Errorf("NLB %q requires replacement because immutable fields changed (scheme); delete and re-apply to recreate it", spec.Name)
		state.Observed = current
		state.Outputs = outputsFromObserved(current)
		state.Status = types.StatusError
		state.Error = err.Error()
		restate.Set(ctx, drivers.StateKey, state)
		return NLBOutputs{}, restate.TerminalError(err, 409)
	}

	if !found {
		outputs, runErr := restate.Run(ctx, func(rc restate.RunContext) (NLBOutputs, error) {
			arn, dnsName, hostedZoneId, vpcId, createErr := api.CreateNLB(rc, spec)
			if createErr != nil {
				if IsDuplicate(createErr) {
					return NLBOutputs{}, restate.TerminalError(createErr, 409)
				}
				if IsTooMany(createErr) {
					return NLBOutputs{}, restate.TerminalError(createErr, 503)
				}
				return NLBOutputs{}, createErr
			}
			return NLBOutputs{
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
			return NLBOutputs{}, runErr
		}
		current, err = d.waitForActive(ctx, api, outputs.LoadBalancerArn)
		if err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Outputs = outputs
			restate.Set(ctx, drivers.StateKey, state)
			return NLBOutputs{}, err
		}
	} else {
		if err := d.correctDrift(ctx, api, current.LoadBalancerArn, spec, current); err != nil {
			state.Status = types.StatusError
			state.Error = err.Error()
			state.Observed = current
			state.Outputs = outputsFromObserved(current)
			restate.Set(ctx, drivers.StateKey, state)
			return NLBOutputs{}, err
		}
		current, err = restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeNLB(rc, current.LoadBalancerArn)
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
			return NLBOutputs{}, err
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

// Import adopts an existing AWS Network Load Balancer (NLB) into Praxis management.
// It reads the current configuration from AWS, synthesizes a spec from
// the observed state, and stores it. Default import mode is Observed
// (read-only); users can re-import with --mode managed to enable writes.
func (d *NLBDriver) Import(ctx restate.ObjectContext, ref types.ImportRef) (NLBOutputs, error) {
	ctx.Log().Info("importing NLB", "resourceId", ref.ResourceID, "mode", ref.Mode)
	api, region, err := d.apiForAccount(ctx, ref.Account)
	if err != nil {
		return NLBOutputs{}, restate.TerminalError(err, 400)
	}
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
	if err != nil {
		return NLBOutputs{}, err
	}
	state.Generation++
	observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
		obs, descErr := api.DescribeNLB(rc, ref.ResourceID)
		if descErr != nil {
			if IsNotFound(descErr) {
				return ObservedState{}, restate.TerminalError(fmt.Errorf("import failed: NLB %s does not exist", ref.ResourceID), 404)
			}
			return ObservedState{}, descErr
		}
		return obs, nil
	})
	if err != nil {
		return NLBOutputs{}, err
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

// Delete removes the AWS Network Load Balancer (NLB) from AWS. It is blocked for
// resources in Observed mode. The method handles not-found gracefully
// (idempotent delete) and sets the final state to StatusDeleted.
func (d *NLBDriver) Delete(ctx restate.ObjectContext) error {
	ctx.Log().Info("deleting NLB", "key", restate.Key(ctx))
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
	if err != nil {
		return err
	}
	if state.Status == types.StatusDeleted {
		return nil
	}
	if state.Mode == types.ModeObserved {
		return restate.TerminalError(fmt.Errorf("cannot delete NLB %s: resource is in Observed mode; re-import with --mode managed to allow deletion", state.Outputs.LoadBalancerArn), 409)
	}
	if state.Outputs.LoadBalancerArn == "" {
		restate.Set(ctx, drivers.StateKey, NLBState{Status: types.StatusDeleted})
		return nil
	}
	api, _, err := d.apiForAccount(ctx, state.Desired.Account)
	if err != nil {
		return restate.TerminalError(err, 400)
	}
	state.Status = types.StatusDeleting
	state.Error = ""
	restate.Set(ctx, drivers.StateKey, state)

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
		runErr := api.DeleteNLB(rc, state.Outputs.LoadBalancerArn)
		if runErr != nil {
			if IsNotFound(runErr) {
				return restate.Void{}, nil
			}
			if IsResourceInUse(runErr) {
				return restate.Void{}, restate.TerminalError(fmt.Errorf("NLB %s is still in use", state.Outputs.LoadBalancerArn), 409)
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
	restate.Set(ctx, drivers.StateKey, NLBState{Status: types.StatusDeleted})
	return nil
}

// Reconcile is the periodic drift-check handler. It re-reads the
// resource from AWS, compares against desired state, and auto-corrects
// drift when in Managed mode. In Observed mode it only reports drift.
// External deletions are detected and flagged as errors.
// The handler self-schedules via a delayed Restate message.
func (d *NLBDriver) Reconcile(ctx restate.ObjectContext) (types.ReconcileResult, error) {
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
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
		obs, descErr := api.DescribeNLB(rc, state.Outputs.LoadBalancerArn)
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
			state.Error = fmt.Sprintf("NLB %s was deleted externally", state.Outputs.LoadBalancerArn)
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

// GetStatus is a shared (read-only) handler that returns the current lifecycle status.
func (d *NLBDriver) GetStatus(ctx restate.ObjectSharedContext) (types.StatusResponse, error) {
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
	if err != nil {
		return types.StatusResponse{}, err
	}
	return types.StatusResponse{Status: state.Status, Mode: state.Mode, Generation: state.Generation, Error: state.Error}, nil
}

// GetOutputs is a shared (read-only) handler that returns the provisioned resource outputs.
func (d *NLBDriver) GetOutputs(ctx restate.ObjectSharedContext) (NLBOutputs, error) {
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
	if err != nil {
		return NLBOutputs{}, err
	}
	return state.Outputs, nil
}

// GetInputs is a shared (read-only) handler that returns the desired input spec.
func (d *NLBDriver) GetInputs(ctx restate.ObjectSharedContext) (NLBSpec, error) {
	state, err := restate.Get[NLBState](ctx, drivers.StateKey)
	if err != nil {
		return NLBSpec{}, err
	}
	return state.Desired, nil
}

func (d *NLBDriver) correctDrift(ctx restate.ObjectContext, api NLBAPI, arn string, desired NLBSpec, observed ObservedState) error {
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
	attrs := buildAttributeMap(desired)
	observedAttrs := map[string]string{
		"deletion_protection.enabled":       fmt.Sprintf("%t", observed.DeletionProtection),
		"load_balancing.cross_zone.enabled": fmt.Sprintf("%t", observed.CrossZoneLoadBalancing),
	}
	if !mapsEqual(attrs, observedAttrs) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyAttributes(rc, arn, attrs)
		})
		if err != nil {
			return fmt.Errorf("modify attributes: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		_, err := restate.Run(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, arn, desired.Tags)
		})
		if err != nil {
			return fmt.Errorf("update tags: %w", err)
		}
	}
	return nil
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

func (d *NLBDriver) waitForActive(ctx restate.ObjectContext, api NLBAPI, arn string) (ObservedState, error) {
	for {
		observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			return api.DescribeNLB(rc, arn)
		})
		if err != nil {
			return ObservedState{}, err
		}
		if observed.State == "active" {
			return observed, nil
		}
		if observed.State == "failed" {
			return ObservedState{}, restate.TerminalError(fmt.Errorf("NLB entered failed state"), 500)
		}
		if err := restate.Sleep(ctx, 10*time.Second); err != nil {
			return ObservedState{}, err
		}
	}
}

func (d *NLBDriver) scheduleReconcile(ctx restate.ObjectContext, state *NLBState) {
	if state.ReconcileScheduled {
		return
	}
	state.ReconcileScheduled = true
	restate.Set(ctx, drivers.StateKey, *state)
	restate.ObjectSend(ctx, ServiceName, restate.Key(ctx), "Reconcile").Send(restate.Void{}, restate.WithDelay(drivers.ReconcileInterval))
}

func (d *NLBDriver) apiForAccount(ctx restate.ObjectContext, account string) (NLBAPI, string, error) {
	if d == nil || d.auth == nil || d.apiFactory == nil {
		return nil, "", fmt.Errorf("NLBDriver is not configured with an auth registry")
	}
	awsCfg, err := d.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve NLB account %q: %w", account, err)
	}
	return d.apiFactory(awsCfg), awsCfg.Region, nil
}

func (d *NLBDriver) lookupCurrent(ctx restate.ObjectContext, api NLBAPI, arn, name string) (ObservedState, bool, error) {
	if arn != "" {
		observed, err := restate.Run(ctx, func(rc restate.RunContext) (ObservedState, error) {
			obs, descErr := api.DescribeNLB(rc, arn)
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
		obs, descErr := api.DescribeNLB(rc, name)
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

func applyDefaults(spec NLBSpec) NLBSpec {
	if spec.Scheme == "" {
		spec.Scheme = "internet-facing"
	}
	if spec.IpAddressType == "" {
		spec.IpAddressType = "ipv4"
	}
	if spec.Tags == nil {
		spec.Tags = map[string]string{}
	}
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Region = strings.TrimSpace(spec.Region)
	return spec
}

func validateSpec(spec NLBSpec) error {
	if spec.Region == "" {
		return fmt.Errorf("region is required")
	}
	if spec.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(spec.Subnets) == 0 && len(spec.SubnetMappings) == 0 {
		return fmt.Errorf("at least 1 subnet is required")
	}
	return nil
}

func hasImmutableChange(desired NLBSpec, observed ObservedState) bool {
	return desired.Scheme != observed.Scheme
}

func specFromObserved(observed ObservedState) NLBSpec {
	return applyDefaults(NLBSpec{
		Name:                   observed.Name,
		Scheme:                 observed.Scheme,
		IpAddressType:          observed.IpAddressType,
		Subnets:                observed.Subnets,
		CrossZoneLoadBalancing: observed.CrossZoneLoadBalancing,
		DeletionProtection:     observed.DeletionProtection,
		Tags:                   drivers.FilterPraxisTags(observed.Tags),
	})
}

func outputsFromObserved(observed ObservedState) NLBOutputs {
	return NLBOutputs{
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
