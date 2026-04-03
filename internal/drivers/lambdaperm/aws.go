package lambdaperm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// PermissionAPI abstracts Lambda permission (resource policy) operations for testability.
type PermissionAPI interface {
	// AddPermission adds a statement to the function's resource-based policy.
	AddPermission(ctx context.Context, spec LambdaPermissionSpec) (string, error)
	// RemovePermission removes a statement by ID from the function's policy.
	RemovePermission(ctx context.Context, functionName, statementID string) error
	// GetPolicy returns the raw JSON policy for the function.
	GetPolicy(ctx context.Context, functionName string) (string, error)
	// GetPermission finds and parses a specific statement from the function's policy.
	GetPermission(ctx context.Context, functionName, statementID string) (ObservedState, error)
}

// realPermissionAPI is the production implementation backed by the Lambda SDK client.
type realPermissionAPI struct {
	client  *lambdasdk.Client
	limiter *ratelimit.Limiter
}

// NewPermissionAPI creates a production PermissionAPI with rate limiting (20 tokens/s, burst 10).
func NewPermissionAPI(client *lambdasdk.Client) PermissionAPI {
	return &realPermissionAPI{client: client, limiter: ratelimit.New("lambda-permission", 20, 10)}
}

// AddPermission calls Lambda AddPermission to add a policy statement.
func (r *realPermissionAPI) AddPermission(ctx context.Context, spec LambdaPermissionSpec) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &lambdasdk.AddPermissionInput{
		FunctionName: aws.String(spec.FunctionName),
		StatementId:  aws.String(spec.StatementId),
		Action:       aws.String(spec.Action),
		Principal:    aws.String(spec.Principal),
	}
	if spec.SourceArn != "" {
		input.SourceArn = aws.String(spec.SourceArn)
	}
	if spec.SourceAccount != "" {
		input.SourceAccount = aws.String(spec.SourceAccount)
	}
	if spec.EventSourceToken != "" {
		input.EventSourceToken = aws.String(spec.EventSourceToken)
	}
	if spec.Qualifier != "" {
		input.Qualifier = aws.String(spec.Qualifier)
	}
	out, err := r.client.AddPermission(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Statement), nil
}

// RemovePermission removes a policy statement by ID.
func (r *realPermissionAPI) RemovePermission(ctx context.Context, functionName, statementID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemovePermission(ctx, &lambdasdk.RemovePermissionInput{FunctionName: aws.String(functionName), StatementId: aws.String(statementID)})
	return err
}

// GetPolicy returns the raw JSON resource-based policy for the function.
func (r *realPermissionAPI) GetPolicy(ctx context.Context, functionName string) (string, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	out, err := r.client.GetPolicy(ctx, &lambdasdk.GetPolicyInput{FunctionName: aws.String(functionName)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.Policy), nil
}

// GetPermission finds a specific statement in the function's policy and parses it to ObservedState.
func (r *realPermissionAPI) GetPermission(ctx context.Context, functionName, statementID string) (ObservedState, error) {
	policyJSON, err := r.GetPolicy(ctx, functionName)
	if err != nil {
		return ObservedState{}, err
	}
	statement, err := permissionStatementFromPolicy(policyJSON, statementID)
	if err != nil {
		return ObservedState{}, err
	}
	return observedFromStatement(functionName, statement), nil
}

type policyStatement struct {
	Sid       string `json:"Sid"`
	Principal any    `json:"Principal"`
	Action    any    `json:"Action"`
	Condition any    `json:"Condition,omitempty"`
}

// permissionStatementFromPolicy finds a statement by Sid in the parsed policy JSON.
func permissionStatementFromPolicy(policyJSON string, statementID string) (policyStatement, error) {
	var policy struct {
		Statement []policyStatement `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		return policyStatement{}, fmt.Errorf("parse Lambda policy: %w", err)
	}
	for _, stmt := range policy.Statement {
		if stmt.Sid == statementID {
			return stmt, nil
		}
	}
	return policyStatement{}, fmt.Errorf("statement %s not found", statementID)
}

// observedFromStatement converts a parsed IAM policy statement to ObservedState.
// Extracts principal, action, and condition values (SourceArn, SourceAccount, EventSourceToken).
func observedFromStatement(functionName string, stmt policyStatement) ObservedState {
	observed := ObservedState{StatementId: stmt.Sid, FunctionName: functionName, Action: stringValue(stmt.Action), Principal: principalValue(stmt.Principal)}
	if stmt.Condition != nil {
		if raw, err := json.Marshal(stmt.Condition); err == nil {
			observed.Condition = string(raw)
			observed.SourceArn = extractConditionValue(stmt.Condition, "AWS:SourceArn")
			observed.SourceAccount = extractConditionValue(stmt.Condition, "AWS:SourceAccount")
			observed.EventSourceToken = extractConditionValue(stmt.Condition, "lambda:EventSourceToken")
		}
	}
	return observed
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		if len(typed) > 0 {
			if first, ok := typed[0].(string); ok {
				return first
			}
		}
	}
	return ""
}

func principalValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if service, ok := typed["Service"].(string); ok {
			return service
		}
		if awsPrincipal, ok := typed["AWS"].(string); ok {
			return awsPrincipal
		}
	}
	return ""
}

// extractConditionValue searches the nested condition map for a specific key.
func extractConditionValue(condition any, key string) string {
	conditionMap, ok := condition.(map[string]any)
	if !ok {
		return ""
	}
	for _, rawClause := range conditionMap {
		clause, ok := rawClause.(map[string]any)
		if !ok {
			continue
		}
		if value, ok := clause[key].(string); ok {
			return value
		}
	}
	return ""
}

// IsNotFound returns true if the function or policy does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

// IsConflict returns true if the statement already exists.
func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceConflictException")
}

// IsPreconditionFailed returns true if the function version doesn't exist.
func IsPreconditionFailed(err error) bool {
	return awserr.HasCode(err, "PreconditionFailedException")
}

// IsThrottled returns true if the request was rate-limited by AWS.
func IsThrottled(err error) bool {
	return awserr.HasCode(err, "TooManyRequestsException")
}
