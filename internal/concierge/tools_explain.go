package concierge

import (
	"encoding/json"
	"fmt"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

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

func toolExplainError(_ restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Provide known error code explanations. The LLM interprets the response.
	explanations := map[string]string{
		"TEMPLATE_EVAL_FAILED":   "The CUE template failed to evaluate. Common causes: syntax errors, undefined variables, type mismatches. Check the template source and variable values.",
		"DEPENDENCY_CYCLE":       "The resource dependency graph contains a cycle. Review resource references (${resources.X.outputs.Y}) for circular dependencies.",
		"RESOURCE_NOT_FOUND":     "A referenced resource does not exist. This can happen when a deployment references a resource that was deleted or never created.",
		"POLICY_VIOLATION":       "The deployment violates one or more policies. Review the policy errors for details on which rules were violated and how to fix them.",
		"AUTH_FAILED":            "AWS credential resolution failed. Check that the account is configured and credentials are valid.",
		"DRIVER_ERROR":           "A resource driver returned an error during provisioning. Check the resource-level error for AWS API details.",
		"WORKSPACE_NOT_FOUND":    "The specified workspace does not exist. Create it with 'praxis workspace create'.",
		"TEMPLATE_NOT_FOUND":     "The specified template is not registered. Register it first or use 'praxis apply' with inline CUE.",
		"DEPLOYMENT_NOT_FOUND":   "The specified deployment does not exist. Check the deployment key.",
		"IMPORT_ALREADY_MANAGED": "The resource is already managed by Praxis. Use 'praxis apply' to update it.",
	}

	// Check for known error codes.
	for code, explanation := range explanations {
		if args.Error == code {
			return fmt.Sprintf("Error code: %s\n\n%s", code, explanation), nil
		}
	}

	// Return the raw error for the LLM to interpret.
	return fmt.Sprintf("Error message: %s\n\nThis is not a known Praxis error code. The error may be from an AWS API call or a runtime issue. Check the deployment events for more context.", args.Error), nil
}

func toolExplainResource(_ restate.Context, argsJSON string, _ SessionState) (string, error) {
	var args struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	// Provide known resource kind descriptions.
	descriptions := map[string]string{
		"S3Bucket":          "An AWS S3 bucket for object storage. Spec fields: bucketName (required), region (required), versioning (bool), encryption {enabled, algorithm}, tags (map), forceDestroy (bool).",
		"SecurityGroup":     "An AWS VPC Security Group. Spec fields: groupName (required), vpcId (required), description, ingressRules [{protocol, fromPort, toPort, cidrBlocks}], egressRules [{protocol, fromPort, toPort, cidrBlocks}], tags.",
		"VPC":               "An AWS Virtual Private Cloud. Spec fields: cidrBlock (required), region (required), enableDnsSupport (bool), enableDnsHostnames (bool), tags.",
		"Subnet":            "An AWS VPC Subnet. Spec fields: vpcId (required), cidrBlock (required), availabilityZone (required), mapPublicIpOnLaunch (bool), tags.",
		"EC2Instance":       "An AWS EC2 instance. Spec fields: amiId (required), instanceType (required), subnetId, securityGroupIds, keyName, userData, tags.",
		"IAMRole":           "An AWS IAM Role. Spec fields: roleName (required), assumeRolePolicy (required, JSON), description, policies [{arn}], inlinePolicies [{name, document}], tags.",
		"IAMPolicy":         "An AWS IAM Policy. Spec fields: policyName (required), policyDocument (required, JSON), description, path, tags.",
		"RDSInstance":       "An AWS RDS database instance. Spec fields: identifier (required), engine (required), engineVersion, instanceClass (required), allocatedStorage, masterUsername, masterPassword, subnetGroupName, securityGroupIds, tags.",
		"LambdaFunction":    "An AWS Lambda function. Spec fields: functionName (required), runtime (required), handler (required), role (required), code {s3Bucket, s3Key} or {zipFile}, memorySize, timeout, environment, tags.",
		"Route53HostedZone": "An AWS Route 53 hosted zone. Spec fields: name (required), comment, privateZone (bool), vpcId (for private zones), tags.",
	}

	if desc, ok := descriptions[args.Kind]; ok {
		return desc, nil
	}

	return fmt.Sprintf("Resource kind %q is recognized by Praxis but detailed spec documentation is not available in this tool. Use 'describeTemplate' on a template that uses this kind to see its schema.", args.Kind), nil
}

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
	summary := fmt.Sprintf("Deployment %q failed.\nError: %s\nError Code: %s\n", args.DeploymentKey, detail.Error, detail.ErrorCode)

	if len(detail.ResourceErrors) > 0 {
		summary += "\nResource errors:\n"
		for name, errMsg := range detail.ResourceErrors {
			summary += fmt.Sprintf("  - %s: %s\n", name, errMsg)
		}
	}

	failedResources := 0
	for _, r := range detail.Resources {
		if r.Status == types.DeploymentResourceError {
			failedResources++
			summary += fmt.Sprintf("\nFailed resource: %s (%s)\n  Key: %s\n  Error: %s\n", r.Name, r.Kind, r.Key, r.Error)
		}
	}

	if failedResources == 0 && detail.Error == "" {
		summary += "\nNo specific resource errors found. The failure may be in template evaluation or dependency resolution."
	}

	return summary, nil
}
