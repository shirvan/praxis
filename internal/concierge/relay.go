package concierge

import (
	"fmt"
	"log/slog"

	restate "github.com/restatedev/sdk-go"
)

// ApprovalRelay is a stateless Restate Basic Service that resolves awakeables.
type ApprovalRelay struct{}

func (ApprovalRelay) ServiceName() string { return ApprovalRelayServiceName }

// Resolve resolves or rejects an awakeable by its ID.
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
