package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseStatePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantRes string
		wantErr bool
	}{
		{"valid", "Deployment/web-app/myBucket", "web-app", "myBucket", false},
		{"missing prefix", "web-app/myBucket", "", "", true},
		{"only two parts", "Deployment/web-app", "", "", true},
		{"empty key", "Deployment//myBucket", "", "", true},
		{"empty resource", "Deployment/web-app/", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, res, err := parseStatePath(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, key)
			assert.Equal(t, tt.wantRes, res)
		})
	}
}

func TestParseDestination_BareName(t *testing.T) {
	key, res, err := parseDestination("newName", "web-app", "myBucket")
	require.NoError(t, err)
	assert.Equal(t, "web-app", key)
	assert.Equal(t, "newName", res)
}

func TestParseDestination_CrossDeployment(t *testing.T) {
	key, res, err := parseDestination("Deployment/data-stack/dataBucket", "web-app", "myBucket")
	require.NoError(t, err)
	assert.Equal(t, "data-stack", key)
	assert.Equal(t, "dataBucket", res)
}

func TestParseDestination_Empty(t *testing.T) {
	_, _, err := parseDestination("", "web-app", "myBucket")
	require.Error(t, err)
}

func TestParseDestination_SlashWithoutPrefix(t *testing.T) {
	_, _, err := parseDestination("bad/path", "web-app", "myBucket")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not start with 'Deployment/'")
}
