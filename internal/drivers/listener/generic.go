package listener

import (
	"fmt"
	"maps"
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
	apiFactory func(aws.Config) ListenerAPI
}

func NewGenericListenerDriver(auth authservice.AuthClient) *kernel.Driver[ListenerSpec, ListenerOutputs, ObservedState] {
	return newGenericListenerDriverWithFactory(auth, nil)
}
func newGenericListenerDriverWithFactory(auth authservice.AuthClient, f func(aws.Config) ListenerAPI) *kernel.Driver[ListenerSpec, ListenerOutputs, ObservedState] {
	if f == nil {
		f = func(c aws.Config) ListenerAPI { return NewListenerAPI(awsclient.NewELBv2Client(c)) }
	}
	o := &genericOperations{auth: auth, apiFactory: f}
	return kernel.MustNew(kernel.Descriptor[ListenerSpec, ListenerOutputs, ObservedState]{ServiceName: ServiceName, Capabilities: kernel.Capabilities{Declared: true, Import: true, ObservedMode: true, Delete: true, ManagedDriftCorrection: true}, Operations: o, Prepare: func(ctx restate.ObjectContext, s ListenerSpec) (ListenerSpec, error) {
		_, r, e := o.apiForAccount(ctx, s.Account)
		if e != nil {
			return ListenerSpec{}, drivers.ClassifyCredentialError(e)
		}
		s.Region = r
		s.ManagedKey = restate.Key(ctx)
		s.Tags = drivers.FilterPraxisTags(s.Tags)
		return s, nil
	}, Validate: validateSpec, DesiredFromObserved: func(ref types.ImportRef, obs ObservedState) ListenerSpec {
		s := specFromObserved(obs)
		s.Account = ref.Account
		return s
	}, OutputsFromObserved: func(o ObservedState, _ ListenerOutputs) ListenerOutputs { return outputsFromObserved(o) }, FieldDiffs: ComputeFieldDiffs,
		HasDrift: HasDrift})
}
func (o *genericOperations) Observe(ctx restate.ObjectContext, d ListenerSpec, out ListenerOutputs) (kernel.Observation[ObservedState], error) {
	api, _, e := o.apiForAccount(ctx, d.Account)
	if e != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(e)
	}
	if out.ListenerArn != "" {
		return observeListener(ctx, api, out.ListenerArn)
	}
	if d.LoadBalancerArn == "" || d.Port == 0 {
		return kernel.Observation[ObservedState]{}, nil
	}
	obs, e := findListener(ctx, api, d.LoadBalancerArn, d.Port)
	if e != nil || !obs.Exists {
		return obs, e
	}
	owner := obs.Value.Tags["praxis:managed-key"]
	if owner != d.ManagedKey {
		return kernel.Observation[ObservedState]{}, restate.TerminalError(fmt.Errorf("refusing to adopt listener on port %d without exact Praxis ownership tag %q", d.Port, d.ManagedKey), 409)
	}
	return obs, nil
}
func (o *genericOperations) Create(ctx restate.ObjectContext, d ListenerSpec) (kernel.CreateResult[ListenerOutputs], error) {
	api, _, e := o.apiForAccount(ctx, d.Account)
	if e != nil {
		return kernel.CreateResult[ListenerOutputs]{}, drivers.ClassifyCredentialError(e)
	}
	arn, e := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		obs, findErr := api.FindListenerByPort(rc, d.LoadBalancerArn, d.Port)
		if findErr == nil {
			if obs.Tags["praxis:managed-key"] != d.ManagedKey {
				return "", restate.TerminalError(fmt.Errorf("refusing to adopt listener without exact Praxis ownership tag %q", d.ManagedKey), 409)
			}
			return obs.ListenerArn, nil
		}
		if !IsNotFound(findErr) {
			return "", findErr
		}
		return api.CreateListener(rc, d)
	}, classifyListenerMutation)
	return kernel.CreateResult[ListenerOutputs]{SeedOutputs: ListenerOutputs{ListenerArn: arn, Port: d.Port, Protocol: d.Protocol}}, e
}
func (o *genericOperations) Converge(ctx restate.ObjectContext, d ListenerSpec, obs ObservedState) error {
	if d.LoadBalancerArn != obs.LoadBalancerArn {
		return restate.TerminalError(fmt.Errorf("loadBalancerArn is immutable; delete and reprovision the listener"), 409)
	}
	if owner := obs.Tags["praxis:managed-key"]; owner != "" && owner != d.ManagedKey {
		return restate.TerminalError(fmt.Errorf("listener is owned by Praxis object %q", owner), 409)
	}
	api, _, e := o.apiForAccount(ctx, d.Account)
	if e != nil {
		return drivers.ClassifyCredentialError(e)
	}
	modify := d.Port != obs.Port || !strings.EqualFold(d.Protocol, obs.Protocol) || effectiveSslPolicy(d.SslPolicy, d.Protocol) != obs.SslPolicy || d.CertificateArn != obs.CertificateArn || d.AlpnPolicy != obs.AlpnPolicy || !actionsEqual(d.DefaultActions, obs.DefaultActions)
	if modify {
		if _, e = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.ModifyListener(rc, obs.ListenerArn, d)
		}, classifyListenerMutation); e != nil {
			return e
		}
	}
	if !drivers.TagsMatch(d.Tags, obs.Tags) || obs.Tags["praxis:managed-key"] != d.ManagedKey {
		_, e = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, obs.ListenerArn, listenerManagedTags(d.Tags, d.ManagedKey))
		}, classifyListenerMutation)
	}
	return e
}
func (o *genericOperations) Delete(ctx restate.ObjectContext, d ListenerSpec, out ListenerOutputs) error {
	if out.ListenerArn == "" {
		return nil
	}
	api, _, e := o.apiForAccount(ctx, d.Account)
	if e != nil {
		return drivers.ClassifyCredentialError(e)
	}
	obs, e := observeListener(ctx, api, out.ListenerArn)
	if e != nil || !obs.Exists {
		return e
	}
	if owner := obs.Value.Tags["praxis:managed-key"]; owner != "" && owner != d.ManagedKey {
		return restate.TerminalError(fmt.Errorf("refusing to delete listener owned by %q", owner), 409)
	}
	_, e = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		x := api.DeleteListener(rc, out.ListenerArn)
		if IsNotFound(x) {
			x = nil
		}
		return restate.Void{}, x
	}, classifyListenerMutation)
	return e
}
func (o *genericOperations) Import(ctx restate.ObjectContext, r types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, e := o.apiForAccount(ctx, r.Account)
	if e != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(e)
	}
	return observeListener(ctx, api, r.ResourceID)
}
func observeListener(ctx restate.ObjectContext, api ListenerAPI, arn string) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		o, e := api.DescribeListener(rc, arn)
		if IsNotFound(e) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if e != nil {
			return kernel.Observation[ObservedState]{}, e
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: o}, nil
	}, classifyListenerObserve)
}
func findListener(ctx restate.ObjectContext, api ListenerAPI, lb string, p int) (kernel.Observation[ObservedState], error) {
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		o, e := api.FindListenerByPort(rc, lb, p)
		if IsNotFound(e) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if e != nil {
			return kernel.Observation[ObservedState]{}, e
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: o}, nil
	}, classifyListenerObserve)
}
func (o *genericOperations) apiForAccount(ctx restate.Context, a string) (ListenerAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("listener driver is not configured with an auth client")
	}
	c, e := o.auth.GetCredentials(ctx, a)
	if e != nil {
		return nil, "", fmt.Errorf("resolve listener account %q: %w", a, e)
	}
	return o.apiFactory(c), c.Region, nil
}
func classifyListenerObserve(e error) error {
	if e == nil || IsNotFound(e) {
		return e
	}
	if awserr.IsAccessDenied(e) {
		return restate.TerminalError(e, 403)
	}
	if IsInvalidConfig(e) || awserr.IsValidation(e) {
		return restate.TerminalError(e, 400)
	}
	return e
}
func classifyListenerMutation(e error) error {
	if e == nil || restate.IsTerminalError(e) {
		return e
	}
	if awserr.IsAccessDenied(e) {
		return restate.TerminalError(e, 403)
	}
	if IsDuplicate(e) {
		return restate.TerminalError(e, 409)
	}
	if IsTargetGroupNotFound(e) || IsCertificateNotFound(e) || IsInvalidConfig(e) || awserr.IsValidation(e) {
		return restate.TerminalError(e, 400)
	}
	if IsTooMany(e) {
		return restate.TerminalError(e, 503)
	}
	return e
}
func listenerManagedTags(t map[string]string, k string) map[string]string {
	o := map[string]string{}
	maps.Copy(o, drivers.FilterPraxisTags(t))
	if k != "" {
		o["praxis:managed-key"] = k
	}
	return o
}
func validateSpec(s ListenerSpec) error {
	if s.LoadBalancerArn == "" {
		return fmt.Errorf("loadBalancerArn is required")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if s.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}
	if requiresSSL(s.Protocol) && s.CertificateArn == "" {
		return fmt.Errorf("certificateArn is required for %s listeners", s.Protocol)
	}
	if len(s.DefaultActions) == 0 {
		return fmt.Errorf("at least one default action is required")
	}
	return nil
}
func specFromObserved(o ObservedState) ListenerSpec {
	return ListenerSpec{LoadBalancerArn: o.LoadBalancerArn, Port: o.Port, Protocol: o.Protocol, SslPolicy: o.SslPolicy, CertificateArn: o.CertificateArn, AlpnPolicy: o.AlpnPolicy, DefaultActions: o.DefaultActions, Tags: drivers.FilterPraxisTags(o.Tags)}
}
func outputsFromObserved(o ObservedState) ListenerOutputs {
	return ListenerOutputs{ListenerArn: o.ListenerArn, Port: o.Port, Protocol: o.Protocol}
}
