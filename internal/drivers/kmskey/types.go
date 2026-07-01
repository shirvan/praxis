// Package kmskey implements the Praxis driver for AWS KMS keys.
//
// This file defines the spec, outputs, observed-state, and reconciliation-state
// types that flow through the driver lifecycle (Provision → Reconcile → Delete).
// The spec is the user's desired configuration; the observed state is read from
// the KMS DescribeKey / GetKeyRotationStatus / ListResourceTags APIs; the driver
// state couples both together with status tracking.
//
// A KMSKey resource manages a KMS key AND its alias "alias/<name>" as a single
// unit. Identity and existence are established by alias lookup: DescribeKey with
// KeyId "alias/<name>" returns the key when the alias exists, or a NotFound error
// when it does not.
package kmskey

import "github.com/shirvan/praxis/pkg/types"

// ServiceName is the Restate Virtual Object service name used to register the AWS KMS key driver.
const ServiceName = "KMSKey"

// KMSKeySpec declares the user's desired configuration for a KMS key.
//
// Immutable fields (set at creation; changes surface as requires-replacement diffs):
//   - KeyUsage: cryptographic operations the key supports (e.g. ENCRYPT_DECRYPT)
//   - KeySpec:  the key's type/size (e.g. SYMMETRIC_DEFAULT)
//
// Mutable fields (converged in place during reconciliation):
//   - Description:       human-readable description
//   - EnableKeyRotation: whether automatic annual key rotation is enabled
//   - Tags:              user-defined tags (praxis:-prefixed tags are reserved)
//
// DeletionWindowInDays is consumed only at delete time (ScheduleKeyDeletion) and
// never participates in drift detection.
type KMSKeySpec struct {
	Account              string            `json:"account,omitempty"`
	Region               string            `json:"region"`
	Name                 string            `json:"name"`
	Description          string            `json:"description,omitempty"`
	KeyUsage             string            `json:"keyUsage,omitempty"`
	KeySpec              string            `json:"keySpec,omitempty"`
	EnableKeyRotation    bool              `json:"enableKeyRotation"`
	DeletionWindowInDays int32             `json:"deletionWindowInDays,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
	ManagedKey           string            `json:"managedKey,omitempty"`
}

// KMSKeyOutputs holds the values produced after provisioning a KMS key.
type KMSKeyOutputs struct {
	ARN       string `json:"arn"`
	KeyID     string `json:"keyId"`
	AliasName string `json:"aliasName"`
}

// ObservedState captures the live configuration of a KMS key as read from the
// DescribeKey, GetKeyRotationStatus, and ListResourceTags APIs. It is compared
// against the spec during drift detection.
type ObservedState struct {
	ARN               string            `json:"arn"`
	KeyID             string            `json:"keyId"`
	AliasName         string            `json:"aliasName"`
	Description       string            `json:"description,omitempty"`
	KeyUsage          string            `json:"keyUsage,omitempty"`
	KeySpec           string            `json:"keySpec,omitempty"`
	KeyState          string            `json:"keyState,omitempty"`
	Enabled           bool              `json:"enabled"`
	EnableKeyRotation bool              `json:"enableKeyRotation"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// KMSKeyState is the single atomic state object persisted under drivers.StateKey
// in the Restate K/V store. It combines desired spec, observed state, outputs,
// lifecycle status, mode (managed/observed), error message, generation counter,
// and reconciliation scheduling metadata.
type KMSKeyState struct {
	Desired            KMSKeySpec           `json:"desired"`
	Observed           ObservedState        `json:"observed"`
	Outputs            KMSKeyOutputs        `json:"outputs"`
	Status             types.ResourceStatus `json:"status"`
	Mode               types.Mode           `json:"mode"`
	Error              string               `json:"error,omitempty"`
	Generation         int64                `json:"generation"`
	LastReconcile      string               `json:"lastReconcile,omitempty"`
	ReconcileScheduled bool                 `json:"reconcileScheduled"`
}
