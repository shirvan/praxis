// Package awsclient provides factory functions for creating AWS SDK clients.
// When cfg.BaseEndpoint is set (dev/test), clients hit LocalStack.
// When it's empty (production), clients use AWS's default endpoint resolution.
package awsclient

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewS3Client returns an S3 client from the given config.
//
// We enable path-style addressing when BaseEndpoint is set because LocalStack
// requires it — virtual-hosted-style (bucket.s3.localhost) doesn't resolve
// in Docker networks. In production with real AWS, the default
// (virtual-hosted-style) is used, which is AWS's preference.
func NewS3Client(cfg aws.Config) *s3.Client {
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if cfg.BaseEndpoint != nil {
			o.UsePathStyle = true
		}
	})
}

// NewEC2Client returns an EC2 client from the given config.
func NewEC2Client(cfg aws.Config) *ec2.Client {
	return ec2.NewFromConfig(cfg)
}
