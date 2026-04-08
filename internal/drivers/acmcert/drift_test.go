package acmcert

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_FalseWhenTagsAndOptionsMatch(t *testing.T) {
	desired := ACMCertificateSpec{
		Tags:    map[string]string{"Name": "api-cert", "env": "dev"},
		Options: &CertificateOptions{CertificateTransparencyLoggingPreference: "DISABLED"},
	}
	observed := ObservedState{
		Tags:    map[string]string{"Name": "api-cert", "env": "dev", "praxis:managed-key": "us-east-1~api-cert"},
		Options: CertificateOptions{CertificateTransparencyLoggingPreference: "DISABLED"},
	}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_TrueWhenOptionDiffers(t *testing.T) {
	desired := ACMCertificateSpec{Options: &CertificateOptions{CertificateTransparencyLoggingPreference: "DISABLED"}}
	observed := ObservedState{Options: CertificateOptions{CertificateTransparencyLoggingPreference: "ENABLED"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_IncludesOptionAndTagDiffs(t *testing.T) {
	desired := ACMCertificateSpec{
		Tags:    map[string]string{"env": "prod"},
		Options: &CertificateOptions{CertificateTransparencyLoggingPreference: "DISABLED"},
	}
	observed := ObservedState{
		Tags:    map[string]string{"env": "dev", "praxis:managed-key": "k"},
		Options: CertificateOptions{CertificateTransparencyLoggingPreference: "ENABLED"},
	}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 2)
	assert.Equal(t, "options.certificateTransparencyLoggingPreference", diffs[0].Path)
	assert.Equal(t, "tags.env", diffs[1].Path)
}

func TestComputeFieldDiffs_ImmutableDomainName(t *testing.T) {
	desired := ACMCertificateSpec{DomainName: "new.example.com"}
	observed := ObservedState{DomainName: "old.example.com"}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.NotEmpty(t, diffs)
	assert.Equal(t, "spec.domainName (immutable, requires replacement)", diffs[0].Path)
}

func TestComputeFieldDiffs_ImmutableSANs(t *testing.T) {
	desired := ACMCertificateSpec{
		DomainName:              "api.example.com",
		SubjectAlternativeNames: []string{"api.example.com", "new.example.com"},
	}
	observed := ObservedState{
		DomainName:              "api.example.com",
		SubjectAlternativeNames: []string{"api.example.com", "www.example.com"},
	}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.NotEmpty(t, diffs)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.subjectAlternativeNames (immutable, requires replacement)" {
			found = true
		}
	}
	assert.True(t, found, "expected immutable SAN diff")
}

func TestComputeFieldDiffs_ImmutableKeyAlgorithm(t *testing.T) {
	desired := ACMCertificateSpec{DomainName: "api.example.com", KeyAlgorithm: "EC_prime256v1"}
	observed := ObservedState{DomainName: "api.example.com", KeyAlgorithm: "RSA_2048"}
	diffs := ComputeFieldDiffs(desired, observed)
	found := false
	for _, d := range diffs {
		if d.Path == "spec.keyAlgorithm (immutable, requires replacement)" {
			found = true
		}
	}
	assert.True(t, found, "expected immutable keyAlgorithm diff")
}

func TestComputeFieldDiffs_NoImmutableDiffsWhenMatching(t *testing.T) {
	desired := ACMCertificateSpec{
		DomainName:              "api.example.com",
		SubjectAlternativeNames: []string{"api.example.com"},
	}
	observed := ObservedState{
		DomainName:              "api.example.com",
		SubjectAlternativeNames: []string{"api.example.com"},
	}
	diffs := ComputeFieldDiffs(desired, observed)
	for _, d := range diffs {
		assert.NotContains(t, d.Path, "immutable")
	}
}
