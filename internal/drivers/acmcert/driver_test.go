package acmcert

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/pkg/types"
)

func TestServiceName(t *testing.T) {
	drv := NewACMCertificateDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		CertificateArn:          "arn:aws:acm:us-east-1:123456789012:certificate/abc",
		DomainName:              "api.example.com",
		SubjectAlternativeNames: []string{"api.example.com", "www.example.com"},
		ValidationMethod:        "DNS",
		KeyAlgorithm:            "RSA_2048",
		Options:                 CertificateOptions{CertificateTransparencyLoggingPreference: "DISABLED"},
		Tags:                    map[string]string{"Name": "api-cert", "env": "dev", "praxis:managed-key": "us-east-1~api-cert"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.DomainName, spec.DomainName)
	assert.Equal(t, []string{"api.example.com", "www.example.com"}, spec.SubjectAlternativeNames)
	assert.Equal(t, "DNS", spec.ValidationMethod)
	assert.Equal(t, "RSA_2048", spec.KeyAlgorithm)
	assert.Equal(t, map[string]string{"Name": "api-cert", "env": "dev"}, spec.Tags)
	if assert.NotNil(t, spec.Options) {
		assert.Equal(t, "DISABLED", spec.Options.CertificateTransparencyLoggingPreference)
	}
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		CertificateArn: "arn:aws:acm:us-east-1:123456789012:certificate/abc",
		DomainName:     "api.example.com",
		Status:         "PENDING_VALIDATION",
		DNSValidationRecords: []DNSValidationRecord{{
			DomainName:          "api.example.com",
			ResourceRecordName:  "_x.api.example.com",
			ResourceRecordType:  "CNAME",
			ResourceRecordValue: "_value.acm-validations.aws.",
		}},
		NotBefore: "2026-01-01T00:00:00Z",
		NotAfter:  "2027-01-01T00:00:00Z",
	})

	assert.Equal(t, "api.example.com", outputs.DomainName)
	assert.Equal(t, "PENDING_VALIDATION", outputs.Status)
	assert.Len(t, outputs.DNSValidationRecords, 1)
	assert.Equal(t, "2026-01-01T00:00:00Z", outputs.NotBefore)
	assert.Equal(t, "2027-01-01T00:00:00Z", outputs.NotAfter)
}

func TestDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultACMCertificateImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultACMCertificateImportMode(types.ModeManaged))
}
