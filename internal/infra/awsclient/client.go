// Package awsclient provides factory functions for creating AWS SDK clients.
// When cfg.BaseEndpoint is set (dev/test), clients hit LocalStack.
// When it's empty (production), clients use AWS's default endpoint resolution.
package awsclient

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
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

// NewECRClient returns an ECR client from the given config.
func NewECRClient(cfg aws.Config) *ecr.Client {
	return ecr.NewFromConfig(cfg)
}

// NewELBv2Client returns an ELBv2 client from the given config.
func NewELBv2Client(cfg aws.Config) *elasticloadbalancingv2.Client {
	return elasticloadbalancingv2.NewFromConfig(cfg)
}

// NewIAMClient returns an IAM client from the given config.
func NewIAMClient(cfg aws.Config) *iam.Client {
	return iam.NewFromConfig(cfg)
}

// NewLambdaClient returns a Lambda client from the given config.
func NewLambdaClient(cfg aws.Config) *lambdasdk.Client {
	return lambdasdk.NewFromConfig(cfg)
}

// NewRoute53Client returns a Route53 client from the given config.
func NewRoute53Client(cfg aws.Config) *route53.Client {
	return route53.NewFromConfig(cfg)
}

// NewRDSClient returns an RDS client from the given config.
func NewRDSClient(cfg aws.Config) *rds.Client {
	return rds.NewFromConfig(cfg)
}

// NewACMClient returns an ACM client from the given config.
func NewACMClient(cfg aws.Config) *acm.Client {
	return acm.NewFromConfig(cfg)
}

// NewCloudWatchClient returns a CloudWatch client from the given config.
func NewCloudWatchClient(cfg aws.Config) *cloudwatch.Client {
	return cloudwatch.NewFromConfig(cfg)
}

// NewCloudWatchLogsClient returns a CloudWatch Logs client from the given config.
func NewCloudWatchLogsClient(cfg aws.Config) *cloudwatchlogs.Client {
	return cloudwatchlogs.NewFromConfig(cfg)
}

// NewSNSClient returns an SNS client from the given config.
func NewSNSClient(cfg aws.Config) *sns.Client {
	return sns.NewFromConfig(cfg)
}

// NewSQSClient returns an SQS client from the given config.
func NewSQSClient(cfg aws.Config) *sqs.Client {
	return sqs.NewFromConfig(cfg)
}
