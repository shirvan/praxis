// Package listener – drift.go
//
// This file implements drift detection for AWS ELBv2 Listener.
// HasDrift compares the desired spec against the observed state from AWS and
// returns true when any mutable field has diverged. ComputeFieldDiffs produces
// a structured list of individual field changes for plan output and logging.
// Immutable fields (those that require resource replacement) are annotated.
package listener

import (
	"github.com/shirvan/praxis/internal/drivers"
	"strings"
)

// FieldDiffEntry represents a single field-level difference between the desired
// spec and the observed state. Path uses dot notation (e.g. "spec.name");
// immutable fields are annotated with "(immutable, requires replacement)".
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift compares the desired Listener spec against the observed
// state from AWS and returns true if any mutable field has diverged.
// It is called during Reconcile to decide whether drift correction is needed.
func HasDrift(desired ListenerSpec, observed ObservedState) bool {
	if desired.Port != observed.Port {
		return true
	}
	if !strings.EqualFold(desired.Protocol, observed.Protocol) {
		return true
	}
	if requiresSSL(desired.Protocol) {
		if desired.SslPolicy != observed.SslPolicy {
			return true
		}
		if desired.CertificateArn != observed.CertificateArn {
			return true
		}
	}
	if desired.AlpnPolicy != observed.AlpnPolicy {
		return true
	}
	if !actionsEqual(desired.DefaultActions, observed.DefaultActions) {
		return true
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

// ComputeFieldDiffs produces a structured list of individual field changes
// between the desired spec and observed state. Used for plan output, CLI
// display, and audit logging. Immutable field changes are clearly annotated.
func ComputeFieldDiffs(desired ListenerSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	if desired.LoadBalancerArn != observed.LoadBalancerArn {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.loadBalancerArn (immutable, requires replacement)", OldValue: observed.LoadBalancerArn, NewValue: desired.LoadBalancerArn})
	}
	if desired.Port != observed.Port {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.port", OldValue: observed.Port, NewValue: desired.Port})
	}
	if !strings.EqualFold(desired.Protocol, observed.Protocol) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.protocol", OldValue: observed.Protocol, NewValue: desired.Protocol})
	}
	if desired.SslPolicy != observed.SslPolicy {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.sslPolicy", OldValue: observed.SslPolicy, NewValue: desired.SslPolicy})
	}
	if desired.CertificateArn != observed.CertificateArn {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.certificateArn", OldValue: observed.CertificateArn, NewValue: desired.CertificateArn})
	}
	if desired.AlpnPolicy != observed.AlpnPolicy {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.alpnPolicy", OldValue: observed.AlpnPolicy, NewValue: desired.AlpnPolicy})
	}
	if !actionsEqual(desired.DefaultActions, observed.DefaultActions) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.defaultActions", OldValue: observed.DefaultActions, NewValue: desired.DefaultActions})
	}
	diffs = append(diffs, computeTagDiffs(desired.Tags, observed.Tags)...)
	return diffs
}

func actionsEqual(a, b []ListenerAction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type {
			return false
		}
		switch a[i].Type {
		case "forward":
			if a[i].TargetGroupArn != b[i].TargetGroupArn {
				return false
			}
		case "redirect":
			if !redirectEqual(a[i].RedirectConfig, b[i].RedirectConfig) {
				return false
			}
		case "fixed-response":
			if !fixedResponseEqual(a[i].FixedResponseConfig, b[i].FixedResponseConfig) {
				return false
			}
		}
	}
	return true
}

func redirectEqual(a, b *RedirectConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Protocol == b.Protocol && a.Host == b.Host && a.Port == b.Port &&
		a.Path == b.Path && a.Query == b.Query && a.StatusCode == b.StatusCode
}

func fixedResponseEqual(a, b *FixedResponseConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.StatusCode == b.StatusCode && a.ContentType == b.ContentType && a.MessageBody == b.MessageBody
}

func computeTagDiffs(desired, observed map[string]string) []FieldDiffEntry {
	var diffs []FieldDiffEntry
	fd := drivers.FilterPraxisTags(desired)
	fo := drivers.FilterPraxisTags(observed)
	for key, value := range fd {
		if oldValue, ok := fo[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: nil, NewValue: value})
		} else if oldValue != value {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: oldValue, NewValue: value})
		}
	}
	for key, value := range fo {
		if _, ok := fd[key]; !ok {
			diffs = append(diffs, FieldDiffEntry{Path: "tags." + key, OldValue: value, NewValue: nil})
		}
	}
	return diffs
}

func requiresSSL(protocol string) bool {
	p := strings.ToUpper(protocol)
	return p == "HTTPS" || p == "TLS"
}
