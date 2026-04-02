package concierge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLookupPraxisKindTerraform(t *testing.T) {
	kind, ok := LookupPraxisKind("aws_s3_bucket")
	assert.True(t, ok)
	assert.Equal(t, "S3Bucket", kind)
}

func TestLookupPraxisKindCloudFormation(t *testing.T) {
	kind, ok := LookupPraxisKind("AWS::EC2::VPC")
	assert.True(t, ok)
	assert.Equal(t, "VPC", kind)
}

func TestLookupPraxisKindCrossplane(t *testing.T) {
	kind, ok := LookupPraxisKind("Bucket")
	assert.True(t, ok)
	assert.Equal(t, "S3Bucket", kind)
}

func TestLookupPraxisKindUnknown(t *testing.T) {
	_, ok := LookupPraxisKind("not_a_real_resource")
	assert.False(t, ok)
}

func TestFormatMappingTable(t *testing.T) {
	table := FormatMappingTable()
	assert.Contains(t, table, "Source Type")
	assert.Contains(t, table, "S3Bucket")
	assert.Contains(t, table, "aws_s3_bucket")
}
