//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/resolver"
)

func motoSSMClient(t *testing.T) *ssm.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(
			func(ctx context.Context) (aws.Credentials, error) {
				return aws.Credentials{
					AccessKeyID:     "test",
					SecretAccessKey: "test",
				}, nil
			},
		)),
	)
	require.NoError(t, err)
	cfg.BaseEndpoint = aws.String(motoEndpoint)
	return ssm.NewFromConfig(cfg)
}

func TestTemplate_SSMResolution_Integration(t *testing.T) {
	ssmClient := motoSSMClient(t)

	// Seed the parameter directly: moto-init also creates it, but tests that
	// reset Moto (lifecycle) wipe that seed, so this test provides its own.
	_, err := ssmClient.PutParameter(context.Background(), &ssm.PutParameterInput{
		Name:      aws.String("/praxis/dev/db-password"),
		Value:     aws.String("test-password-dev"),
		Type:      "SecureString",
		Overwrite: aws.Bool(true),
	})
	require.NoError(t, err)

	r := resolver.NewSSMResolver(ssmClient)

	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"password":"ssm:///praxis/dev/db-password","host":"db.example.com"}`),
	}

	result, err := r.Resolve(context.Background(), specs)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result["db"], &parsed))
	assert.Equal(t, "test-password-dev", parsed["password"])
	assert.Equal(t, "db.example.com", parsed["host"])
}

func TestTemplate_SSMResolution_MissingParam_Integration(t *testing.T) {
	ssmClient := motoSSMClient(t)
	r := resolver.NewSSMResolver(ssmClient)

	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"password":"ssm:///praxis/dev/nonexistent-param"}`),
	}

	_, err := r.Resolve(context.Background(), specs)
	require.Error(t, err, "should fail for missing SSM parameter")
}
