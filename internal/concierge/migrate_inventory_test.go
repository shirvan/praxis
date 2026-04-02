package concierge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildInventoryTerraform(t *testing.T) {
	source := `
resource "aws_s3_bucket" "mybucket" {
  bucket = "test"
}

resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource "aws_s3_bucket" "another" {
  bucket = "test2"
}
`
	inv := BuildInventory("terraform", source)

	assert.Equal(t, "terraform", inv.Format)
	assert.Equal(t, 3, inv.TotalResources)
	assert.Contains(t, inv.ResourceTypes, "aws_s3_bucket")
	assert.Contains(t, inv.ResourceTypes, "aws_vpc")
	assert.Equal(t, "S3Bucket", inv.MappedKinds["aws_s3_bucket"])
	assert.Equal(t, "VPC", inv.MappedKinds["aws_vpc"])
	assert.Empty(t, inv.UnmappedTypes)
}

func TestBuildInventoryCloudFormationYAML(t *testing.T) {
	source := `
Resources:
  MyBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: test
  MyVPC:
    Type: AWS::EC2::VPC
    Properties:
      CidrBlock: 10.0.0.0/16
`
	inv := BuildInventory("cloudformation", source)

	assert.Equal(t, 2, inv.TotalResources)
	assert.Contains(t, inv.MappedKinds, "AWS::S3::Bucket")
	assert.Contains(t, inv.MappedKinds, "AWS::EC2::VPC")
}

func TestBuildInventoryCloudFormationJSON(t *testing.T) {
	source := `{
  "Resources": {
    "MyBucket": {
      "Type": "AWS::S3::Bucket",
      "Properties": {
        "BucketName": "test"
      }
    }
  }
}`
	inv := BuildInventory("cloudformation", source)

	assert.Equal(t, 1, inv.TotalResources)
	assert.Equal(t, "S3Bucket", inv.MappedKinds["AWS::S3::Bucket"])
}

func TestBuildInventoryCrossplane(t *testing.T) {
	source := `
apiVersion: s3.aws.upbound.io/v1beta1
kind: Bucket
metadata:
  name: test
`
	inv := BuildInventory("crossplane", source)

	assert.Equal(t, 1, inv.TotalResources)
	assert.Contains(t, inv.MappedKinds, "Bucket")
}

func TestBuildInventoryUnmappedTypes(t *testing.T) {
	source := `
resource "aws_unknown_thing" "foo" {
  name = "bar"
}
`
	inv := BuildInventory("terraform", source)

	assert.Equal(t, 1, inv.TotalResources)
	assert.Contains(t, inv.UnmappedTypes, "aws_unknown_thing")
}

func TestDetectFormatTerraform(t *testing.T) {
	source := `resource "aws_s3_bucket" "mybucket" { bucket = "test" }`
	assert.Equal(t, "terraform", DetectFormat(source))
}

func TestDetectFormatCloudFormationYAML(t *testing.T) {
	source := `
Resources:
  MyBucket:
    Type: AWS::S3::Bucket
`
	assert.Equal(t, "cloudformation", DetectFormat(source))
}

func TestDetectFormatCrossplane(t *testing.T) {
	source := `
apiVersion: s3.aws.upbound.io/v1beta1
kind: Bucket
metadata:
  name: test
`
	assert.Equal(t, "crossplane", DetectFormat(source))
}

func TestDetectFormatUnknown(t *testing.T) {
	assert.Equal(t, "unknown", DetectFormat("just some random text"))
}
