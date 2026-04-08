package concierge

import (
	"encoding/json"
	"fmt"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// registerExplainTools adds tools that help the LLM explain Praxis concepts,
// errors, and resource types. These are read-only and do not require approval.
// They provide structured knowledge that the LLM combines with its own reasoning
// to produce helpful explanations for the user.
func (r *ToolRegistry) registerExplainTools() {
	r.Register(&ToolDef{
		Name:        "explainError",
		Description: "Given an error code or message, explain what it means and how to fix it",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error": map[string]any{"type": "string", "description": "The error code or message to explain"},
			},
			"required": []string{"error"},
		},
		Execute: toolExplainError,
	})

	r.Register(&ToolDef{
		Name:        "explainResource",
		Description: "Explain what a Praxis resource kind does and its available spec fields",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{"type": "string", "description": "Resource kind (e.g. S3Bucket, SecurityGroup)"},
			},
			"required": []string{"kind"},
		},
		Execute: toolExplainResource,
	})

	r.Register(&ToolDef{
		Name:        "suggestFix",
		Description: "Given a failed deployment, analyze the errors and suggest remediation",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deploymentKey": map[string]any{"type": "string", "description": "The failed deployment to analyze"},
			},
			"required": []string{"deploymentKey"},
		},
		Execute: toolSuggestFix,
	})
}

// toolExplainError provides known error code explanations. For recognized Praxis
// error codes, it returns a structured explanation. For unknown errors, it returns
// the raw message and lets the LLM interpret it using its general knowledge.
func toolExplainError(_ restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Provide known error code explanations. These match the ErrorCode constants
	// defined in pkg/types/errorcode.go. The LLM interprets the response.
	explanations := map[string]string{
		"VALIDATION_ERROR":  "Schema or input validation failure. CUE evaluation detected a constraint violation, a required field is missing, or a field value is outside the allowed range. Check the template source and variable values against the resource schema.",
		"NOT_FOUND":         "The requested resource, deployment, or template does not exist. Verify the identifier (deployment key, template name, or resource key) is correct and the object has been created.",
		"CONFLICT":          "A naming or ownership collision occurred. For example, a deployment key is already in use, an S3 bucket name is already taken, or a resource is already managed by Praxis.",
		"CAPACITY_EXCEEDED": "A system limit has been reached, such as too many concurrent deployments or resources per template. Reduce parallelism or clean up unused deployments.",
		"TEMPLATE_INVALID":  "The CUE template source could not be parsed or unified against the provider schemas. Common causes: syntax errors, undefined variables, type mismatches. Check the template source with 'praxis template validate'.",
		"GRAPH_INVALID":     "The resource dependency graph contains cycles, missing references, or other structural errors that prevent safe orchestration. Review resource references (${resources.X.outputs.Y}) for circular dependencies or references to non-existent resources.",
		"PROVISION_FAILED":  "One or more resources failed during the provisioning phase. Check the deployment detail for per-resource errors — these typically contain the underlying AWS API error message.",
		"DELETE_FAILED":     "One or more resources failed during the deletion phase. A resource may have a preventDestroy lifecycle policy, or the AWS API returned an error. Check per-resource errors in the deployment detail.",
		"INTERNAL_ERROR":    "An unexpected system error occurred. This may indicate a bug, an infrastructure failure, or a transient issue. Check the Praxis server logs for more details.",
	}

	// Check for known error codes (exact match and substring match).
	for code, explanation := range explanations {
		if args.Error == code || strings.Contains(args.Error, code) {
			return fmt.Sprintf("Error code: %s\n\n%s", code, explanation), nil
		}
	}

	// Return the raw error for the LLM to interpret.
	return fmt.Sprintf("Error message: %s\n\nThis is not a known Praxis error code. The error may be from an AWS API call or a runtime issue. Check the deployment events for more context.", args.Error), nil
}

// toolExplainResource returns a structured description of a Praxis resource kind,
// including its purpose and spec fields. This helps the LLM guide users in
// constructing valid CUE templates.
func toolExplainResource(_ restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Provide known resource kind descriptions. These match the ServiceName()
	// constants in internal/drivers/*/service.go.
	descriptions := map[string]string{
		// Compute
		"EC2Instance": "An AWS EC2 instance. Spec fields: amiId (required), instanceType (required), subnetId, securityGroupIds, keyName, userData, tags.",
		"AMI":         "An Amazon Machine Image. Spec fields: sourceInstanceId or sourceSnapshotId (required), name (required), description, tags.",
		"EBSVolume":   "An Elastic Block Store volume. Spec fields: availabilityZone (required), size (required, GiB), volumeType, iops, encrypted (bool), kmsKeyId, tags.",
		"ElasticIP":   "An Elastic IP address. Spec fields: domain (vpc), tags. Outputs: allocationId, publicIp.",
		"KeyPair":     "An EC2 key pair. Spec fields: keyName (required), publicKeyMaterial (required), tags.",

		// Networking
		"VPC":                  "An AWS Virtual Private Cloud. Spec fields: cidrBlock (required), region (required), enableDnsSupport (bool), enableDnsHostnames (bool), tags.",
		"Subnet":               "An AWS VPC Subnet. Spec fields: vpcId (required), cidrBlock (required), availabilityZone (required), mapPublicIpOnLaunch (bool), tags.",
		"SecurityGroup":        "An AWS VPC Security Group. Spec fields: groupName (required), vpcId (required), description, ingressRules [{protocol, fromPort, toPort, cidrBlocks}], egressRules [{protocol, fromPort, toPort, cidrBlocks}], tags.",
		"InternetGateway":      "A VPC Internet Gateway. Spec fields: vpcId (required), tags. Outputs: internetGatewayId.",
		"NATGateway":           "A NAT Gateway for private subnet internet access. Spec fields: subnetId (required), allocationId (required — an ElasticIP), tags.",
		"RouteTable":           "A VPC Route Table. Spec fields: vpcId (required), routes [{destinationCidrBlock, gatewayId or natGatewayId}], associations [{subnetId}], tags.",
		"NetworkACL":           "A VPC Network ACL. Spec fields: vpcId (required), ingressRules [{ruleNumber, protocol, ruleAction, cidrBlock, fromPort, toPort}], egressRules (same), tags.",
		"VPCPeeringConnection": "A VPC Peering Connection. Spec fields: vpcId (required), peerVpcId (required), peerOwnerId, peerRegion, tags.",

		// Load Balancing
		"ALB":          "An Application Load Balancer. Spec fields: name (required), subnets (required), securityGroups, scheme (internet-facing or internal), tags.",
		"NLB":          "A Network Load Balancer. Spec fields: name (required), subnets (required), scheme, tags.",
		"TargetGroup":  "An ELB Target Group. Spec fields: name (required), port (required), protocol (required), vpcId (required), healthCheck {path, protocol, port}, targetType, tags.",
		"Listener":     "An ELB Listener. Spec fields: loadBalancerArn (required), port (required), protocol (required), defaultActions [{type, targetGroupArn}], certificateArn.",
		"ListenerRule": "An ELB Listener Rule. Spec fields: listenerArn (required), priority (required), conditions [{field, values}], actions [{type, targetGroupArn}].",

		// Storage
		"S3Bucket": "An AWS S3 bucket for object storage. Spec fields: bucketName (required), region (required), versioning (bool), encryption {enabled, algorithm}, tags (map), forceDestroy (bool).",

		// Database
		"RDSInstance":      "An AWS RDS database instance. Spec fields: identifier (required), engine (required), engineVersion, instanceClass (required), allocatedStorage, masterUsername, masterPassword, subnetGroupName, securityGroupIds, tags.",
		"AuroraCluster":    "An Aurora RDS cluster. Spec fields: clusterIdentifier (required), engine (required), engineVersion, masterUsername, masterPassword, dbSubnetGroupName, vpcSecurityGroupIds, tags.",
		"DBSubnetGroup":    "An RDS DB Subnet Group. Spec fields: name (required), description, subnetIds (required), tags.",
		"DBParameterGroup": "An RDS DB Parameter Group. Spec fields: name (required), family (required), description, parameters [{name, value}], tags.",

		// IAM
		"IAMRole":            "An AWS IAM Role. Spec fields: roleName (required), assumeRolePolicy (required, JSON), description, policies [{arn}], inlinePolicies [{name, document}], tags.",
		"IAMPolicy":          "An AWS IAM Policy. Spec fields: policyName (required), policyDocument (required, JSON), description, path, tags.",
		"IAMUser":            "An AWS IAM User. Spec fields: userName (required), path, groups, policies [{arn}], tags.",
		"IAMGroup":           "An AWS IAM Group. Spec fields: groupName (required), path, policies [{arn}].",
		"IAMInstanceProfile": "An AWS IAM Instance Profile. Spec fields: instanceProfileName (required), roleName (required), path, tags.",

		// Serverless
		"LambdaFunction":     "An AWS Lambda function. Spec fields: functionName (required), runtime (required), handler (required), role (required), code {s3Bucket, s3Key} or {zipFile}, memorySize, timeout, environment, tags.",
		"LambdaLayer":        "A Lambda layer version. Spec fields: layerName (required), compatibleRuntimes, content {s3Bucket, s3Key}, description.",
		"LambdaPermission":   "A Lambda resource-based policy permission. Spec fields: functionName (required), action (required), principal (required), sourceArn, statementId.",
		"EventSourceMapping": "A Lambda event source mapping. Spec fields: functionName (required), eventSourceArn (required), batchSize, startingPosition, enabled (bool).",

		// DNS
		"Route53HostedZone":  "An AWS Route 53 hosted zone. Spec fields: name (required), comment, privateZone (bool), vpcId (for private zones), tags.",
		"Route53Record":      "A Route 53 DNS record. Spec fields: hostedZoneId (required), name (required), type (required — A, AAAA, CNAME, etc.), ttl, records, aliasTarget {hostedZoneId, dnsName}.",
		"Route53HealthCheck": "A Route 53 health check. Spec fields: type (required — HTTP, HTTPS, TCP), fqdn or ipAddress, port, resourcePath, failureThreshold, requestInterval.",

		// Containers
		"ECRRepository":      "An Elastic Container Registry repository. Spec fields: repositoryName (required), imageTagMutability, imageScanningConfiguration {scanOnPush}, tags.",
		"ECRLifecyclePolicy": "An ECR lifecycle policy for image cleanup. Spec fields: repositoryName (required), policyText (required, JSON).",

		// Messaging
		"SNSTopic":        "An SNS topic. Spec fields: topicName (required), displayName, tags. Outputs: topicArn.",
		"SNSSubscription": "An SNS subscription. Spec fields: topicArn (required), protocol (required — email, sqs, lambda, etc.), endpoint (required).",
		"SQSQueue":        "An SQS queue. Spec fields: queueName (required), delaySeconds, visibilityTimeout, messageRetentionPeriod, fifoQueue (bool), tags.",
		"SQSQueuePolicy":  "An SQS queue access policy. Spec fields: queueUrl (required), policy (required, JSON).",

		// Monitoring
		"LogGroup":    "A CloudWatch log group. Spec fields: logGroupName (required), retentionInDays, tags.",
		"MetricAlarm": "A CloudWatch metric alarm. Spec fields: alarmName (required), metricName (required), namespace (required), statistic, period, threshold, comparisonOperator, evaluationPeriods, alarmActions, tags.",
		"Dashboard":   "A CloudWatch dashboard. Spec fields: dashboardName (required), dashboardBody (required, JSON).",

		// Security
		"ACMCertificate": "An AWS Certificate Manager TLS certificate. Spec fields: domainName (required), validationMethod (DNS or EMAIL), subjectAlternativeNames, tags.",
	}

	if desc, ok := descriptions[args.Kind]; ok {
		return desc, nil
	}

	return fmt.Sprintf("Resource kind %q is recognized by Praxis but detailed spec documentation is not available in this tool. Use 'describeTemplate' on a template that uses this kind to see its schema.", args.Kind), nil
}

// toolSuggestFix analyzes a failed deployment by fetching its details and building
// a structured error summary. The LLM uses this summary to suggest remediation steps.
func toolSuggestFix(ctx restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		DeploymentKey string `json:"deploymentKey"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if args.DeploymentKey == "" {
		return "Error: deploymentKey is required", nil
	}

	detail, err := restate.Object[*types.DeploymentDetail](
		ctx, "DeploymentStateObj", args.DeploymentKey, "GetDetail",
	).Request(restate.Void{})
	if err != nil {
		return fmt.Sprintf("Error fetching deployment: %s", err.Error()), nil
	}

	if detail.Status != types.DeploymentFailed {
		return fmt.Sprintf("Deployment %q is in %s state, not Failed. No fix needed.", args.DeploymentKey, detail.Status), nil
	}

	// Build a structured error summary for the LLM to suggest fixes.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Deployment %q failed.\nError: %s\nError Code: %s\n", args.DeploymentKey, detail.Error, detail.ErrorCode)

	if len(detail.ResourceErrors) > 0 {
		sb.WriteString("\nResource errors:\n")
		for name, errMsg := range detail.ResourceErrors {
			fmt.Fprintf(&sb, "  - %s: %s\n", name, errMsg)
		}
	}

	failedResources := 0
	for _, r := range detail.Resources {
		if r.Status == types.DeploymentResourceError {
			failedResources++
			fmt.Fprintf(&sb, "\nFailed resource: %s (%s)\n  Key: %s\n  Error: %s\n", r.Name, r.Kind, r.Key, r.Error)
		}
	}

	if failedResources == 0 && detail.Error == "" {
		sb.WriteString("\nNo specific resource errors found. The failure may be in template evaluation or dependency resolution.")
	}

	return sb.String(), nil
}
