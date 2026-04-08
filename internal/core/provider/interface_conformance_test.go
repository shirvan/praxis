package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- Observer interface conformance ---

func TestS3Adapter_ImplementsObserver(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	_, ok := any(adapter).(Observer)
	assert.True(t, ok, "S3Adapter should implement Observer")
}

func TestVPCAdapter_ImplementsObserver(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	_, ok := any(adapter).(Observer)
	assert.True(t, ok, "VPCAdapter should implement Observer")
}

func TestSGAdapter_ImplementsObserver(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	_, ok := any(adapter).(Observer)
	assert.True(t, ok, "SGAdapter should implement Observer")
}

func TestEC2Adapter_DoesNotImplementObserver(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	_, ok := any(adapter).(Observer)
	assert.False(t, ok, "EC2Adapter should not implement Observer")
}

// --- TimeoutDefaultsProvider interface conformance ---

func TestEC2Adapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "10m", timeouts.Create)
}

func TestRDSInstanceAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewRDSInstanceAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "30m", timeouts.Create)
}

func TestALBAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewALBAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "10m", timeouts.Create)
}

func TestNLBAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewNLBAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "10m", timeouts.Create)
}

func TestLambdaAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewLambdaAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "5m", timeouts.Create)
}

func TestAuroraClusterAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "30m", timeouts.Create)
}

func TestNATGWAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewNATGatewayAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "10m", timeouts.Create)
}

func TestEBSAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewEBSAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "10m", timeouts.Create)
}

func TestVPCAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "5m", timeouts.Create)
	assert.Equal(t, "10m", timeouts.Delete)
}

func TestSGAdapter_ImplementsTimeoutDefaults(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	p, ok := any(adapter).(TimeoutDefaultsProvider)
	assert.True(t, ok)
	timeouts := p.DefaultTimeouts()
	assert.Equal(t, "5m", timeouts.Create)
}

// --- PreDeleter interface conformance ---

func TestS3Adapter_ImplementsPreDeleter(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	_, ok := any(adapter).(PreDeleter)
	assert.True(t, ok, "S3Adapter should implement PreDeleter")
}

func TestECRRepoAdapter_ImplementsPreDeleter(t *testing.T) {
	adapter := NewECRRepositoryAdapterWithAuth(nil)
	_, ok := any(adapter).(PreDeleter)
	assert.True(t, ok, "ECRRepositoryAdapter should implement PreDeleter")
}

func TestIAMRoleAdapter_ImplementsPreDeleter(t *testing.T) {
	adapter := NewIAMRoleAdapterWithAuth(nil)
	_, ok := any(adapter).(PreDeleter)
	assert.True(t, ok, "IAMRoleAdapter should implement PreDeleter")
}

func TestVPCAdapter_DoesNotImplementPreDeleter(t *testing.T) {
	adapter := NewVPCAdapterWithAuth(nil)
	_, ok := any(adapter).(PreDeleter)
	assert.False(t, ok, "VPCAdapter should not implement PreDeleter")
}

// --- ReadyWaiter interface conformance ---

func TestEC2Adapter_ImplementsReadyWaiter(t *testing.T) {
	adapter := NewEC2AdapterWithAuth(nil)
	_, ok := any(adapter).(ReadyWaiter)
	assert.True(t, ok, "EC2Adapter should implement ReadyWaiter")
}

func TestRDSInstanceAdapter_ImplementsReadyWaiter(t *testing.T) {
	adapter := NewRDSInstanceAdapterWithAuth(nil)
	_, ok := any(adapter).(ReadyWaiter)
	assert.True(t, ok, "RDSInstanceAdapter should implement ReadyWaiter")
}

func TestLambdaAdapter_ImplementsReadyWaiter(t *testing.T) {
	adapter := NewLambdaAdapterWithAuth(nil)
	_, ok := any(adapter).(ReadyWaiter)
	assert.True(t, ok, "LambdaAdapter should implement ReadyWaiter")
}

func TestAuroraClusterAdapter_ImplementsReadyWaiter(t *testing.T) {
	adapter := NewAuroraClusterAdapterWithAuth(nil)
	_, ok := any(adapter).(ReadyWaiter)
	assert.True(t, ok, "AuroraClusterAdapter should implement ReadyWaiter")
}

func TestS3Adapter_DoesNotImplementReadyWaiter(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	_, ok := any(adapter).(ReadyWaiter)
	assert.False(t, ok, "S3Adapter should not implement ReadyWaiter")
}
