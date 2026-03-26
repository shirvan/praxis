//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	acmsdk "github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/acmcert"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueACMCertName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(strings.ToLower(t.Name()), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 40 {
		name = name[:40]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000)
}

func skipIfACMUnavailable(t *testing.T, client *acmsdk.Client) {
	t.Helper()
	_, err := client.ListCertificates(context.Background(), &acmsdk.ListCertificatesInput{})
	if err != nil {
		t.Skipf("ACM API unavailable in test environment: %v", err)
	}
}

func setupACMCertificateDriver(t *testing.T) (*ingress.Client, *acmsdk.Client) {
	t.Helper()
	configureLocalAccount(t)
	awsCfg := localstackAWSConfig(t)
	acmClient := awsclient.NewACMClient(awsCfg)
	skipIfACMUnavailable(t, acmClient)
	driver := acmcert.NewACMCertificateDriver(authservice.NewAuthClient())
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), acmClient
}

func TestACMCertificateProvision(t *testing.T) {
	client, acmClient := setupACMCertificateDriver(t)
	name := uniqueACMCertName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	domainName := fmt.Sprintf("%s.example.com", name)

	outputs, err := ingress.Object[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs](client, acmcert.ServiceName, key, "Provision").Request(t.Context(), acmcert.ACMCertificateSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		DomainName:       domainName,
		ValidationMethod: "DNS",
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, outputs.CertificateArn)
	assert.Equal(t, domainName, outputs.DomainName)

	desc, err := acmClient.DescribeCertificate(context.Background(), &acmsdk.DescribeCertificateInput{CertificateArn: aws.String(outputs.CertificateArn)})
	require.NoError(t, err)
	assert.Equal(t, domainName, aws.ToString(desc.Certificate.DomainName))
}

func TestACMCertificateGetStatus(t *testing.T) {
	client, _ := setupACMCertificateDriver(t)
	name := uniqueACMCertName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	domainName := fmt.Sprintf("%s.example.com", name)

	_, err := ingress.Object[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs](client, acmcert.ServiceName, key, "Provision").Request(t.Context(), acmcert.ACMCertificateSpec{
		Account:          integrationAccountName,
		Region:           "us-east-1",
		DomainName:       domainName,
		ValidationMethod: "DNS",
		ManagedKey:       key,
		Tags:             map[string]string{"Name": name},
	})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, acmcert.ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
	assert.Greater(t, status.Generation, int64(0))
}
