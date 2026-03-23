package lambdaperm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type PermissionAPI interface {
	AddPermission(ctx context.Context, spec LambdaPermissionSpec) (string, error)
	RemovePermission(ctx context.Context, functionName, statementID string) error
	GetPolicy(ctx context.Context, functionName string) (string, error)
	GetPermission(ctx context.Context, functionName, statementID string) (ObservedState, error)
}

type realPermissionAPI struct {
	client  *lambdasdk.Client
	limiter *ratelimit.Limiter
}

func NewPermissionAPI(client *lambdasdk.Client) PermissionAPI {
	return &realPermissionAPI{client: client, limiter: ratelimit.New("lambda-permission", 20, 10)}
}

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

func (r *realPermissionAPI) RemovePermission(ctx context.Context, functionName, statementID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemovePermission(ctx, &lambdasdk.RemovePermissionInput{FunctionName: aws.String(functionName), StatementId: aws.String(statementID)})
	return err
}

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

func IsNotFound(err error) bool {
	return hasPermissionErrorCode(err, "ResourceNotFoundException") || strings.Contains(strings.ToLower(err.Error()), "not found")
}

func IsConflict(err error) bool {
	return hasPermissionErrorCode(err, "ResourceConflictException")
}

func IsPreconditionFailed(err error) bool {
	return hasPermissionErrorCode(err, "PreconditionFailedException")
}

func IsThrottled(err error) bool {
	return hasPermissionErrorCode(err, "TooManyRequestsException")
}

func hasPermissionErrorCode(err error, code string) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == code
	}
	return strings.Contains(err.Error(), code)
}