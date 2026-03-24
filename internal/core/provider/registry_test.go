package provider

import (
	"encoding/json"
	"testing"

	"github.com/shirvan/praxis/internal/drivers/ami"
	"github.com/shirvan/praxis/internal/drivers/ebs"
	"github.com/shirvan/praxis/internal/drivers/ec2"
	"github.com/shirvan/praxis/internal/drivers/eip"
	"github.com/shirvan/praxis/internal/drivers/esm"
	"github.com/shirvan/praxis/internal/drivers/iaminstanceprofile"
	"github.com/shirvan/praxis/internal/drivers/iamrole"
	"github.com/shirvan/praxis/internal/drivers/iamuser"
	"github.com/shirvan/praxis/internal/drivers/lambda"
	"github.com/shirvan/praxis/internal/drivers/lambdalayer"
	"github.com/shirvan/praxis/internal/drivers/lambdaperm"
	"github.com/shirvan/praxis/internal/drivers/nacl"
	"github.com/shirvan/praxis/internal/drivers/route53healthcheck"
	"github.com/shirvan/praxis/internal/drivers/route53record"
	"github.com/shirvan/praxis/internal/drivers/route53zone"
	"github.com/shirvan/praxis/internal/drivers/s3"
	"github.com/shirvan/praxis/internal/drivers/sg"
	"github.com/shirvan/praxis/internal/drivers/subnet"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Key Utilities
// ---------------------------------------------------------------------------

func TestJoinKey(t *testing.T) {
	assert.Equal(t, "vpc-123~web-sg", JoinKey("vpc-123", "web-sg"))
	assert.Equal(t, "us-east-1~my-func", JoinKey("us-east-1", "my-func"))
	assert.Equal(t, "single", JoinKey("single"))
}

func TestValidateKeyPart_Empty(t *testing.T) {
	err := ValidateKeyPart("bucket name", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket name is required")
}

func TestValidateKeyPart_ContainsSeparator(t *testing.T) {
	err := ValidateKeyPart("bucket name", "bad~name")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot contain")
}

func TestValidateKeyPart_Valid(t *testing.T) {
	err := ValidateKeyPart("bucket name", "my-bucket")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// S3 Adapter
// ---------------------------------------------------------------------------

func TestS3Adapter_DecodeSpecAndBuildKey(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"S3Bucket",
		"metadata":{"name":"assets-bucket"},
		"spec":{
			"region":"us-east-1",
			"versioning":true,
			"acl":"private",
			"encryption":{"enabled":true,"algorithm":"AES256"},
			"tags":{"env":"dev"}
		}
	}`)

	decoded, err := adapter.DecodeSpec(raw)
	require.NoError(t, err)
	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)

	assert.Equal(t, "assets-bucket", key)
	assert.Equal(t, KeyScopeGlobal, adapter.Scope())
	typed, ok := decoded.(s3.S3BucketSpec)
	require.True(t, ok)
	assert.Equal(t, "assets-bucket", typed.BucketName)
	assert.Equal(t, "us-east-1", typed.Region)
}

func TestS3Adapter_BuildImportKey(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "my-bucket")
	require.NoError(t, err)
	assert.Equal(t, "my-bucket", key)
}

func TestS3Adapter_Kind(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	assert.Equal(t, s3.ServiceName, adapter.Kind())
	assert.Equal(t, s3.ServiceName, adapter.ServiceName())
}

func TestS3Adapter_NormalizeOutputs(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(s3.S3BucketOutputs{
		ARN:        "arn:aws:s3:::my-bucket",
		BucketName: "my-bucket",
		Region:     "us-east-1",
		DomainName: "my-bucket.s3.amazonaws.com",
	})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:s3:::my-bucket", out["arn"])
	assert.Equal(t, "my-bucket", out["bucketName"])
	assert.Equal(t, "us-east-1", out["region"])
	assert.Equal(t, "my-bucket.s3.amazonaws.com", out["domainName"])
}

func TestS3Adapter_NormalizeOutputs_WrongType(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	_, err := adapter.NormalizeOutputs("not-an-output")
	require.Error(t, err)
}

func TestS3Adapter_DecodeSpec_InvalidJSON(t *testing.T) {
	adapter := NewS3AdapterWithAuth(nil)
	_, err := adapter.DecodeSpec(json.RawMessage(`{invalid`))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// SecurityGroup Adapter
// ---------------------------------------------------------------------------

func TestSecurityGroupAdapter_BuildKey(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SecurityGroup",
		"metadata":{"name":"web-sg","labels":{"praxis.io/region":"eu-west-1"}},
		"spec":{
			"groupName":"web-sg",
			"description":"web tier",
			"vpcId":"vpc-123",
			"ingressRules":[],
			"egressRules":[],
			"tags":{"env":"dev"}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "vpc-123~web-sg", key)
	assert.Equal(t, KeyScopeCustom, adapter.Scope())
}

func TestSecurityGroupAdapter_BuildKey_NoRegionLabel(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	raw := json.RawMessage(`{
		"apiVersion":"praxis.io/v1",
		"kind":"SecurityGroup",
		"metadata":{"name":"default-sg"},
		"spec":{
			"groupName":"default-sg",
			"description":"test",
			"vpcId":"vpc-1",
			"ingressRules":[],
			"egressRules":[],
			"tags":{}
		}
	}`)

	key, err := adapter.BuildKey(raw)
	require.NoError(t, err)
	assert.Equal(t, "vpc-1~default-sg", key)
}

func TestSecurityGroupAdapter_BuildImportKey(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	key, err := adapter.BuildImportKey("us-east-1", "sg-0abc123")
	require.NoError(t, err)
	assert.Equal(t, "sg-0abc123", key)
}

func TestSecurityGroupAdapter_Kind(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	assert.Equal(t, sg.ServiceName, adapter.Kind())
	assert.Equal(t, sg.ServiceName, adapter.ServiceName())
}

func TestSecurityGroupAdapter_NormalizeOutputs(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	out, err := adapter.NormalizeOutputs(sg.SecurityGroupOutputs{
		GroupId:  "sg-12345",
		GroupArn: "arn:aws:ec2:us-east-1:123:security-group/sg-12345",
		VpcId:    "vpc-abc",
	})
	require.NoError(t, err)
	assert.Equal(t, "sg-12345", out["groupId"])
	assert.Equal(t, "arn:aws:ec2:us-east-1:123:security-group/sg-12345", out["groupArn"])
	assert.Equal(t, "vpc-abc", out["vpcId"])
}

func TestSecurityGroupAdapter_NormalizeOutputs_WrongType(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	_, err := adapter.NormalizeOutputs(42)
	require.Error(t, err)
}

func TestSecurityGroupAdapter_DecodeSpec_InvalidJSON(t *testing.T) {
	adapter := NewSecurityGroupAdapterWithAuth(nil)
	_, err := adapter.DecodeSpec(json.RawMessage(`not-json`))
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

func TestRegistry_Get_UnsupportedKind(t *testing.T) {
	registry := NewRegistry(nil)
	_, err := registry.Get("NoSuchKind")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported resource kind")
}

func TestRegistry_Get_LambdaLayer(t *testing.T) {
	registry := NewRegistryWithAdapters(NewLambdaLayerAdapterWithAuth(nil))
	adapter, err := registry.Get(lambdalayer.ServiceName)
	require.NoError(t, err)
	assert.Equal(t, lambdalayer.ServiceName, adapter.Kind())
}

func TestRegistry_Get_LambdaPermission(t *testing.T) {
	registry := NewRegistryWithAdapters(NewLambdaPermissionAdapterWithAuth(nil))
	adapter, err := registry.Get(lambdaperm.ServiceName)
	require.NoError(t, err)
	assert.Equal(t, lambdaperm.ServiceName, adapter.Kind())
}

func TestRegistry_Get_ESM(t *testing.T) {
	registry := NewRegistryWithAdapters(NewESMAdapterWithAuth(nil))
	adapter, err := registry.Get(esm.ServiceName)
	require.NoError(t, err)
	assert.Equal(t, esm.ServiceName, adapter.Kind())
}

func TestRegistry_Get_S3(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("S3Bucket")
	require.NoError(t, err)
	assert.Equal(t, "S3Bucket", adapter.Kind())
}

func TestRegistry_Get_EC2Instance(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("EC2Instance")
	require.NoError(t, err)
	assert.Equal(t, ec2.ServiceName, adapter.Kind())
}

func TestRegistry_Get_LambdaFunction(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("LambdaFunction")
	require.NoError(t, err)
	assert.Equal(t, lambda.ServiceName, adapter.Kind())
}

func TestRegistry_Get_EBSVolume(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("EBSVolume")
	require.NoError(t, err)
	assert.Equal(t, ebs.ServiceName, adapter.Kind())
}

func TestRegistry_Get_ElasticIP(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("ElasticIP")
	require.NoError(t, err)
	assert.Equal(t, eip.ServiceName, adapter.Kind())
}

func TestRegistry_Get_IAMInstanceProfile(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("IAMInstanceProfile")
	require.NoError(t, err)
	assert.Equal(t, iaminstanceprofile.ServiceName, adapter.Kind())
}

func TestRegistry_Get_IAMRole(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("IAMRole")
	require.NoError(t, err)
	assert.Equal(t, iamrole.ServiceName, adapter.Kind())
}

func TestRegistry_Get_IAMUser(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("IAMUser")
	require.NoError(t, err)
	assert.Equal(t, iamuser.ServiceName, adapter.Kind())
}

func TestRegistry_Get_AMI(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("AMI")
	require.NoError(t, err)
	assert.Equal(t, ami.ServiceName, adapter.Kind())
}

func TestRegistry_Get_SecurityGroup(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("SecurityGroup")
	require.NoError(t, err)
	assert.Equal(t, "SecurityGroup", adapter.Kind())
}

func TestRegistry_Get_NetworkACL(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("NetworkACL")
	require.NoError(t, err)
	assert.Equal(t, nacl.ServiceName, adapter.Kind())
}

func TestRegistry_Get_Subnet(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("Subnet")
	require.NoError(t, err)
	assert.Equal(t, subnet.ServiceName, adapter.Kind())
}

func TestRegistry_Get_Route53HostedZone(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("Route53HostedZone")
	require.NoError(t, err)
	assert.Equal(t, route53zone.ServiceName, adapter.Kind())
}

func TestRegistry_Get_Route53Record(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("Route53Record")
	require.NoError(t, err)
	assert.Equal(t, route53record.ServiceName, adapter.Kind())
}

func TestRegistry_Get_Route53HealthCheck(t *testing.T) {
	registry := NewRegistry(nil)
	adapter, err := registry.Get("Route53HealthCheck")
	require.NoError(t, err)
	assert.Equal(t, route53healthcheck.ServiceName, adapter.Kind())
}

func TestRegistry_NilSafety(t *testing.T) {
	var r *Registry
	_, err := r.Get("S3Bucket")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")

	all := r.All()
	assert.Nil(t, all)
}

func TestRegistry_All_ReturnsDefensiveCopy(t *testing.T) {
	registry := NewRegistry(nil)
	all := registry.All()
	require.NotEmpty(t, all)

	// Mutating the copy should not affect the registry.
	delete(all, "S3Bucket")
	_, err := registry.Get("S3Bucket")
	require.NoError(t, err, "original registry must be unaffected")
}

func TestNewRegistryWithAdapters_SkipsNil(t *testing.T) {
	registry := NewRegistryWithAdapters(nil, NewS3AdapterWithAuth(nil), nil)
	all := registry.All()
	assert.Len(t, all, 1)
}

// ---------------------------------------------------------------------------
// Helpers: castOutput
// ---------------------------------------------------------------------------

func TestCastOutput_DirectType(t *testing.T) {
	out, err := castOutput[s3.S3BucketOutputs](s3.S3BucketOutputs{ARN: "arn:test"})
	require.NoError(t, err)
	assert.Equal(t, "arn:test", out.ARN)
}

func TestCastOutput_PointerType(t *testing.T) {
	val := s3.S3BucketOutputs{ARN: "arn:ptr"}
	out, err := castOutput[s3.S3BucketOutputs](&val)
	require.NoError(t, err)
	assert.Equal(t, "arn:ptr", out.ARN)
}

func TestCastOutput_WrongType(t *testing.T) {
	_, err := castOutput[s3.S3BucketOutputs]("string-value")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected")
}

// ---------------------------------------------------------------------------
// Helpers: createFieldDiffsFromSpec
// ---------------------------------------------------------------------------

func TestCreateFieldDiffsFromSpec_NilSpec(t *testing.T) {
	diffs, err := createFieldDiffsFromSpec(nil)
	require.NoError(t, err)
	assert.Nil(t, diffs)
}

func TestCreateFieldDiffsFromSpec_FlatStruct(t *testing.T) {
	spec := map[string]any{
		"name":   "test",
		"region": "us-east-1",
	}
	diffs, err := createFieldDiffsFromSpec(spec)
	require.NoError(t, err)
	assert.NotEmpty(t, diffs)
	// Should have one diff per field
	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.name"])
	assert.True(t, paths["spec.region"])
}

func TestCreateFieldDiffsFromSpec_NestedStruct(t *testing.T) {
	spec := map[string]any{
		"encryption": map[string]any{
			"enabled":   true,
			"algorithm": "AES256",
		},
	}
	diffs, err := createFieldDiffsFromSpec(spec)
	require.NoError(t, err)
	paths := make(map[string]bool)
	for _, d := range diffs {
		paths[d.Path] = true
	}
	assert.True(t, paths["spec.encryption.enabled"])
	assert.True(t, paths["spec.encryption.algorithm"])
}
