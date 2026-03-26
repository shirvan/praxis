package orchestrator

import (
	"fmt"
	"strings"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/eventing"
)

const resourceEventOwnerStateKey = "owner"

type ResourceEventOwnerObj struct{}

func (ResourceEventOwnerObj) ServiceName() string {
	return eventing.ResourceEventOwnerServiceName
}

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

func (ResourceEventOwnerObj) Get(ctx restate.ObjectSharedContext, _ restate.Void) (*eventing.ResourceEventOwner, error) {
	return restate.Get[*eventing.ResourceEventOwner](ctx, resourceEventOwnerStateKey)
}

func (ResourceEventOwnerObj) Delete(ctx restate.ObjectContext, _ restate.Void) error {
	restate.Clear(ctx, resourceEventOwnerStateKey)
	return nil
}

type ResourceEventBridge struct{}

func (ResourceEventBridge) ServiceName() string {
	return eventing.ResourceEventBridgeServiceName
}

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
