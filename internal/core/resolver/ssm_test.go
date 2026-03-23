package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/template"
)

// mockSSMClient implements SSMClient for testing.
type mockSSMClient struct {
	params map[string]string
	err    error
}

func (m *mockSSMClient) GetParameters(ctx context.Context, input *ssm.GetParametersInput, _ ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	if m.err != nil {
		return nil, m.err
	}

	var params []ssmtypes.Parameter
	var invalid []string

	for _, name := range input.Names {
		if val, ok := m.params[name]; ok {
			params = append(params, ssmtypes.Parameter{
				Name:  aws.String(name),
				Value: aws.String(val),
			})
		} else {
			invalid = append(invalid, name)
		}
	}

	return &ssm.GetParametersOutput{
		Parameters:        params,
		InvalidParameters: invalid,
	}, nil
}

func TestSSMResolver_Resolve_ReplacesURIs(t *testing.T) {
	client := &mockSSMClient{
		params: map[string]string{
			"/praxis/dev/db-password": "secret123",
		},
	}

	r := NewSSMResolver(client)
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"password":"ssm:///praxis/dev/db-password"}`),
	}

	result, err := r.Resolve(context.Background(), specs)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result["db"], &parsed))
	assert.Equal(t, "secret123", parsed["password"])
}

func TestSSMResolver_Resolve_NoSSMReferences(t *testing.T) {
	client := &mockSSMClient{params: map[string]string{}}
	r := NewSSMResolver(client)

	specs := map[string]json.RawMessage{
		"bucket": json.RawMessage(`{"region":"us-east-1"}`),
	}

	result, err := r.Resolve(context.Background(), specs)
	require.NoError(t, err)
	assert.JSONEq(t, `{"region":"us-east-1"}`, string(result["bucket"]))
}

func TestSSMResolver_Resolve_MissingParameter(t *testing.T) {
	client := &mockSSMClient{
		params: map[string]string{},
	}

	r := NewSSMResolver(client)
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"password":"ssm:///praxis/dev/missing-param"}`),
	}

	_, err := r.Resolve(context.Background(), specs)
	require.Error(t, err)

	var tErrs template.TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, template.ErrResolve, tErrs[0].Kind)
	assert.Contains(t, tErrs[0].Message, "not found")
}

func TestSSMResolver_Resolve_AccessDenied(t *testing.T) {
	client := &mockSSMClient{
		err: fmt.Errorf("AccessDeniedException: User is not authorized"),
	}

	r := NewSSMResolver(client)
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"password":"ssm:///praxis/dev/db-password"}`),
	}

	_, err := r.Resolve(context.Background(), specs)
	require.Error(t, err)

	var tErrs template.TemplateErrors
	require.ErrorAs(t, err, &tErrs)
	assert.Equal(t, template.ErrResolve, tErrs[0].Kind)
	assert.Contains(t, tErrs[0].Detail, "permission")
}

func TestSSMResolver_Resolve_MultipleParams(t *testing.T) {
	client := &mockSSMClient{
		params: map[string]string{
			"/praxis/dev/db-password": "dbpass",
			"/praxis/dev/api-key":     "apikey123",
		},
	}

	r := NewSSMResolver(client)
	specs := map[string]json.RawMessage{
		"db":  json.RawMessage(`{"password":"ssm:///praxis/dev/db-password"}`),
		"api": json.RawMessage(`{"key":"ssm:///praxis/dev/api-key"}`),
	}

	result, err := r.Resolve(context.Background(), specs)
	require.NoError(t, err)

	var dbParsed, apiParsed map[string]any
	require.NoError(t, json.Unmarshal(result["db"], &dbParsed))
	require.NoError(t, json.Unmarshal(result["api"], &apiParsed))
	assert.Equal(t, "dbpass", dbParsed["password"])
	assert.Equal(t, "apikey123", apiParsed["key"])
}

func TestSSMResolver_Resolve_DeduplicatesPaths(t *testing.T) {
	callCount := 0
	inner := &mockSSMClient{
		params: map[string]string{
			"/praxis/dev/shared-secret": "shared",
		},
	}
	client := &countingSSMClient{inner: inner, count: &callCount}

	r := NewSSMResolver(client)
	specs := map[string]json.RawMessage{
		"a": json.RawMessage(`{"secret":"ssm:///praxis/dev/shared-secret"}`),
		"b": json.RawMessage(`{"secret":"ssm:///praxis/dev/shared-secret"}`),
	}

	result, err := r.Resolve(context.Background(), specs)
	require.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, 1, callCount, "should deduplicate and make only one API call")
}

type countingSSMClient struct {
	inner SSMClient
	count *int
}

func (c *countingSSMClient) GetParameters(ctx context.Context, input *ssm.GetParametersInput, opts ...func(*ssm.Options)) (*ssm.GetParametersOutput, error) {
	*c.count++
	return c.inner.GetParameters(ctx, input, opts...)
}

func TestSSMResolver_Resolve_SpecialCharsInValue(t *testing.T) {
	client := &mockSSMClient{
		params: map[string]string{
			"/praxis/dev/password": "p@ss\"w0rd&<>",
		},
	}

	r := NewSSMResolver(client)
	specs := map[string]json.RawMessage{
		"db": json.RawMessage(`{"password":"ssm:///praxis/dev/password"}`),
	}

	result, err := r.Resolve(context.Background(), specs)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(result["db"], &parsed))
	assert.Equal(t, "p@ss\"w0rd&<>", parsed["password"])
}
