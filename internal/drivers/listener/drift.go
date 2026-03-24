package listener

import "strings"

type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

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
	if !tagsMatch(desired.Tags, observed.Tags) {
		return true
	}
	return false
}

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
	fd := filterPraxisTags(desired)
	fo := filterPraxisTags(observed)
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

func tagsMatch(a, b map[string]string) bool {
	fa := filterPraxisTags(a)
	fb := filterPraxisTags(b)
	if len(fa) != len(fb) {
		return false
	}
	for key, value := range fa {
		if other, ok := fb[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func requiresSSL(protocol string) bool {
	p := strings.ToUpper(protocol)
	return p == "HTTPS" || p == "TLS"
}
