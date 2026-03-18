package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveResourceKey_GlobalScope(t *testing.T) {
	flags := &rootFlags{region: "us-east-1"}
	// S3Bucket is global — key is passed through unchanged.
	key := flags.resolveResourceKey("S3Bucket", "my-bucket")
	assert.Equal(t, "my-bucket", key)
}

func TestResolveResourceKey_CustomScope(t *testing.T) {
	flags := &rootFlags{region: "us-east-1"}
	// SecurityGroup is custom — key is passed through unchanged.
	key := flags.resolveResourceKey("SecurityGroup", "vpc-123~web-sg")
	assert.Equal(t, "vpc-123~web-sg", key)
}

func TestResolveResourceKey_RegionScope_PrependRegion(t *testing.T) {
	flags := &rootFlags{region: "us-west-2"}
	key := flags.resolveResourceKey("EC2Instance", "my-function")
	assert.Equal(t, "us-west-2~my-function", key)
}

func TestResolveResourceKey_RegionScope_NoRegionSet(t *testing.T) {
	flags := &rootFlags{region: ""}
	// No region configured — key returned as-is.
	key := flags.resolveResourceKey("Lambda", "my-function")
	assert.Equal(t, "my-function", key)
}

func TestResolveResourceKey_RegionScope_AlreadyQualified(t *testing.T) {
	flags := &rootFlags{region: "us-east-1"}
	// If user already included ~ in the key, don't prepend region.
	key := flags.resolveResourceKey("Lambda", "us-west-2~my-function")
	assert.Equal(t, "us-west-2~my-function", key)
}
