package route53record

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/drivers/awserr"
)

type mockAPIError struct {
	code    string
	message string
}

func (e *mockAPIError) Error() string                 { return fmt.Sprintf("%s: %s", e.code, e.message) }
func (e *mockAPIError) ErrorCode() string             { return e.code }
func (e *mockAPIError) ErrorMessage() string          { return e.message }
func (e *mockAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestIsNotFound_True(t *testing.T) {
	// A deleted hosted zone implies the record is gone too.
	assert.True(t, IsNotFound(&mockAPIError{code: "NoSuchHostedZone", message: "zone deleted"}))
	// DescribeRecord wraps awserr.ErrNotFound when the record set is absent.
	assert.True(t, IsNotFound(awserr.NotFound("record example.com A")))
}

func TestIsNotFound_False(t *testing.T) {
	assert.False(t, IsNotFound(nil))
	assert.False(t, IsNotFound(errors.New("some other error")))
	// InvalidInput is a real validation failure, not an already-deleted record.
	assert.False(t, IsNotFound(&mockAPIError{code: "InvalidInput", message: "bad change batch"}))
}

func TestIsConflict_True(t *testing.T) {
	assert.True(t, IsConflict(&mockAPIError{code: "PriorRequestNotComplete"}))
}

func TestIsInvalidInput_True(t *testing.T) {
	assert.True(t, IsInvalidInput(&mockAPIError{code: "InvalidInput"}))
	assert.True(t, IsInvalidInput(&mockAPIError{code: "InvalidChangeBatch"}))
}

func TestIsInvalidInput_False(t *testing.T) {
	assert.False(t, IsInvalidInput(nil))
	assert.False(t, IsInvalidInput(errors.New("some other error")))
}

func TestParseRecordIdentity_Simple(t *testing.T) {
	identity, err := parseRecordIdentity("Z123~example.com~A")
	assert.NoError(t, err)
	assert.Equal(t, "Z123", identity.HostedZoneId)
	assert.Equal(t, "example.com", identity.Name)
	assert.Equal(t, "A", identity.Type)
	assert.Empty(t, identity.SetIdentifier)
}

func TestParseRecordIdentity_WithSetIdentifier(t *testing.T) {
	identity, err := parseRecordIdentity("Z123~example.com~A~us-east-1")
	assert.NoError(t, err)
	assert.Equal(t, "Z123", identity.HostedZoneId)
	assert.Equal(t, "example.com", identity.Name)
	assert.Equal(t, "A", identity.Type)
	assert.Equal(t, "us-east-1", identity.SetIdentifier)
}

func TestParseRecordIdentity_InvalidTooFewParts(t *testing.T) {
	_, err := parseRecordIdentity("Z123~example.com")
	assert.Error(t, err)
}

func TestParseRecordIdentity_InvalidTooManyParts(t *testing.T) {
	_, err := parseRecordIdentity("Z123~example.com~A~set~extra")
	assert.Error(t, err)
}

// Route53 returns octal-escaped names from ListResourceRecordSets (e.g.
// "\052.example.com" for "*.example.com"). normalizeRecordName must decode
// them so names compare equal to the raw form users supply.
func TestNormalizeRecordName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "example.com", want: "example.com"},
		{name: "trailing dot stripped", in: "example.com.", want: "example.com"},
		{name: "uppercase lowered", in: "EXAMPLE.COM.", want: "example.com"},
		{name: "raw wildcard unchanged", in: "*.example.com", want: "*.example.com"},
		{name: "octal-escaped wildcard", in: `\052.example.com`, want: "*.example.com"},
		{name: "octal-escaped wildcard with trailing dot", in: `\052.example.com.`, want: "*.example.com"},
		{name: "octal-escaped at sign", in: `\100.example.com`, want: "@.example.com"},
		{name: "non-octal escape preserved", in: `\08a.example.com`, want: `\08a.example.com`},
		{name: "trailing backslash preserved", in: `example.com\`, want: `example.com\`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeRecordName(tt.in))
		})
	}
}

func TestToRoute53RecordSet_Weight(t *testing.T) {
	base := RecordSpec{HostedZoneId: "Z123", Name: "w.example.com", Type: "A", TTL: 60, ResourceRecords: []string{"1.2.3.4"}}

	t.Run("weight zero sent for weighted records", func(t *testing.T) {
		spec := base
		spec.SetIdentifier = "blue"
		spec.Weight = 0
		recordSet := toRoute53RecordSet(spec)
		if assert.NotNil(t, recordSet.Weight) {
			assert.Equal(t, int64(0), *recordSet.Weight)
		}
	})

	t.Run("nonzero weight sent for weighted records", func(t *testing.T) {
		spec := base
		spec.SetIdentifier = "green"
		spec.Weight = 10
		recordSet := toRoute53RecordSet(spec)
		if assert.NotNil(t, recordSet.Weight) {
			assert.Equal(t, int64(10), *recordSet.Weight)
		}
	})

	t.Run("no weight without set identifier", func(t *testing.T) {
		recordSet := toRoute53RecordSet(base)
		assert.Nil(t, recordSet.Weight)
	})

	t.Run("no weight for other routing policies", func(t *testing.T) {
		spec := base
		spec.SetIdentifier = "primary"
		spec.Failover = "PRIMARY"
		recordSet := toRoute53RecordSet(spec)
		assert.Nil(t, recordSet.Weight)
	})
}
