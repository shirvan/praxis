#!/bin/bash
# moto-init/setup.sh
# Seeds test parameters and resources in Moto (AWS mock server).
#
# This is the single source of truth for Moto seed data. docker-compose
# mounts this script and runs it; standalone / CI callers invoke it directly
# with AWS_ENDPOINT_URL pointing at the Moto server.

set -euo pipefail

ENDPOINT="${AWS_ENDPOINT_URL:-http://localhost:4566}"

echo "=== Praxis Moto init ==="

# ── SSM parameters ──────────────────────────────────────────
aws ssm put-parameter \
    --endpoint-url "$ENDPOINT" \
    --name "/praxis/dev/db-password" \
    --value "test-password-dev" \
    --type "SecureString" \
    --overwrite

aws ssm put-parameter \
    --endpoint-url "$ENDPOINT" \
    --name "/praxis/prod/db-password" \
    --value "test-password-prod" \
    --type "SecureString" \
    --overwrite

aws ssm put-parameter \
    --endpoint-url "$ENDPOINT" \
    --name "/praxis/prod/aurora-password" \
    --value "test-aurora-password-prod" \
    --type "SecureString" \
    --overwrite

echo "=== Praxis Moto SSM parameters ready ==="

# ── Default VPC ─────────────────────────────────────────────
# Moto auto-creates one per region, but we verify explicitly.
aws ec2 describe-vpcs \
    --endpoint-url "$ENDPOINT" \
    --filters Name=isDefault,Values=true --query 'Vpcs[0].VpcId' --output text || \
    aws ec2 create-default-vpc --endpoint-url "$ENDPOINT"

echo "=== Praxis Moto default VPC ready ==="

# ── Shared-services VPC ─────────────────────────────────────
# saas-platform.cue references this via a data.sharedVpc data source
# (filter: tag Name=shared-services). Idempotent.
EXISTING=$(aws ec2 describe-vpcs \
    --endpoint-url "$ENDPOINT" \
    --filters Name=tag:Name,Values=shared-services \
    --query 'Vpcs[0].VpcId' --output text 2>/dev/null || echo "None")
if [ "$EXISTING" = "None" ] || [ -z "$EXISTING" ]; then
    SHARED_VPC_ID=$(aws ec2 create-vpc \
        --endpoint-url "$ENDPOINT" \
        --cidr-block 10.100.0.0/16 \
        --query 'Vpc.VpcId' --output text)
    aws ec2 create-tags \
        --endpoint-url "$ENDPOINT" \
        --resources "$SHARED_VPC_ID" \
        --tags Key=Name,Value=shared-services Key=env,Value=shared
    echo "=== Praxis Moto shared-services VPC ($SHARED_VPC_ID) created ==="
else
    echo "=== Praxis Moto shared-services VPC ($EXISTING) already exists ==="
fi

# ── Seed AMI for CopyImage ──────────────────────────────────
# Moto requires the source AMI to exist before CopyImage can reference it.
# Register a root-device AMI directly — no throwaway instance needed.
SEED_AMI=$(aws ec2 register-image \
    --endpoint-url "$ENDPOINT" \
    --name "seed-source-ami" \
    --root-device-name /dev/xvda \
    --architecture x86_64 \
    --query 'ImageId' --output text)
aws ssm put-parameter \
    --endpoint-url "$ENDPOINT" \
    --name "/praxis/moto/base-ami" \
    --value "$SEED_AMI" \
    --type "String" \
    --overwrite
echo "=== Praxis Moto seed AMI ($SEED_AMI) ready ==="

echo "=== Praxis Moto init complete ==="
