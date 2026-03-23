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
