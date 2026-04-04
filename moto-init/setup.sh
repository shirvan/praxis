#!/bin/bash
# moto-init/setup.sh
# Seeds test parameters and resources in Moto (AWS mock server).

set -euo pipefail

echo "=== Praxis Moto init ==="

# Create SSM parameters for the SSM secret resolver.
aws ssm put-parameter \
    --endpoint-url "${AWS_ENDPOINT_URL:-http://localhost:4566}" \
    --name "/praxis/dev/db-password" \
    --value "test-password-dev" \
    --type "SecureString" \
    --overwrite

aws ssm put-parameter \
    --endpoint-url "${AWS_ENDPOINT_URL:-http://localhost:4566}" \
    --name "/praxis/prod/db-password" \
    --value "test-password-prod" \
    --type "SecureString" \
    --overwrite

echo "=== Praxis Moto init complete ==="

# Ensure a default VPC exists for EC2 security group tests.
# Moto auto-creates one per region, but we verify explicitly.
aws ec2 describe-vpcs \
    --endpoint-url "${AWS_ENDPOINT_URL:-http://localhost:4566}" \
    --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text || \
    aws ec2 create-default-vpc --endpoint-url "${AWS_ENDPOINT_URL:-http://localhost:4566}"

echo "=== Praxis Moto VPC ready ==="
