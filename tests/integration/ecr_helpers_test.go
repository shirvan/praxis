//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/ecrpolicy"
	"github.com/shirvan/praxis/internal/drivers/ecrrepo"
	"github.com/shirvan/praxis/internal/infra/awsclient"
)

func uniqueRepoName(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func skipIfECRUnavailable(t *testing.T, client *ecrsdk.Client) {
	t.Helper()
	_, err := client.DescribeRepositories(context.Background(), &ecrsdk.DescribeRepositoriesInput{})
	if err != nil && !ecrrepo.IsNotFound(err) {
		t.Skipf("ECR API unavailable in test environment: %v", err)
	}
}

func setupECRRepoDriver(t *testing.T) (*ingress.Client, *ecrsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ecrClient := awsclient.NewECRClient(awsCfg)
	skipIfECRUnavailable(t, ecrClient)

	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(ecrrepo.NewECRRepositoryDriver(authservice.NewAuthClient())),
	)
	return env.Ingress(), ecrClient
}

func setupECRPolicyDriver(t *testing.T) (*ingress.Client, *ecrsdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ecrClient := awsclient.NewECRClient(awsCfg)
	skipIfECRUnavailable(t, ecrClient)

	env := restatetest.Start(t,
		restate.Reflect(authservice.NewAuthService(authservice.LoadBootstrapFromEnv())),
		restate.Reflect(ecrpolicy.NewECRLifecyclePolicyDriver(authservice.NewAuthClient())),
	)
	return env.Ingress(), ecrClient
}
