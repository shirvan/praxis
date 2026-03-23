package listener

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewListenerDriver(nil)
	assert.Equal(t, "Listener", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		LoadBalancerArn: "arn:aws:elasticloadbalancing:us-east-1:123:loadbalancer/app/my-alb/1234",
		Port:            443,
		Protocol:        "HTTPS",
		SslPolicy:       "ELBSecurityPolicy-TLS13-1-2-2021-06",
		CertificateArn:  "arn:aws:acm:us-east-1:123:certificate/abc",
		DefaultActions:  []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
		Tags:            map[string]string{"env": "dev", "praxis:listener-name": "my-listener"},
	}
	spec := specFromObserved(obs)
	assert.Equal(t, obs.LoadBalancerArn, spec.LoadBalancerArn)
	assert.Equal(t, obs.Port, spec.Port)
	assert.Equal(t, obs.Protocol, spec.Protocol)
	assert.Equal(t, obs.SslPolicy, spec.SslPolicy)
	assert.Equal(t, obs.CertificateArn, spec.CertificateArn)
	assert.Equal(t, obs.DefaultActions, spec.DefaultActions)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestValidateSpec_Valid(t *testing.T) {
	spec := ListenerSpec{
		LoadBalancerArn: "arn:lb",
		Port:            80,
		Protocol:        "HTTP",
		DefaultActions:  []ListenerAction{{Type: "forward", TargetGroupArn: "arn:tg"}},
	}
	assert.NoError(t, validateSpec(spec))
}

func TestValidateSpec_MissingLB(t *testing.T) {
	spec := ListenerSpec{Port: 80, Protocol: "HTTP", DefaultActions: []ListenerAction{{Type: "forward"}}}
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_InvalidPort(t *testing.T) {
	spec := ListenerSpec{LoadBalancerArn: "arn:lb", Port: 0, Protocol: "HTTP", DefaultActions: []ListenerAction{{Type: "forward"}}}
	assert.Error(t, validateSpec(spec))
	spec.Port = 70000
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_MissingProtocol(t *testing.T) {
	spec := ListenerSpec{LoadBalancerArn: "arn:lb", Port: 80, DefaultActions: []ListenerAction{{Type: "forward"}}}
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_HTTPSWithoutCert(t *testing.T) {
	spec := ListenerSpec{LoadBalancerArn: "arn:lb", Port: 443, Protocol: "HTTPS", DefaultActions: []ListenerAction{{Type: "forward"}}}
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_TLSWithoutCert(t *testing.T) {
	spec := ListenerSpec{LoadBalancerArn: "arn:lb", Port: 443, Protocol: "TLS", DefaultActions: []ListenerAction{{Type: "forward"}}}
	assert.Error(t, validateSpec(spec))
}

func TestValidateSpec_NoActions(t *testing.T) {
	spec := ListenerSpec{LoadBalancerArn: "arn:lb", Port: 80, Protocol: "HTTP"}
	assert.Error(t, validateSpec(spec))
}

func TestHasImmutableChange(t *testing.T) {
	spec := ListenerSpec{LoadBalancerArn: "arn:lb-a"}
	obs := ObservedState{LoadBalancerArn: "arn:lb-a"}
	assert.False(t, hasImmutableChange(spec, obs))
	spec.LoadBalancerArn = "arn:lb-b"
	assert.True(t, hasImmutableChange(spec, obs))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{ListenerArn: "arn:listener", Port: 443, Protocol: "HTTPS"}
	out := outputsFromObserved(obs)
	assert.Equal(t, "arn:listener", out.ListenerArn)
	assert.Equal(t, 443, out.Port)
	assert.Equal(t, "HTTPS", out.Protocol)
}
