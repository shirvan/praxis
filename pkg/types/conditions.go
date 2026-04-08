package types

import "time"

// Condition is a structured status signal for one aspect of a resource's
// lifecycle. It mirrors the familiar Kubernetes condition shape.
type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
}

const (
	ConditionReady       = "Ready"
	ConditionProvisioned = "Provisioned"
	ConditionHealthy     = "Healthy"
	ConditionDriftFree   = "DriftFree"
)

const (
	ConditionTrue    = "True"
	ConditionFalse   = "False"
	ConditionUnknown = "Unknown"
)

const (
	ReasonDispatched      = "Dispatched"
	ReasonSucceeded       = "Succeeded"
	ReasonProvisionFailed = "ProvisionFailed"
	ReasonDeleteFailed    = "DeleteFailed"
	ReasonDeleting        = "Deleting"
	ReasonDriftDetected   = "DriftDetected"
	ReasonDriftCorrected  = "DriftCorrected"
	ReasonExternalDelete  = "ExternalDelete"
	ReasonNotFound        = "NotFound"
	ReasonSkipped         = "Skipped"
	ReasonRetrying        = "Retrying"
	ReasonTimedOut        = "TimedOut"
	ReasonOrphaned        = "Orphaned"
)

// SetCondition replaces or inserts a condition keyed by Type. The caller is
// responsible for providing a deterministic timestamp.
func SetCondition(conditions []Condition, condition Condition, now time.Time) []Condition {
	if condition.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = now
	}

	updated := make([]Condition, len(conditions))
	copy(updated, conditions)
	for i, existing := range updated {
		if existing.Type != condition.Type {
			continue
		}
		if existing.Status == condition.Status && existing.Reason == condition.Reason && existing.Message == condition.Message {
			condition.LastTransitionTime = existing.LastTransitionTime
		}
		updated[i] = condition
		return updated
	}

	return append(updated, condition)
}

func GetCondition(conditions []Condition, conditionType string) (Condition, bool) {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition, true
		}
	}
	return Condition{}, false
}

// IsConditionTrue returns true if the named condition exists and has status "True".
func IsConditionTrue(conditions []Condition, conditionType string) bool {
	c, ok := GetCondition(conditions, conditionType)
	return ok && c.Status == ConditionTrue
}
