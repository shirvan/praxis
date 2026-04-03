// resource_event_bridge.go implements the bridge between driver-level resource
// events and the orchestrator's deployment-scoped event pipeline.
//
// Drivers operate with resource keys (e.g. "aws/us-east-1/s3/my-bucket") but
// have no knowledge of deployments. When a driver detects drift, it reports the
// event through ResourceEventBridge with the resource key. The bridge looks up
// the ResourceEventOwner mapping (maintained by DeploymentStateObj.InitDeployment)
// to find the deployment key, workspace, and resource name, then re-emits the
// event as a deployment-scoped CloudEvent through the standard EventBus pipeline.
package orchestrator

import (
	"fmt"
	"strings"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/eventing"
)

// resourceEventOwnerStateKey is the Restate state key used by
// ResourceEventOwnerObj to store the ownership mapping.
const resourceEventOwnerStateKey = "owner"

// ResourceEventOwnerObj is a Restate Virtual Object keyed by resource key
// (e.g. "aws/us-east-1/s3/my-bucket"). It maps a driver-visible resource key
// to its owning deployment's stream key, workspace, generation, resource name,
// and resource kind.
type ResourceEventOwnerObj struct{}

// ServiceName returns the Restate service name for the resource event owner object.
func (ResourceEventOwnerObj) ServiceName() string {
	return eventing.ResourceEventOwnerServiceName
}

// Upsert creates or updates the ownership mapping for a resource key.
// Called by DeploymentStateObj.InitDeployment during deployment initialisation.
func (ResourceEventOwnerObj) Upsert(ctx restate.ObjectContext, owner eventing.ResourceEventOwner) error {
	owner.ResourceKey = strings.TrimSpace(restate.Key(ctx))
	owner.StreamKey = strings.TrimSpace(owner.StreamKey)
	owner.ResourceName = strings.TrimSpace(owner.ResourceName)
	owner.ResourceKind = strings.TrimSpace(owner.ResourceKind)
	if owner.StreamKey == "" {
		return restate.TerminalError(fmt.Errorf("stream key is required"), 400)
	}
	if owner.ResourceName == "" {
		return restate.TerminalError(fmt.Errorf("resource name is required"), 400)
	}
	if owner.ResourceKind == "" {
		return restate.TerminalError(fmt.Errorf("resource kind is required"), 400)
	}
	restate.Set(ctx, resourceEventOwnerStateKey, owner)
	return nil
}

// Get returns the current ownership mapping for this resource key, or nil if
// the resource is not tracked by any deployment.
func (ResourceEventOwnerObj) Get(ctx restate.ObjectSharedContext, _ restate.Void) (*eventing.ResourceEventOwner, error) {
	return restate.Get[*eventing.ResourceEventOwner](ctx, resourceEventOwnerStateKey)
}

// Delete removes the ownership mapping. Called when a resource is removed from
// a deployment (during re-apply cleanup, delete, or state mv).
func (ResourceEventOwnerObj) Delete(ctx restate.ObjectContext, _ restate.Void) error {
	restate.Clear(ctx, resourceEventOwnerStateKey)
	return nil
}

// ResourceEventBridge is a stateless Restate Service that translates
// driver-reported events (keyed by resource key) into deployment-scoped
// CloudEvents (keyed by deployment key).
type ResourceEventBridge struct{}

// ServiceName returns the Restate service name for the resource event bridge.
func (ResourceEventBridge) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

// ReportDrift receives a drift report from a driver, looks up the resource's
// owning deployment via ResourceEventOwnerObj, and emits the appropriate drift
// CloudEvent (detected, corrected, or external_delete) through the EventBus.
//
// If the resource has no owner mapping (e.g. it was removed from all deployments),
// the event is silently dropped with a warning log.
func (ResourceEventBridge) ReportDrift(ctx restate.Context, req eventing.DriftReportRequest) error {
	resourceKey := strings.TrimSpace(req.ResourceKey)
	if resourceKey == "" {
		return restate.TerminalError(fmt.Errorf("resource key is required"), 400)
	}
	owner, err := restate.Object[*eventing.ResourceEventOwner](ctx, eventing.ResourceEventOwnerServiceName, resourceKey, "Get").Request(restate.Void{})
	if err != nil {
		return err
	}
	if owner == nil {
		ctx.Log().Warn("skipping drift event without ownership mapping", "resourceKey", resourceKey, "eventType", req.EventType)
		return nil
	}

	resourceKind := strings.TrimSpace(req.ResourceKind)
	if resourceKind == "" {
		resourceKind = owner.ResourceKind
	}

	var event cloudevents.Event
	var buildErr error
	streamKey := owner.StreamKey
	workspace := owner.Workspace
	generation := owner.Generation
	resourceName := owner.ResourceName

	switch strings.TrimSpace(req.EventType) {
	case eventing.DriftEventDetected:
		event, buildErr = NewDriftDetectedEvent(streamKey, workspace, generation, resourceName, resourceKind, req.Error)
	case eventing.DriftEventCorrected:
		event, buildErr = NewDriftCorrectedEvent(streamKey, workspace, generation, resourceName, resourceKind)
	case eventing.DriftEventExternalDelete:
		event, buildErr = NewDriftExternalDeleteEvent(streamKey, workspace, generation, resourceName, resourceKind, req.Error)
	default:
		return restate.TerminalError(fmt.Errorf("unsupported drift event type %q", req.EventType), 400)
	}
	if buildErr != nil {
		return buildErr
	}
	return EmitCloudEvent(ctx, event)
}
