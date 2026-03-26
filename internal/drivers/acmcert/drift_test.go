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
