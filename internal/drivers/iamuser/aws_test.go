package iamuser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.True(t, IsNotFound(&mockAPIError{code: "NoSuchEntity"}))
}

func TestIsAlreadyExists_True(t *testing.T) {
	assert.True(t, IsAlreadyExists(&mockAPIError{code: "EntityAlreadyExists"}))
}

func TestIsDeleteConflict_True(t *testing.T) {
	assert.True(t, IsDeleteConflict(&mockAPIError{code: "DeleteConflict"}))
}

func TestRealIAMUserAPIDeleteOnlyRequestsDeleteUser(t *testing.T) {
	var actions []string
	httpClient := smithyhttp.ClientDoFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		actions = append(actions, values.Get("Action"))
		return &http.Response{
			StatusCode: http.StatusConflict,
			Header:     http.Header{"Content-Type": []string{"text/xml"}},
			Body: io.NopCloser(strings.NewReader(
				`<ErrorResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><Error><Type>Sender</Type><Code>DeleteConflict</Code><Message>user has external credentials</Message></Error><RequestId>test</RequestId></ErrorResponse>`,
			)),
			Request: request,
		}, nil
	})
	client := iamsdk.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient:  httpClient,
	}, func(options *iamsdk.Options) {
		options.BaseEndpoint = aws.String("https://iam.test")
		options.RetryMaxAttempts = 1
	})
	api := NewIAMUserAPI(client)

	err := api.DeleteUser(context.Background(), "credential-user")
	require.Error(t, err)
	assert.True(t, IsDeleteConflict(err))
	assert.Equal(t, []string{"DeleteUser"}, actions, "deletion must not issue credential-cleanup actions")
}
