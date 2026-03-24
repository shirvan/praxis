package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrorCodeOmitEmpty(t *testing.T) {
	detail := DeploymentDetail{Key: "test", Status: DeploymentComplete}
	data, err := json.Marshal(detail)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "errorCode")
	assert.NotContains(t, string(data), "resourceErrors")
}

func TestErrorCodePresent(t *testing.T) {
	detail := DeploymentDetail{
		Key:       "test",
		Status:    DeploymentFailed,
		Error:     "something broke",
		ErrorCode: ErrCodeProvisionFailed,
		ResourceErrors: map[string]string{
			"db": "subnet not found",
		},
	}
	data, err := json.Marshal(detail)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"errorCode":"PROVISION_FAILED"`)
	assert.Contains(t, string(data), `"resourceErrors"`)
}
