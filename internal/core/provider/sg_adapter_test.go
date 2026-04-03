package provider

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sgdrv "github.com/shirvan/praxis/internal/drivers/sg"
)

func TestSecurityGroupAdapter_BuildKeyAndDecodeSpec(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SecurityGroup",
		"metadata":{"name":"web-sg"},
		"spec":{
			"groupName":"web-sg",
			"description":"Web security group",
			"vpcId":"vpc-123",
			"ingressRules":[{"protocol":"tcp","fromPort":443,"toPort":443,"cidrBlock":"0.0.0.0/0"}],
			"egressRules":[{"protocol":"all","fromPort":0,"toPort":65535,"cidrBlock":"0.0.0.0/0"}],
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "vpc-123~web-sg", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	typed, ok := decoded.(sgdrv.SecurityGroupSpec)
	require.True(t, ok)
	assert.Equal(t, "web-sg", typed.GroupName)
	assert.Equal(t, "Web security group", typed.Description)
	assert.Equal(t, "vpc-123", typed.VpcId)
	assert.Len(t, typed.IngressRules, 1)
	assert.Len(t, typed.EgressRules, 1)
}

func TestSecurityGroupAdapter_BuildImportKey_ByGroupId(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "sg-0abc123")
	require.NoError(t, err)
	// SG import key is just the resource ID (group ID)
	assert.Equal(t, "sg-0abc123", key)
}

func TestSecurityGroupAdapter_KindAndServiceName(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	assert.Equal(t, "SecurityGroup", adapter.Kind())
	assert.Equal(t, "SecurityGroup", adapter.ServiceName())
}

func TestSecurityGroupAdapter_Scope(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())
}

func TestSecurityGroupAdapter_NormalizeOutputs_AllFields(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(sgdrv.SecurityGroupOutputs{
		GroupId:  "sg-abc",
		GroupArn: "arn:aws:ec2:eu-west-1:999999999999:security-group/sg-abc",
		VpcId:    "vpc-xyz",
	})
	require.NoError(t, err)
	assert.Equal(t, "sg-abc", out["groupId"])
	assert.Equal(t, "arn:aws:ec2:eu-west-1:999999999999:security-group/sg-abc", out["groupArn"])
	assert.Equal(t, "vpc-xyz", out["vpcId"])
}

func TestSecurityGroupAdapter_DecodeSpec_MissingGroupName(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SecurityGroup",
		"metadata":{"name":"web-sg"},
		"spec":{"description":"test","vpcId":"vpc-123"}
	}`)
	_, err := adapter.DecodeSpec(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "groupName")
}

func TestSecurityGroupAdapter_BuildKey_MissingVpcId(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SecurityGroup",
		"metadata":{"name":"web-sg"},
		"spec":{"groupName":"web-sg","description":"test"}
	}`)
	_, err := adapter.BuildKey(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VPC ID")
}

func TestSecurityGroupAdapter_BuildKey_MissingGroupName(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1","kind":"SecurityGroup",
		"metadata":{"name":"web-sg"},
		"spec":{"description":"test","vpcId":"vpc-123"}
	}`)
	_, err := adapter.BuildKey(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "groupName")
}
