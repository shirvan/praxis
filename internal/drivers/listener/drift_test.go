package listener

import (
	"strings"
	"testing"
)

func TestHasDrift_NoDrift(t *testing.T) {
	spec := ListenerSpec{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{"env": "dev"},
	}
	observed := ObservedState{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{"env": "dev"},
	}
	if HasDrift(spec, observed) {
		t.Fatal("expected no drift")
	}
}

func TestHasDrift_PortChanged(t *testing.T) {
	spec := ListenerSpec{
		Port:           8080,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{},
	}
	observed := ObservedState{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from port change")
	}
}

func TestHasDrift_ProtocolChanged(t *testing.T) {
	spec := ListenerSpec{
		Port:           443,
		Protocol:       "HTTPS",
		SslPolicy:      "policy",
		CertificateArn: "arn:cert",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{},
	}
	observed := ObservedState{
		Port:           443,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from protocol change")
	}
}

func TestHasDrift_SSLPolicyChanged(t *testing.T) {
	spec := ListenerSpec{
		Port:           443,
		Protocol:       "HTTPS",
		SslPolicy:      "policy-new",
		CertificateArn: "arn:cert",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{},
	}
	observed := ObservedState{
		Port:           443,
		Protocol:       "HTTPS",
		SslPolicy:      "policy-old",
		CertificateArn: "arn:cert",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from SSL policy change")
	}
}

func TestHasDrift_DefaultActionChanged(t *testing.T) {
	spec := ListenerSpec{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg-new"}},
		Tags:           map[string]string{},
	}
	observed := ObservedState{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg-old"}},
		Tags:           map[string]string{},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from default action change")
	}
}

func TestHasDrift_TagChanged(t *testing.T) {
	spec := ListenerSpec{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{"env": "prod"},
	}
	observed := ObservedState{
		Port:           80,
		Protocol:       "HTTP",
		DefaultActions: []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:           map[string]string{"env": "dev"},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from tag change")
	}
}

func TestComputeFieldDiffs_ImmutableLB(t *testing.T) {
	spec := ListenerSpec{
		LoadBalancerArn: "arn:lb-new",
		Port:            80,
		Protocol:        "HTTP",
		DefaultActions:  []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:            map[string]string{},
	}
	observed := ObservedState{
		LoadBalancerArn: "arn:lb-old",
		Port:            80,
		Protocol:        "HTTP",
		DefaultActions:  []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:            map[string]string{},
	}
	diffs := ComputeFieldDiffs(spec, observed)
	found := false
	for _, d := range diffs {
		if strings.Contains(d.Path, "loadBalancerArn") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected loadBalancerArn diff in %v", diffs)
	}
}

func TestActionsEqual(t *testing.T) {
	a := []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}}
	b := []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}}
	if !actionsEqual(a, b) {
		t.Fatal("expected equal")
	}
	b[0].TargetGroupArn = "arn:tg2"
	if actionsEqual(a, b) {
		t.Fatal("expected not equal")
	}
}

func TestActionsEqual_DifferentLength(t *testing.T) {
	a := []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}}
	b := []ListenerAction{}
	if actionsEqual(a, b) {
		t.Fatal("expected not equal for different lengths")
	}
}

func TestRedirectEqual(t *testing.T) {
	a := &RedirectConfig{Protocol: "HTTPS", Host: "example.com", Port: "443", Path: "/new", Query: "", StatusCode: "HTTP_301"}
	b := &RedirectConfig{Protocol: "HTTPS", Host: "example.com", Port: "443", Path: "/new", Query: "", StatusCode: "HTTP_301"}
	if !redirectEqual(a, b) {
		t.Fatal("expected equal")
	}
	b.StatusCode = "HTTP_302"
	if redirectEqual(a, b) {
		t.Fatal("expected not equal")
	}
	if !redirectEqual(nil, nil) {
		t.Fatal("nil/nil should be equal")
	}
	if redirectEqual(a, nil) {
		t.Fatal("non-nil/nil should not be equal")
	}
}

func TestRequiresSSL(t *testing.T) {
	if !requiresSSL("HTTPS") {
		t.Fatal("HTTPS requires SSL")
	}
	if !requiresSSL("TLS") {
		t.Fatal("TLS requires SSL")
	}
	if requiresSSL("HTTP") {
		t.Fatal("HTTP does not require SSL")
	}
	if requiresSSL("TCP") {
		t.Fatal("TCP does not require SSL")
	}
}
