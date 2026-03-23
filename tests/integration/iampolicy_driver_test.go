//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	iamsdk "github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/iampolicy"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func setupIAMPolicyDriver(t *testing.T) (*ingress.Client, *iamsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	iamClient := awsclient.NewIAMClient(awsCfg)
	driver := iampolicy.NewIAMPolicyDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), iamClient
}

func uniquePolicyName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 90 {
		name = name[:90]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func allowAllS3PolicyDoc() string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":"*"}]}`
}

func denyAllS3PolicyDoc() string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:GetObject"],"Resource":"*"}]}`
}

func TestIAMPolicyProvision_CreatesPolicy(t *testing.T) {
	client, iamClient := setupIAMPolicyDriver(t)
	name := uniquePolicyName(t)

	outputs, err := ingress.Object[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Provision").Request(t.Context(), iampolicy.IAMPolicySpec{
		Account:        integrationAccountName,
		PolicyName:     name,
		Path:           "/app/",
		PolicyDocument: allowAllS3PolicyDoc(),
		Description:    "integration test policy",
		Tags:           map[string]string{"env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.PolicyName)
	assert.Contains(t, outputs.Arn, name)

	policyOut, err := iamClient.GetPolicy(context.Background(), &iamsdk.GetPolicyInput{PolicyArn: &outputs.Arn})
	require.NoError(t, err)
	assert.Equal(t, name, *policyOut.Policy.PolicyName)
}

func TestIAMPolicyProvision_IdempotentAndUpdatesDocument(t *testing.T) {
	client, iamClient := setupIAMPolicyDriver(t)
	name := uniquePolicyName(t)
	spec := iampolicy.IAMPolicySpec{
		Account:        integrationAccountName,
		PolicyName:     name,
		Path:           "/",
		PolicyDocument: allowAllS3PolicyDoc(),
		Tags:           map[string]string{"env": "test"},
	}

	first, err := ingress.Object[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	spec.PolicyDocument = denyAllS3PolicyDoc()
	second, err := ingress.Object[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, first.Arn, second.Arn)

	policyOut, err := iamClient.GetPolicy(context.Background(), &iamsdk.GetPolicyInput{PolicyArn: &second.Arn})
	require.NoError(t, err)
	versionOut, err := iamClient.GetPolicyVersion(context.Background(), &iamsdk.GetPolicyVersionInput{PolicyArn: &second.Arn, VersionId: policyOut.Policy.DefaultVersionId})
	require.NoError(t, err)
	assert.Contains(t, *versionOut.PolicyVersion.Document, "Deny")
}

func TestIAMPolicyImport_ExistingPolicyDefaultsObserved(t *testing.T) {
	client, iamClient := setupIAMPolicyDriver(t)
	name := uniquePolicyName(t)
	doc := allowAllS3PolicyDoc()

	created, err := iamClient.CreatePolicy(context.Background(), &iamsdk.CreatePolicyInput{PolicyName: &name, PolicyDocument: &doc})
	require.NoError(t, err)

	outputs, err := ingress.Object[types.ImportRef, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Import").Request(t.Context(), types.ImportRef{ResourceID: name, Account: integrationAccountName})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.PolicyName)
	assert.Equal(t, *created.Policy.Arn, outputs.Arn)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iampolicy.ServiceName, name, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestIAMPolicyDelete_RemovesPolicy(t *testing.T) {
	client, iamClient := setupIAMPolicyDriver(t)
	name := uniquePolicyName(t)

	outputs, err := ingress.Object[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Provision").Request(t.Context(), iampolicy.IAMPolicySpec{Account: integrationAccountName, PolicyName: name, PolicyDocument: allowAllS3PolicyDoc()})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, iampolicy.ServiceName, name, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = iamClient.GetPolicy(context.Background(), &iamsdk.GetPolicyInput{PolicyArn: &outputs.Arn})
	require.Error(t, err)
}

func TestIAMPolicyReconcile_DetectsAndFixesDrift(t *testing.T) {
	client, iamClient := setupIAMPolicyDriver(t)
	name := uniquePolicyName(t)

	outputs, err := ingress.Object[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Provision").Request(t.Context(), iampolicy.IAMPolicySpec{Account: integrationAccountName, PolicyName: name, PolicyDocument: allowAllS3PolicyDoc()})
	require.NoError(t, err)

	doc := denyAllS3PolicyDoc()
	_, err = iamClient.CreatePolicyVersion(context.Background(), &iamsdk.CreatePolicyVersionInput{PolicyArn: &outputs.Arn, PolicyDocument: &doc, SetAsDefault: true})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, iampolicy.ServiceName, name, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	policyOut, err := iamClient.GetPolicy(context.Background(), &iamsdk.GetPolicyInput{PolicyArn: &outputs.Arn})
	require.NoError(t, err)
	versionOut, err := iamClient.GetPolicyVersion(context.Background(), &iamsdk.GetPolicyVersionInput{PolicyArn: &outputs.Arn, VersionId: policyOut.Policy.DefaultVersionId})
	require.NoError(t, err)
	assert.Contains(t, *versionOut.PolicyVersion.Document, "Allow")
}

func TestIAMPolicyGetStatus_ReturnsReady(t *testing.T) {
	client, _ := setupIAMPolicyDriver(t)
	name := uniquePolicyName(t)

	_, err := ingress.Object[iampolicy.IAMPolicySpec, iampolicy.IAMPolicyOutputs](client, iampolicy.ServiceName, name, "Provision").Request(t.Context(), iampolicy.IAMPolicySpec{Account: integrationAccountName, PolicyName: name, PolicyDocument: allowAllS3PolicyDoc()})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, iampolicy.ServiceName, name, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
