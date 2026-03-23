package targetgroup

import "testing"

func TestHasDrift_TargetsAndTags(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", Tags: map[string]string{"env": "dev"}, Targets: []Target{{ID: "i-1", Port: 80}}})
	observed := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", HealthCheck: spec.HealthCheck, DeregistrationDelay: spec.DeregistrationDelay, Tags: map[string]string{"env": "prod"}, Targets: []Target{{ID: "i-2", Port: 80}}}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift")
	}
}