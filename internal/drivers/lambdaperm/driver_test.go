package lambdaperm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/pkg/types"
)

func TestPermissionServiceName(t *testing.T) {
	assert.Equal(t, ServiceName, NewLambdaPermissionDriver(nil).ServiceName())
}

func TestPermissionValidateProvisionSpec(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{Region: "us-east-1", FunctionName: "processor", StatementId: "allow-s3", Principal: "s3.amazonaws.com"})
	require.NoError(t, validateProvisionSpec(spec))
}

func TestPermissionDefaultImportMode(t *testing.T) {
	assert.Equal(t, types.ModeObserved, defaultImportMode(""))
	assert.Equal(t, types.ModeManaged, defaultImportMode(types.ModeManaged))
}

func TestPermissionValidateProvisionSpec_MissingRegion(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{FunctionName: "fn", StatementId: "sid", Principal: "s3.amazonaws.com"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestPermissionValidateProvisionSpec_MissingFunctionName(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{Region: "us-east-1", StatementId: "sid", Principal: "s3.amazonaws.com"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestPermissionValidateProvisionSpec_MissingStatementId(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{Region: "us-east-1", FunctionName: "fn", Principal: "s3.amazonaws.com"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestPermissionValidateProvisionSpec_MissingPrincipal(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{Region: "us-east-1", FunctionName: "fn", StatementId: "sid"})
	assert.Error(t, validateProvisionSpec(spec))
}

func TestPermissionApplyDefaults_ActionSetIfEmpty(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{})
	assert.Equal(t, "lambda:InvokeFunction", spec.Action)
}

func TestPermissionApplyDefaults_PreservesExistingAction(t *testing.T) {
	spec := applyDefaults(LambdaPermissionSpec{Action: "lambda:GetFunction"})
	assert.Equal(t, "lambda:GetFunction", spec.Action)
}

func TestPermissionSpecChanged_NoChange(t *testing.T) {
	spec := LambdaPermissionSpec{FunctionName: "fn", StatementId: "sid", Action: "lambda:InvokeFunction",
		Principal: "s3.amazonaws.com", SourceArn: "arn", SourceAccount: "123", EventSourceToken: "tok", Qualifier: "v1"}
	assert.False(t, specChanged(spec, spec))
}

func TestPermissionSpecChanged_FunctionNameChanged(t *testing.T) {
	a := LambdaPermissionSpec{FunctionName: "fn-old"}
	b := LambdaPermissionSpec{FunctionName: "fn-new"}
	assert.True(t, specChanged(a, b))
}

func TestPermissionSpecChanged_QualifierChanged(t *testing.T) {
	a := LambdaPermissionSpec{FunctionName: "fn", Qualifier: "v1"}
	b := LambdaPermissionSpec{FunctionName: "fn", Qualifier: "v2"}
	assert.True(t, specChanged(a, b))
}

func TestPermissionSpecFromObserved(t *testing.T) {
	observed := ObservedState{
		FunctionName: "my-function", StatementId: "allow-s3",
		Action: "lambda:InvokeFunction", Principal: "s3.amazonaws.com",
		SourceArn: "arn:aws:s3:::bucket", SourceAccount: "123456789012",
	}
	spec := specFromObserved(observed)
	assert.Equal(t, "my-function", spec.FunctionName)
	assert.Equal(t, "allow-s3", spec.StatementId)
	assert.Equal(t, "lambda:InvokeFunction", spec.Action)
	assert.Equal(t, "s3.amazonaws.com", spec.Principal)
	assert.Equal(t, "arn:aws:s3:::bucket", spec.SourceArn)
	assert.Equal(t, "123456789012", spec.SourceAccount)
}

func TestPermissionSplitImportResourceID_Valid(t *testing.T) {
	fn, sid, err := splitImportResourceID("my-function~allow-s3")
	require.NoError(t, err)
	assert.Equal(t, "my-function", fn)
	assert.Equal(t, "allow-s3", sid)
}

func TestPermissionSplitImportResourceID_Invalid(t *testing.T) {
	_, _, err := splitImportResourceID("no-separator")
	assert.Error(t, err)
}

func TestPermissionSplitImportResourceID_EmptyParts(t *testing.T) {
	_, _, err := splitImportResourceID("~sid")
	assert.Error(t, err)
	_, _, err = splitImportResourceID("fn~")
	assert.Error(t, err)
}
