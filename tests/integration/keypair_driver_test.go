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
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"

	"github.com/shirvan/praxis/internal/drivers/keypair"
	"github.com/shirvan/praxis/pkg/types"
)

func uniqueKeyPairName(t *testing.T) string {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-")
	name = strings.ReplaceAll(name, "_", "-")
	if len(name) > 50 {
		name = name[:50]
	}
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano()%1000000000)
}

func setupKeyPairDriver(t *testing.T) (*ingress.Client, *ec2sdk.Client) {
	t.Helper()
	configureLocalAccount(t)

	awsCfg := localstackAWSConfig(t)
	ec2Client := ec2sdk.NewFromConfig(awsCfg)
	driver := keypair.NewKeyPairDriver(nil)

	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress(), ec2Client
}

func TestKeyPairProvision_CreatesKeyPair(t *testing.T) {
	client, ec2Client := setupKeyPairDriver(t)
	name := uniqueKeyPairName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	outputs, err := ingress.Object[keypair.KeyPairSpec, keypair.KeyPairOutputs](client, "KeyPair", key, "Provision").Request(t.Context(), keypair.KeyPairSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		KeyName: name,
		KeyType: "rsa",
		Tags:    map[string]string{"Name": name, "env": "test"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.KeyName)
	assert.NotEmpty(t, outputs.KeyPairId)
	assert.NotEmpty(t, outputs.KeyFingerprint)
	assert.NotEmpty(t, outputs.PrivateKeyMaterial)

	desc, err := ec2Client.DescribeKeyPairs(context.Background(), &ec2sdk.DescribeKeyPairsInput{KeyNames: []string{name}})
	require.NoError(t, err)
	require.Len(t, desc.KeyPairs, 1)
	assert.Equal(t, outputs.KeyPairId, aws.ToString(desc.KeyPairs[0].KeyPairId))
	assert.Equal(t, outputs.KeyFingerprint, aws.ToString(desc.KeyPairs[0].KeyFingerprint))
}

func TestKeyPairProvision_Idempotent(t *testing.T) {
	client, _ := setupKeyPairDriver(t)
	name := uniqueKeyPairName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	spec := keypair.KeyPairSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		KeyName: name,
		KeyType: "rsa",
		Tags:    map[string]string{"Name": name},
	}

	out1, err := ingress.Object[keypair.KeyPairSpec, keypair.KeyPairOutputs](client, "KeyPair", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)
	out2, err := ingress.Object[keypair.KeyPairSpec, keypair.KeyPairOutputs](client, "KeyPair", key, "Provision").Request(t.Context(), spec)
	require.NoError(t, err)

	assert.Equal(t, out1.KeyPairId, out2.KeyPairId)
	assert.NotEmpty(t, out1.PrivateKeyMaterial)
	assert.Empty(t, out2.PrivateKeyMaterial)

	stored, err := ingress.Object[restate.Void, keypair.KeyPairOutputs](client, "KeyPair", key, "GetOutputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Empty(t, stored.PrivateKeyMaterial)
	assert.Equal(t, out1.KeyPairId, stored.KeyPairId)
}

func TestKeyPairProvision_ImportPublicKey(t *testing.T) {
	client, _ := setupKeyPairDriver(t)
	name := uniqueKeyPairName(t)
	key := fmt.Sprintf("us-east-1~%s", name)
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC9nKdNEhyWj7L6VR/80+65OlrVc8W+gyhKhMSKRO4L7bQd4MumLregNNjb1elO6CMoCtvblJ207O5L3KlQQZ72srMnk40GvPrT/vTBRl9u+kMG3IGotwcrd184NPaCF3PsftHNWciGUUPnYoPkhZEt/ekQaZ0K29W4nhhyTO2boucCIQYC9uZPOmjr7e6bmxkdPpCIrpNARSOYolMyrbVx3XOl1CFShdsnDJIKzAAM2z7gLDbmlVJcS6gvTGq6C3jN9fOhppavy2x+UmCvanRTi4NHS7bIOAv76vqtarYXDYIaSrhSPhCNObnl+1jiUUPnrJKE/EvRjQf6SWJd3qwv test@localhost"

	outputs, err := ingress.Object[keypair.KeyPairSpec, keypair.KeyPairOutputs](client, "KeyPair", key, "Provision").Request(t.Context(), keypair.KeyPairSpec{
		Account:           integrationAccountName,
		Region:            "us-east-1",
		KeyName:           name,
		KeyType:           "rsa",
		PublicKeyMaterial: publicKey,
		Tags:              map[string]string{"Name": name, "env": "imported"},
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.KeyName)
	assert.NotEmpty(t, outputs.KeyPairId)
	assert.Empty(t, outputs.PrivateKeyMaterial)
}

func TestKeyPairImport_ExistingKeyPair(t *testing.T) {
	client, ec2Client := setupKeyPairDriver(t)
	name := uniqueKeyPairName(t)

	createOut, err := ec2Client.CreateKeyPair(context.Background(), &ec2sdk.CreateKeyPairInput{
		KeyName: aws.String(name),
		KeyType: ec2types.KeyTypeRsa,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(createOut.KeyPairId))

	key := fmt.Sprintf("us-east-1~%s", name)
	outputs, err := ingress.Object[types.ImportRef, keypair.KeyPairOutputs](client, "KeyPair", key, "Import").Request(t.Context(), types.ImportRef{
		ResourceID: name,
		Account:    integrationAccountName,
	})
	require.NoError(t, err)
	assert.Equal(t, name, outputs.KeyName)
	assert.Equal(t, aws.ToString(createOut.KeyPairId), outputs.KeyPairId)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, "KeyPair", key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestKeyPairDelete_RemovesKeyPair(t *testing.T) {
	client, ec2Client := setupKeyPairDriver(t)
	name := uniqueKeyPairName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	_, err := ingress.Object[keypair.KeyPairSpec, keypair.KeyPairOutputs](client, "KeyPair", key, "Provision").Request(t.Context(), keypair.KeyPairSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		KeyName: name,
		KeyType: "rsa",
		Tags:    map[string]string{"Name": name},
	})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, "KeyPair", key, "Delete").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	_, err = ec2Client.DescribeKeyPairs(context.Background(), &ec2sdk.DescribeKeyPairsInput{KeyNames: []string{name}})
	require.Error(t, err, "key pair should be deleted from LocalStack")
}

func TestKeyPairReconcile_DetectsTagDrift(t *testing.T) {
	client, ec2Client := setupKeyPairDriver(t)
	name := uniqueKeyPairName(t)
	key := fmt.Sprintf("us-east-1~%s", name)

	out, err := ingress.Object[keypair.KeyPairSpec, keypair.KeyPairOutputs](client, "KeyPair", key, "Provision").Request(t.Context(), keypair.KeyPairSpec{
		Account: integrationAccountName,
		Region:  "us-east-1",
		KeyName: name,
		KeyType: "rsa",
		Tags:    map[string]string{"Name": name, "env": "managed"},
	})
	require.NoError(t, err)

	_, err = ec2Client.DeleteTags(context.Background(), &ec2sdk.DeleteTagsInput{
		Resources: []string{out.KeyPairId},
		Tags:      []ec2types.Tag{{Key: aws.String("env")}},
	})
	require.NoError(t, err)

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, "KeyPair", key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)

	desc, err := ec2Client.DescribeKeyPairs(context.Background(), &ec2sdk.DescribeKeyPairsInput{KeyNames: []string{name}})
	require.NoError(t, err)
	require.Len(t, desc.KeyPairs, 1)
	assert.Contains(t, desc.KeyPairs[0].Tags, ec2types.Tag{Key: aws.String("env"), Value: aws.String("managed")})
}
