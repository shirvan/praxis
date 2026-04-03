package concierge

import (
	"fmt"
	"log/slog"

	restate "github.com/restatedev/sdk-go"
)

// ApprovalRelay is a stateless Restate Basic Service that resolves or rejects
// awakeables on behalf of external transports (CLI, Slack).
//
// This service is the bridge between the external approval UI and the suspended
// ConciergeSession handler. The flow works as follows:
//
//  1. ConciergeSession.Ask() encounters a write tool and creates an awakeable
//  2. The awakeable ID is stored in SessionState.PendingApproval
//  3. The transport (CLI/Slack) polls GetStatus(), discovers the pending approval
//  4. The transport shows an approval prompt to the user
//  5. The user approves or rejects
//  6. The transport calls ApprovalRelay.Resolve() with the awakeable ID and decision
//  7. Restate delivers the decision to the suspended Ask() handler, which resumes
//
// ApprovalRelay is stateless — it doesn't store anything. It simply forwards the
// resolve/reject call to Restate's awakeable API.
type ApprovalRelay struct{}

// ServiceName returns the Restate service name for registration.
func (ApprovalRelay) ServiceName() string { return ApprovalRelayServiceName }

// Resolve resolves or rejects an awakeable by its ID. If approved, the awakeable
// is resolved with an ApprovalDecision{Approved: true}. If rejected, the awakeable
// is rejected with an error (which the suspended handler receives as an error from
// awakeable.Result()). This distinction matters: resolved awakeables return a value,
// rejected awakeables return an error.
func (ApprovalRelay) Resolve(ctx restate.Context, req ApprovalRelayRequest) error {
	if req.AwakeableID == "" {
		return restate.TerminalError(fmt.Errorf("awakeableId is required"), 400)
	}

	slog.Info("approval decision",
		"awakeableId", req.AwakeableID,
		"approved", req.Approved,
		"reason", req.Reason,
		"actor", req.Actor,
	)

	if req.Approved {
		restate.ResolveAwakeable(ctx, req.AwakeableID, ApprovalDecision{
			Approved: true,
		})
	} else {
		reason := req.Reason
		if reason == "" {
			reason = "rejected"
		}
		restate.RejectAwakeable(ctx, req.AwakeableID, fmt.Errorf("%s", reason))
	}
	return nil
}
