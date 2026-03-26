package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/drivers/acmcert"
)

type mockACMAPI struct {
	describe map[string]acmcert.ObservedState
}

func (m *mockACMAPI) RequestCertificate(context.Context, acmcert.ACMCertificateSpec) (string, error) {
	return "", nil
}

func (m *mockACMAPI) DescribeCertificate(_ context.Context, certificateArn string) (acmcert.ObservedState, error) {
	if obs, ok := m.describe[certificateArn]; ok {
		return obs, nil
	}
	return acmcert.ObservedState{}, nil
}

func (m *mockACMAPI) UpdateCertificateOptions(context.Context, string, *acmcert.CertificateOptions) error {
	return nil
}

func (m *mockACMAPI) UpdateTags(context.Context, string, map[string]string) error { return nil }
func (m *mockACMAPI) DeleteCertificate(context.Context, string) error             { return nil }
func (m *mockACMAPI) FindByManagedKey(context.Context, string) (string, error)    { return "", nil }

func TestACMCertificateAdapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewACMCertificateAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"ACMCertificate",
		"metadata":{"name":"api-cert"},
		"spec":{
			"region":"us-east-1",
			"domainName":"api.example.com",
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~api-cert", key)
	assert.Equal(t, KeyScopeRegion, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(acmcert.ACMCertificateSpec)
	require.True(t, ok)
	assert.Equal(t, "us-east-1", typed.Region)
	assert.Equal(t, "api.example.com", typed.DomainName)
	assert.Equal(t, "DNS", typed.ValidationMethod)
	assert.Equal(t, "RSA_2048", typed.KeyAlgorithm)
	assert.Equal(t, "api-cert", typed.Tags["Name"])
	assert.Equal(t, "dev", typed.Tags["env"])
}

func TestACMCertificateAdapter_BuildImportKey(t *testing.T) {
	adapter := NewACMCertificateAdapterWithAPI(&mockACMAPI{describe: map[string]acmcert.ObservedState{
		"arn:aws:acm:us-east-1:123456789012:certificate/abc": {DomainName: "api.example.com"},
	}})
	key, err := adapter.BuildImportKey("us-east-1", "arn:aws:acm:us-east-1:123456789012:certificate/abc")
	require.NoError(t, err)
	assert.Equal(t, "us-east-1~api.example.com", key)
}

func TestACMCertificateAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewACMCertificateAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(acmcert.ACMCertificateOutputs{
		CertificateArn: "arn:aws:acm:us-east-1:123456789012:certificate/abc",
		DomainName:     "api.example.com",
		Status:         "ISSUED",
		DNSValidationRecords: []acmcert.DNSValidationRecord{{
			DomainName:          "api.example.com",
			ResourceRecordName:  "_x.api.example.com",
			ResourceRecordType:  "CNAME",
			ResourceRecordValue: "_value.acm-validations.aws.",
		}},
		NotBefore: "2026-01-01T00:00:00Z",
		NotAfter:  "2027-01-01T00:00:00Z",
	})
	require.NoError(t, err)
	assert.Equal(t, "api.example.com", out["domainName"])
	assert.Equal(t, "ISSUED", out["status"])
	assert.Equal(t, "2026-01-01T00:00:00Z", out["notBefore"])
	assert.Equal(t, "2027-01-01T00:00:00Z", out["notAfter"])
	assert.Len(t, out["dnsValidationRecords"], 1)
}
