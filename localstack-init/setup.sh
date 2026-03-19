#!/bin/bash
# localstack-init/setup.sh
# Runs when LocalStack container starts (mounted to /etc/localstack/init/ready.d/).
# Seeds test parameters and resources.

set -euo pipefail

echo "=== Praxis LocalStack init ==="

# Create SSM parameters for the SSM secret resolver.
awslocal ssm put-parameter \
    --name "/praxis/dev/db-password" \
    --value "test-password-dev" \
    --type "SecureString" \
    --overwrite

awslocal ssm put-parameter \
    --name "/praxis/prod/db-password" \
    --value "test-password-prod" \
    --type "SecureString" \
    --overwrite

echo "=== Praxis LocalStack init complete ==="

# Ensure a default VPC exists for EC2 security group tests.
# LocalStack auto-creates one, but we verify explicitly.
awslocal ec2 describe-vpcs --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text || \
    awslocal ec2 create-default-vpc

echo "=== Praxis LocalStack VPC ready ==="
