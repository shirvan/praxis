package targetgroup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewTargetGroupDriver(nil)
	assert.Equal(t, "TargetGroup", drv.ServiceName())
}

func TestSpecFromObserved(t *testing.T) {
	obs := ObservedState{
		Name:                "api-tg",
		Protocol:            "HTTP",
		Port:                8080,
		VpcId:               "vpc-123",
		TargetType:          "instance",
		ProtocolVersion:     "HTTP1",
		HealthCheck:         HealthCheck{Protocol: "HTTP", Path: "/health", Port: "traffic-port", HealthyThreshold: 5, UnhealthyThreshold: 2, Interval: 30, Timeout: 5},
		DeregistrationDelay: 300,
		Stickiness:          &Stickiness{Enabled: true, Type: "lb_cookie", Duration: 3600},
		Targets:             []Target{{ID: "i-123", Port: 8080}},
		Tags:                map[string]string{"env": "dev", "praxis:managed-key": "us-east-1~api-tg"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.Name, spec.Name)
	assert.Equal(t, obs.Protocol, spec.Protocol)
	assert.Equal(t, obs.Port, spec.Port)
	assert.Equal(t, obs.VpcId, spec.VpcId)
	assert.Equal(t, obs.TargetType, spec.TargetType)
	assert.Equal(t, obs.ProtocolVersion, spec.ProtocolVersion)
	assert.Equal(t, obs.HealthCheck, spec.HealthCheck)
	assert.Equal(t, obs.DeregistrationDelay, spec.DeregistrationDelay)
	assert.Equal(t, obs.Stickiness, spec.Stickiness)
	assert.Equal(t, obs.Targets, spec.Targets)
	assert.Equal(t, map[string]string{"env": "dev"}, spec.Tags, "praxis: tags should be filtered out")
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
	assert.Equal(t, types.ModeObserved, defaultImportMode(types.ModeObserved))
}

func TestApplyDefaults(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Name: "  tg  ", Region: "  us-east-1  ", VpcId: " vpc-1 "})
	assert.Equal(t, "tg", spec.Name)
	assert.Equal(t, "us-east-1", spec.Region)
	assert.Equal(t, "vpc-1", spec.VpcId)
	assert.Equal(t, "instance", spec.TargetType)
	assert.Equal(t, 300, spec.DeregistrationDelay)
	assert.Equal(t, "HTTP", spec.HealthCheck.Protocol)
	assert.Equal(t, "traffic-port", spec.HealthCheck.Port)
	assert.Equal(t, "/", spec.HealthCheck.Path)
	assert.Equal(t, int32(5), spec.HealthCheck.HealthyThreshold)
	assert.Equal(t, int32(2), spec.HealthCheck.UnhealthyThreshold)
	assert.Equal(t, int32(30), spec.HealthCheck.Interval)
	assert.Equal(t, int32(5), spec.HealthCheck.Timeout)
	assert.NotNil(t, spec.Tags)
	assert.NotNil(t, spec.Targets)
}

func TestApplyDefaults_StickinessDefaults(t *testing.T) {
	spec := applyDefaults(TargetGroupSpec{Stickiness: &Stickiness{Enabled: true}})
	assert.Equal(t, "lb_cookie", spec.Stickiness.Type)
	assert.Equal(t, 86400, spec.Stickiness.Duration)
}

func TestValidateSpec(t *testing.T) {
	base := applyDefaults(TargetGroupSpec{
		Region:   "us-east-1",
		Name:     "api-tg",
		Protocol: "HTTP",
		Port:     8080,
		VpcId:    "vpc-123",
	})
	assert.NoError(t, validateSpec(base))

	noRegion := base
	noRegion.Region = ""
	assert.Error(t, validateSpec(noRegion))

	noName := base
	noName.Name = ""
	assert.Error(t, validateSpec(noName))

	noProtocol := base
	noProtocol.Protocol = ""
	assert.Error(t, validateSpec(noProtocol))

	badPort := base
	badPort.Port = 0
	assert.Error(t, validateSpec(badPort))

	noVpc := base
	noVpc.VpcId = ""
	assert.Error(t, validateSpec(noVpc))

	lambdaNoVpc := base
	lambdaNoVpc.TargetType = "lambda"
	lambdaNoVpc.VpcId = ""
	assert.NoError(t, validateSpec(lambdaNoVpc))

	emptyTarget := base
	emptyTarget.Targets = []Target{{ID: "  "}}
	assert.Error(t, validateSpec(emptyTarget))
}

func TestHasImmutableChange(t *testing.T) {
	spec := TargetGroupSpec{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", ProtocolVersion: "HTTP1"}
	obs := ObservedState{Protocol: "HTTP", Port: 80, VpcId: "vpc-1", TargetType: "instance", ProtocolVersion: "HTTP1"}
	assert.False(t, hasImmutableChange(spec, obs))

	protocolChanged := spec
	protocolChanged.Protocol = "HTTPS"
	assert.True(t, hasImmutableChange(protocolChanged, obs))

	portChanged := spec
	portChanged.Port = 443
	assert.True(t, hasImmutableChange(portChanged, obs))

	vpcChanged := spec
	vpcChanged.VpcId = "vpc-2"
	assert.True(t, hasImmutableChange(vpcChanged, obs))

	targetTypeChanged := spec
	targetTypeChanged.TargetType = "ip"
	assert.True(t, hasImmutableChange(targetTypeChanged, obs))

	protocolVersionChanged := spec
	protocolVersionChanged.ProtocolVersion = "HTTP2"
	assert.True(t, hasImmutableChange(protocolVersionChanged, obs))
}

func TestOutputsFromObserved(t *testing.T) {
	obs := ObservedState{TargetGroupArn: "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/api-tg/1234567890", Name: "api-tg"}
	out := outputsFromObserved(obs)
	assert.Equal(t, obs.TargetGroupArn, out.TargetGroupArn)
	assert.Equal(t, obs.Name, out.TargetGroupName)
}
