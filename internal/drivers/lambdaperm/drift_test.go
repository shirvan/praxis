package lambdaperm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------- HasDrift ----------

func TestPermissionHasDrift(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "events.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	assert.True(t, HasDrift(desired, observed))
}

func TestPermissionNoDrift(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket"}
	assert.False(t, HasDrift(desired, observed))
}

func TestPermissionHasDrift_ActionChanged(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com"}
	observed := ObservedState{Action: "lambda:GetFunction", Principal: "s3.amazonaws.com"}
	assert.True(t, HasDrift(desired, observed))
}

func TestPermissionHasDrift_SourceArnChanged(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket-new"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:aws:s3:::bucket-old"}
	assert.True(t, HasDrift(desired, observed))
}

func TestPermissionHasDrift_SourceAccountChanged(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceAccount: "111111111111"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceAccount: "222222222222"}
	assert.True(t, HasDrift(desired, observed))
}

func TestPermissionHasDrift_EventSourceTokenChanged(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", EventSourceToken: "token-new"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", EventSourceToken: "token-old"}
	assert.True(t, HasDrift(desired, observed))
}

func TestPermissionNoDrift_AllFieldsMatch(t *testing.T) {
	desired := LambdaPermissionSpec{
		Action: "lambda:InvokeFunction", Principal: "events.amazonaws.com",
		SourceArn: "arn:aws:events:us-east-1:123:rule/myrule", SourceAccount: "123456789012",
		EventSourceToken: "my-token",
	}
	observed := ObservedState{
		Action: "lambda:InvokeFunction", Principal: "events.amazonaws.com",
		SourceArn: "arn:aws:events:us-east-1:123:rule/myrule", SourceAccount: "123456789012",
		EventSourceToken: "my-token",
	}
	assert.False(t, HasDrift(desired, observed))
}

// ---------- ComputeFieldDiffs ----------

func TestPermissionComputeFieldDiffs_NoDiffs(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com"}
	assert.Empty(t, ComputeFieldDiffs(desired, observed))
}

func TestPermissionComputeFieldDiffs_ActionDiff(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com"}
	observed := ObservedState{Action: "lambda:GetFunction", Principal: "s3.amazonaws.com"}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.action", diffs[0].Path)
	assert.Equal(t, "lambda:GetFunction", diffs[0].OldValue)
	assert.Equal(t, "lambda:InvokeFunction", diffs[0].NewValue)
}

func TestPermissionComputeFieldDiffs_PrincipalDiff(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "events.amazonaws.com"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com"}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.principal", diffs[0].Path)
}

func TestPermissionComputeFieldDiffs_SourceArnDiff(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:new"}
	observed := ObservedState{Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com", SourceArn: "arn:old"}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "spec.sourceArn", diffs[0].Path)
}

func TestPermissionComputeFieldDiffs_MultipleDiffs(t *testing.T) {
	desired := LambdaPermissionSpec{Action: "lambda:InvokeFunction", Principal: "events.amazonaws.com",
		SourceArn: "arn:new", SourceAccount: "111", EventSourceToken: "tok-new"}
	observed := ObservedState{Action: "lambda:GetFunction", Principal: "s3.amazonaws.com",
		SourceArn: "arn:old", SourceAccount: "222", EventSourceToken: "tok-old"}
	diffs := ComputeFieldDiffs(desired, observed)
	assert.Len(t, diffs, 5)
}
