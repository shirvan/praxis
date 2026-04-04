//go:build integration

package integration

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/ami"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueAMIName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 60 {
		name = name[:60]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%100000)
}

func setupAMIDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := awsclient.NewEC2Client(awsCfg)
	driver := ami.NewAMIDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func createExternalTestAMI(t *testing.T, ec2Client *ec2sdk.Client) string {
	t.Helper()
	name := uniqueAMIName(t) + "-external"
	out, err := ec2Client.CopyImage(context.Background(), &ec2sdk.CopyImageInput{
		Name:          aws.String(name),
		Description:   aws.String("external test ami"),
		SourceImageId: aws.String("ami-0123456789abcdef0"),
		SourceRegion:  aws.String("us-east-1"),
	})
	if err != nil {
		t.Skipf("Moto AMI copy support unavailable: %v", err)
	}
	imageID := aws.ToString(out.ImageId)
	require.NotEmpty(t, imageID)

	waiter := ec2sdk.NewImageAvailableWaiter(ec2Client)
	if err := waiter.Wait(context.Background(), &ec2sdk.DescribeImagesInput{ImageIds: []string{imageID}}, 3*time.Minute); err != nil {
		t.Skipf("Moto AMI availability waiter unsupported: %v", err)
	}
	return imageID
}

func TestAMIProvision_CopyImage(t *testing.T) {
	client, _ := setupAMIDriver(t)
	name := uniqueAMIName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[ami.AMISpec, ami.AMIOutputs](client, "AMI", key, "Provision").Request(t.Context(), ami.AMISpec{
		Account:     integrationAccountName,
		Region:      "us-east-1",
		Name:        name,
		Description: "copied test image",
		Source: ami.SourceSpec{FromAMI: &ami.FromAMISpec{
			SourceImageId: "ami-0123456789abcdef0",
		}},
		ManagedKey: key,
		Tags:       map[string]string{"Name": name, "env": "test"},
	})
	if err != nil {
		t.Skipf("Moto AMI provision support unavailable: %v", err)
	}
	require.NotEmpty(t, outputs.ImageId)
	assert.Equal(t, name, outputs.Name)
	assert.NotEmpty(t, outputs.State)
}

func TestAMIImport_DefaultsToObserved(t *testing.T) {
	client, ec2Client := setupAMIDriver(t)
	imageID := createExternalTestAMI(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", imageID)

	outputs, err := ingress.Object[types.ImportRef, ami.AMIOutputs](client, "AMI", key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: imageID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, imageID, outputs.ImageId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "AMI", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestAMIDelete_ObservedModeBlocked(t *testing.T) {
	client, ec2Client := setupAMIDriver(t)
	imageID := createExternalTestAMI(t, ec2Client)
	key := fmt.Sprintf("us-east-1~%s", imageID)

	_, err := ingress.Object[types.ImportRef, ami.AMIOutputs](client, "AMI", key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: imageID,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, "AMI", key, "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}
