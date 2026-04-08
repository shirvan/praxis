// workflow.go implements the apply/re-apply deployment workflow.
//
// The workflow is a Restate durable workflow keyed by deployment. Restate
// guarantees that Run() will execute exactly once for each workflow start,
// automatically journaling completed steps so that restarts after transient
// failures resume from the last successful step rather than re-executing
// side-effecting driver calls.
package orchestrator

import (
	"fmt"
	"sort"
	"time"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/dag"
	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/pkg/types"
)

// forceReplaceDeleteTimeout is the maximum time to wait for a force-replace
// delete sub-invocation before recording a failure and continuing the dispatch.
const forceReplaceDeleteTimeout = 5 * time.Minute

// DeploymentWorkflow executes one apply/re-apply run for a deployment.
//
// The workflow itself is intentionally thin on durable state. The authoritative
// lifecycle record lives in DeploymentStateObj. This keeps the workflow focused
// on scheduling, dispatching, waiting, and translating driver outcomes into
// deployment-level state transitions.
//
// Architecture note: DeploymentWorkflow is a Restate Workflow (not a Virtual
// Object). Each apply invocation gets its own durable execution. The workflow
// communicates with DeploymentStateObj (a Virtual Object) to persist state that
// must survive across workflow generations (e.g. re-apply increments generation
// but keeps the same deployment key).
type DeploymentWorkflow struct {
	// providers is the registry of typed provider adapters that translate
	// abstract resource kinds (e.g. "praxis:aws:s3:Bucket") into concrete
	// driver service calls.
	providers *provider.Registry
}

// NewDeploymentWorkflow constructs the apply workflow.
func NewDeploymentWorkflow(providers *provider.Registry) *DeploymentWorkflow {
	return &DeploymentWorkflow{providers: providers}
}

// ServiceName returns the Restate service name under which this workflow is
// registered. Callers use DeploymentWorkflowServiceName to start new runs.
func (*DeploymentWorkflow) ServiceName() string {
	return DeploymentWorkflowServiceName
}

// Run executes the deployment plan using eager dispatch:
//
//   - resources start as soon as their direct dependencies complete
//   - outputs are fed back into dispatch-time expression hydration
//   - dependency failures mark downstream resources as Skipped
//   - cancellation stops new dispatches but lets in-flight operations finish
func (w *DeploymentWorkflow) Run(ctx restate.WorkflowContext, plan DeploymentPlan) (DeploymentResult, error) {
	if plan.Key == "" {
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("deployment key is required"), 400)
	}

	// ---------------------------------------------------------------
	// Phase 1: Validate preconditions
	// ---------------------------------------------------------------
	// The deployment must already be initialised in DeploymentStateObj.
	// Initialisation happens in the command layer before the workflow starts,
	// ensuring the durable state record exists with generation > 0.
	state, err := getDeploymentState(ctx, plan.Key)
	if err != nil {
		return DeploymentResult{}, err
	}
	if state == nil {
		return DeploymentResult{}, restate.TerminalError(
			fmt.Errorf("deployment %q must be initialized in %s before starting the workflow", plan.Key, DeploymentStateServiceName),
			404,
		)
	}

	// Build the DAG from the plan's resource dependency declarations.
	// graphFromPlanResources validates that the dependency graph is acyclic.
	// An invalid graph (cycles, missing nodes) is a terminal error because
	// no amount of retrying will fix structural template problems.
	graph, err := graphFromPlanResources(plan.Resources)
	if err != nil {
		now, nowErr := currentTime(ctx)
		if nowErr == nil {
			if finalizeErr := finalizeDeployment(ctx, plan.Key, FinalizeRequest{
				Status:    types.DeploymentFailed,
				Error:     err.Error(),
				UpdatedAt: now,
			}); finalizeErr != nil {
				return DeploymentResult{}, restate.TerminalError(
					fmt.Errorf("invalid deployment graph: %w (additionally, failed to finalize deployment: %w)", err, finalizeErr),
					400,
				)
			}
		}
		return DeploymentResult{}, restate.TerminalError(fmt.Errorf("invalid deployment graph: %w", err), 400)
	}

	// ---------------------------------------------------------------
	// Phase 2: Transition to Running
	// ---------------------------------------------------------------
	// currentTime is wrapped in restate.Run so Restate journals the timestamp,
	// ensuring deterministic replay if the workflow restarts.
	now, err := currentTime(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	// Persist the Running status in both DeploymentStateObj and the global
	// DeploymentIndex so the CLI can poll for progress.
	if err := setDeploymentStatus(ctx, plan.Key, StatusUpdate{
		Status:    types.DeploymentRunning,
		UpdatedAt: now,
	}); err != nil {
		return DeploymentResult{}, err
	}
	state.Status = types.DeploymentRunning
	state.UpdatedAt = now
	if err := upsertDeploymentSummary(ctx, deploymentSummaryFromState(state)); err != nil {
		return DeploymentResult{}, err
	}
	startedEvent, err := NewDeploymentStartedEvent(plan.Key, plan.Workspace, state.Generation, now)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := EmitDeploymentCloudEvent(ctx, startedEvent); err != nil {
		return DeploymentResult{}, err
	}

	// ---------------------------------------------------------------
	// Phase 3: Prepare the DAG scheduler and execution state
	// ---------------------------------------------------------------
	// The Schedule wraps the DAG graph and exposes a Ready() method that
	// returns resources whose direct dependencies are all in the completed
	// set. This is the core of the eager parallel dispatch strategy.
	schedule := dag.NewSchedule(graph)

	// executionState tracks per-resource statuses, outputs, and errors across
	// the dispatch loop. loadOutputs seeds it with any outputs persisted from
	// a previous generation (relevant for re-apply scenarios).
	exec := newExecutionState(plan.Resources)
	exec.loadOutputs(state.Outputs)

	// ForceReplace resources will be deleted before re-provisioning.
	replaceSet := make(map[string]bool, len(plan.ForceReplace))
	for _, name := range plan.ForceReplace {
		replaceSet[name] = true
	}

	// inFlight tracks resources currently being provisioned by their driver.
	// Each entry maps resource name → ProvisionInvocation, which wraps a
	// Restate future for the async driver call.
	inFlight := make(map[string]provider.ProvisionInvocation)
	cancellationRequested := false

	// ---------------------------------------------------------------
	// Phase 4: Main dispatch loop (eager parallel execution)
	// ---------------------------------------------------------------
	// Each iteration:
	//   1. Check for cancellation (polls DeploymentStateObj.IsCancelled).
	//   2. Collect "ready" resources (all deps satisfied, not yet dispatched).
	//   3. For each ready resource:
	//      a. Hydrate expressions: resolve "resources.X.outputs.Y" references
	//         from completed resource outputs into the resource spec.
	//      b. Look up the provider adapter for this resource kind.
	//      c. If force-replace, delete the old resource first.
	//      d. Dispatch the driver Provision call (async via Restate).
	//   4. Wait for any one in-flight resource to complete (WaitFirst).
	//   5. Record the outcome (Ready or Error), feed outputs back for hydration.
	//   6. Repeat until no resources are left to dispatch and nothing is in-flight.
	for {
		// --- Step 4a: Check cancellation flag ---
		// The cancel flag is a durable boolean in DeploymentStateObj. Polling
		// it via a shared handler is safe because it's read-only. Once set,
		// no new resources are dispatched, but in-flight ones run to completion.
		if !cancellationRequested {
			cancelled, err := deploymentCancelled(ctx, plan.Key)
			if err != nil {
				return DeploymentResult{}, err
			}
			if cancelled {
				cancellationRequested = true
				cancelEvent, eventErr := NewCommandCancelEvent(plan.Key, plan.Workspace, state.Generation, time.Time{})
				if eventErr != nil {
					return DeploymentResult{}, eventErr
				}
				if err := EmitDeploymentCloudEvent(ctx, cancelEvent); err != nil {
					return DeploymentResult{}, err
				}
			}
		}

		// --- Step 4b: Dispatch ready resources ---
		if !cancellationRequested {
			ready := exec.ready(schedule)
			for _, name := range ready {
				resource := exec.plan[name]

				// Expression hydration: resolve cross-resource output references.
				// For example, a security group rule may reference
				// "resources.vpc.outputs.vpcId" which gets replaced with the
				// actual VPC ID from the completed VPC resource's outputs.
				// HydrateExprs writes typed values (ints stay ints, arrays stay
				// arrays) back into the JSON spec at the recorded paths.
				hydratedSpec, err := HydrateExprs(resource.Spec, resource.Expressions, exec.outputs)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("failed to hydrate spec: %v", err)); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				// Look up the typed provider adapter for this resource's driver kind.
				// The adapter translates between the generic orchestrator protocol
				// (Provision/Delete with raw JSON) and the typed driver interface.
				adapter, err := w.providers.Get(resource.Kind)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, err.Error()); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				// Decode the hydrated JSON spec into the driver's typed Go struct.
				// This catches schema mismatches early before hitting the wire.
				decodedSpec, err := adapter.DecodeSpec(hydratedSpec)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("failed to decode driver spec: %v", err)); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				// Force replacement: delete the existing resource before re-provisioning.
				if replaceSet[name] {
					if resource.Lifecycle != nil && resource.Lifecycle.PreventDestroy {
						policyEvent, eventErr := NewPolicyPreventedDestroyEvent(plan.Key, plan.Workspace, state.Generation, time.Time{}, name, resource.Kind, "force-replace")
						if eventErr != nil {
							return DeploymentResult{}, eventErr
						}
						if err := EmitCloudEvent(ctx, policyEvent); err != nil {
							return DeploymentResult{}, err
						}
						if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind,
							fmt.Sprintf("resource %s has lifecycle.preventDestroy enabled; refusing to force-replace", name)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}

					replaceEvent, eventErr := NewResourceReplaceStartedEvent(plan.Key, plan.Workspace, state.Generation, time.Time{}, name, resource.Kind)
					if eventErr != nil {
						return DeploymentResult{}, eventErr
					}
					if err := EmitDeploymentCloudEvent(ctx, replaceEvent); err != nil {
						return DeploymentResult{}, err
					}

					delInvocation, err := adapter.Delete(ctx, resource.Key)
					if err != nil {
						if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("force-replace delete dispatch failed: %v", err)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
					// Use a timeout to prevent a stuck force-replace delete from
					// blocking the entire dispatch loop.
					delTimeout := restate.After(ctx, forceReplaceDeleteTimeout)
					delFirst, delErr := restate.WaitFirst(ctx, delInvocation.Future(), delTimeout)
					if delErr != nil {
						if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("force-replace delete wait error: %v", delErr)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
					if delFirst == delTimeout {
						if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("force-replace delete timed out after %s", forceReplaceDeleteTimeout)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
					if err := delInvocation.Done(); err != nil {
						if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("force-replace delete failed: %v", err)); err != nil {
							return DeploymentResult{}, err
						}
						continue
					}
				}

				// Dispatch the async provisioning call. This creates a Restate
				// invocation to the driver service and returns a future. The driver
				// call runs independently; the workflow awaits it in the WaitFirst
				// block below.
				invocation, err := adapter.Provision(ctx, resource.Key, plan.Account, decodedSpec)
				if err != nil {
					if err := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, name, resource.Kind, fmt.Sprintf("failed to dispatch driver call: %v", err)); err != nil {
						return DeploymentResult{}, err
					}
					continue
				}

				exec.markProvisioning(name)
				inFlight[name] = invocation

				dispatchStatus := types.DeploymentResourceProvisioning
				if rs := state.Resources[name]; rs != nil && rs.PriorReady {
					dispatchStatus = types.DeploymentResourceUpdating
				}
				if err := updateDeploymentResource(ctx, plan.Key, ResourceUpdate{
					Name:   name,
					Status: dispatchStatus,
				}); err != nil {
					return DeploymentResult{}, err
				}
				dispatchedEvent, eventErr := NewResourceDispatchedEvent(plan.Key, plan.Workspace, state.Generation, time.Time{}, name, resource.Kind)
				if eventErr != nil {
					return DeploymentResult{}, eventErr
				}
				if err := EmitDeploymentCloudEvent(ctx, dispatchedEvent); err != nil {
					return DeploymentResult{}, err
				}
			}
		}

		if len(inFlight) == 0 {
			if cancellationRequested {
				break
			}
			if len(exec.ready(schedule)) == 0 {
				break
			}
		}

		if len(inFlight) == 0 {
			continue
		}

		// --- Step 4d: Await the next in-flight completion ---
		// Process completions in deterministic sorted-name order so that
		// Restate journal replay always sees the same sequence of calls.
		// Resources still execute in parallel; only the *processing* of
		// their completions is serialised in a stable order.
		//
		// Using WaitFirst with map-derived futures is non-deterministic
		// because Go map iteration order varies across runs. During
		// replay, the SDK may resolve a different future first, causing
		// a journal mismatch (code 570). Sorted-order await eliminates
		// this class of non-determinism entirely.
		resourceName, invocation := nextInFlightCompletion(inFlight)
		delete(inFlight, resourceName)

		// Retrieve the resource outputs from the completed driver call.
		// If the driver returned an error, this is a resource-level failure
		// (not a workflow infrastructure error). The resource is marked Error,
		// and all transitive dependents are marked Skipped.
		outputs, err := invocation.Outputs()
		if err != nil {
			resource := exec.plan[resourceName]

			// Auto-replace: if the driver returned a 409 immutable-field
			// conflict and the plan has AllowReplace enabled, delete the
			// resource and re-provision it instead of failing outright.
			if plan.AllowReplace && restate.ErrorCode(err) == 409 {
				// Respect lifecycle.preventDestroy even in auto-replace.
				if resource.Lifecycle != nil && resource.Lifecycle.PreventDestroy {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("resource %s requires replacement (immutable field conflict) but has lifecycle.preventDestroy enabled: %s", resourceName, err.Error())); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}

				// Emit a warning event so operators can see auto-replace activity.
				autoReplaceEvent, eventErr := NewResourceAutoReplaceStartedEvent(plan.Key, plan.Workspace, state.Generation, time.Time{}, resourceName, resource.Kind, err.Error())
				if eventErr != nil {
					return DeploymentResult{}, eventErr
				}
				if err := EmitDeploymentCloudEvent(ctx, autoReplaceEvent); err != nil {
					return DeploymentResult{}, err
				}
				ctx.Log().Warn("auto-replacing resource due to immutable field conflict",
					"resource", resourceName, "kind", resource.Kind, "error", err.Error())

				// Look up the adapter again for the delete+re-provision sequence.
				adapter, adapterErr := w.providers.Get(resource.Kind)
				if adapterErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace failed: %v", adapterErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}

				// Delete the existing resource (same timeout as force-replace).
				delInvocation, delErr := adapter.Delete(ctx, resource.Key)
				if delErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace delete dispatch failed: %v", delErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}
				delTimeout := restate.After(ctx, forceReplaceDeleteTimeout)
				delFirst, delWaitErr := restate.WaitFirst(ctx, delInvocation.Future(), delTimeout)
				if delWaitErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace delete wait error: %v", delWaitErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}
				if delFirst == delTimeout {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace delete timed out after %s", forceReplaceDeleteTimeout)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}
				if doneErr := delInvocation.Done(); doneErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace delete failed: %v", doneErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}

				// Re-decode the spec and re-dispatch provision.
				hydratedSpec, hydrErr := HydrateExprs(resource.Spec, resource.Expressions, exec.outputs)
				if hydrErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace re-hydrate failed: %v", hydrErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}
				decodedSpec, decErr := adapter.DecodeSpec(hydratedSpec)
				if decErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace decode spec failed: %v", decErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}

				reProvInvocation, provErr := adapter.Provision(ctx, resource.Key, plan.Account, decodedSpec)
				if provErr != nil {
					if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind,
						fmt.Sprintf("auto-replace re-provision dispatch failed: %v", provErr)); recErr != nil {
						return DeploymentResult{}, recErr
					}
					continue
				}

				// Put the re-provisioned resource back in flight.
				exec.resetToPending(resourceName)
				exec.markProvisioning(resourceName)
				inFlight[resourceName] = reProvInvocation
				continue
			}

			if recErr := w.recordApplyFailure(ctx, plan.Key, plan.Workspace, state.Generation, exec, schedule, resourceName, resource.Kind, err.Error()); recErr != nil {
				return DeploymentResult{}, recErr
			}
			continue
		}

		// Record the successful outputs in execution state. These outputs
		// become available for hydrating downstream resource expressions.
		exec.markReady(resourceName, outputs)
		if err := updateDeploymentResource(ctx, plan.Key, ResourceUpdate{
			Name:    resourceName,
			Status:  types.DeploymentResourceReady,
			Outputs: outputs,
		}); err != nil {
			return DeploymentResult{}, err
		}
		resource := exec.plan[resourceName]
		readyEvent, eventErr := NewResourceReadyEvent(plan.Key, plan.Workspace, state.Generation, time.Time{}, resourceName, resource.Kind, outputs)
		if eventErr != nil {
			return DeploymentResult{}, eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, readyEvent); err != nil {
			return DeploymentResult{}, err
		}
		if err := upsertResourceIndex(ctx, ResourceIndexEntry{
			Kind:          resource.Kind,
			Key:           resource.Key,
			DeploymentKey: plan.Key,
			ResourceName:  resourceName,
			Workspace:     plan.Workspace,
			Status:        string(types.DeploymentResourceReady),
			CreatedAt:     plan.CreatedAt,
		}); err != nil {
			return DeploymentResult{}, err
		}
	}

	// ---------------------------------------------------------------
	// Phase 5: Determine final deployment status
	// ---------------------------------------------------------------
	finalStatus := types.DeploymentComplete
	finalError := ""
	if cancellationRequested {
		pending := exec.skipPendingForCancellation()
		for _, name := range pending {
			resource := exec.plan[name]
			if err := updateDeploymentResource(ctx, plan.Key, ResourceUpdate{
				Name:   name,
				Status: types.DeploymentResourceSkipped,
				Error:  exec.errors[name],
			}); err != nil {
				return DeploymentResult{}, err
			}
			skippedEvent, eventErr := NewResourceSkippedEvent(plan.Key, plan.Workspace, state.Generation, time.Time{}, name, resource.Kind, types.DeploymentCancelled, exec.errors[name])
			if eventErr != nil {
				return DeploymentResult{}, eventErr
			}
			if err := EmitDeploymentCloudEvent(ctx, skippedEvent); err != nil {
				return DeploymentResult{}, err
			}
		}
		finalStatus = types.DeploymentCancelled
		finalError = exec.failureSummary()
	} else if exec.hasFailures() {
		finalStatus = types.DeploymentFailed
		finalError = exec.failureSummary()
	}

	// ---------------------------------------------------------------
	// Phase 6: Finalize and emit terminal event
	// ---------------------------------------------------------------
	now, err = currentTime(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	// Write the terminal status to DeploymentStateObj.Finalize, which also
	// cleans up resource-event-owner mappings if the deployment was fully deleted.
	if err := finalizeDeployment(ctx, plan.Key, FinalizeRequest{
		Status:    finalStatus,
		Error:     finalError,
		UpdatedAt: now,
	}); err != nil {
		return DeploymentResult{}, err
	}
	state.Status = finalStatus
	state.Error = finalError
	state.UpdatedAt = now
	state.Outputs = exec.outputs
	if err := upsertDeploymentSummary(ctx, deploymentSummaryFromState(state)); err != nil {
		return DeploymentResult{}, err
	}
	// Sync failed/skipped resources to the resource index so cross-deployment
	// queries reflect the terminal state of every resource in this deployment.
	for _, name := range exec.order {
		status := exec.statuses[name]
		if status == types.DeploymentResourceReady {
			continue // already indexed at dispatch time
		}
		resource := exec.plan[name]
		if err := upsertResourceIndex(ctx, ResourceIndexEntry{
			Kind:          resource.Kind,
			Key:           resource.Key,
			DeploymentKey: plan.Key,
			ResourceName:  name,
			Workspace:     plan.Workspace,
			Status:        string(status),
			CreatedAt:     plan.CreatedAt,
		}); err != nil {
			return DeploymentResult{}, err
		}
	}
	terminalEvent, err := NewDeploymentTerminalEvent(plan.Key, plan.Workspace, state.Generation, now, finalStatus, finalError)
	if err != nil {
		return DeploymentResult{}, err
	}
	if err := EmitDeploymentCloudEvent(ctx, terminalEvent); err != nil {
		return DeploymentResult{}, err
	}

	return exec.result(plan.Key, finalStatus, finalError), nil
}

// recordApplyFailure handles a resource-level failure during the apply workflow.
// It marks the resource as Error in both execution state and DeploymentStateObj,
// emits a resource.error CloudEvent, and then computes the transitive set of
// dependent resources that must be skipped (because their dependency failed).
// Each skipped resource also gets a resource.skipped event.
func (w *DeploymentWorkflow) recordApplyFailure(
	ctx restate.WorkflowContext,
	deploymentKey string,
	workspace string,
	generation int64,
	exec *executionState,
	schedule *dag.Schedule,
	resourceName string,
	resourceKind string,
	errMsg string,
) error {
	exec.markFailed(resourceName, errMsg)
	if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
		Name:   resourceName,
		Status: types.DeploymentResourceError,
		Error:  errMsg,
	}); err != nil {
		return err
	}
	errorEvent, eventErr := NewResourceErrorEvent(deploymentKey, workspace, generation, time.Time{}, resourceName, resourceKind, types.DeploymentRunning, errMsg)
	if eventErr != nil {
		return eventErr
	}
	if err := EmitDeploymentCloudEvent(ctx, errorEvent); err != nil {
		return err
	}

	skipped := exec.skipAffectedDependents(schedule, resourceName, fmt.Sprintf("skipped because dependency %s failed", resourceName))
	for _, name := range skipped {
		resource := exec.plan[name]
		if err := updateDeploymentResource(ctx, deploymentKey, ResourceUpdate{
			Name:   name,
			Status: types.DeploymentResourceSkipped,
			Error:  exec.errors[name],
		}); err != nil {
			return err
		}
		skippedEvent, eventErr := NewResourceSkippedEvent(deploymentKey, workspace, generation, time.Time{}, name, resource.Kind, types.DeploymentRunning, exec.errors[name])
		if eventErr != nil {
			return eventErr
		}
		if err := EmitDeploymentCloudEvent(ctx, skippedEvent); err != nil {
			return err
		}
	}
	return nil
}

// nextInFlightCompletion picks the alphabetically-first in-flight resource and
// returns its name and invocation. The caller then blocks on invocation.Outputs()
// which suspends the Restate workflow until that specific driver call completes.
//
// This is intentionally deterministic: sorting by name gives Restate a stable
// journal sequence regardless of Go map iteration order or the real-time
// completion order of parallel driver calls.
func nextInFlightCompletion(inFlight map[string]provider.ProvisionInvocation) (string, provider.ProvisionInvocation) {
	names := make([]string, 0, len(inFlight))
	for name := range inFlight {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0], inFlight[names[0]]
}
