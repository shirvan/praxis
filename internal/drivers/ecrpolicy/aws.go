package ecrpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecrsdk "github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/smithy-go"

	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

type LifecyclePolicyAPI interface {
	PutLifecyclePolicy(ctx context.Context, spec ECRLifecyclePolicySpec) (ObservedState, error)
	GetLifecyclePolicy(ctx context.Context, repositoryName string) (ObservedState, error)
	DeleteLifecyclePolicy(ctx context.Context, repositoryName string) error
}

type realLifecyclePolicyAPI struct {
	client  *ecrsdk.Client
	limiter *ratelimit.Limiter
}

func NewLifecyclePolicyAPI(client *ecrsdk.Client) LifecyclePolicyAPI {
	return &realLifecyclePolicyAPI{client: client, limiter: ratelimit.New("ecr-lifecycle-policy", 15, 5)}
}

func (r *realLifecyclePolicyAPI) PutLifecyclePolicy(ctx context.Context, spec ECRLifecyclePolicySpec) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	_, err := r.client.PutLifecyclePolicy(ctx, &ecrsdk.PutLifecyclePolicyInput{RepositoryName: aws.String(spec.RepositoryName), LifecyclePolicyText: aws.String(spec.LifecyclePolicyText)})
	if err != nil {
		return ObservedState{}, err
	}
	return r.GetLifecyclePolicy(ctx, spec.RepositoryName)
}

func (r *realLifecyclePolicyAPI) GetLifecyclePolicy(ctx context.Context, repositoryName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetLifecyclePolicy(ctx, &ecrsdk.GetLifecyclePolicyInput{RepositoryName: aws.String(repositoryName)})
	if err != nil {
		return ObservedState{}, err
	}
	state := ObservedState{RepositoryName: repositoryName, LifecyclePolicyText: aws.ToString(out.LifecyclePolicyText), RegistryId: aws.ToString(out.RegistryId)}
	repoOut, repoErr := r.client.DescribeRepositories(ctx, &ecrsdk.DescribeRepositoriesInput{RepositoryNames: []string{repositoryName}})
	if repoErr == nil && len(repoOut.Repositories) > 0 {
		state.RepositoryArn = aws.ToString(repoOut.Repositories[0].RepositoryArn)
		if state.RegistryId == "" {
			state.RegistryId = aws.ToString(repoOut.Repositories[0].RegistryId)
		}
	}
	return state, nil
}

func (r *realLifecyclePolicyAPI) DeleteLifecyclePolicy(ctx context.Context, repositoryName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteLifecyclePolicy(ctx, &ecrsdk.DeleteLifecyclePolicyInput{RepositoryName: aws.String(repositoryName)})
	return err
}

func outputsFromObserved(observed ObservedState) ECRLifecyclePolicyOutputs {
	return ECRLifecyclePolicyOutputs{RepositoryName: observed.RepositoryName, RepositoryArn: observed.RepositoryArn, RegistryId: observed.RegistryId}
}

func specFromObserved(observed ObservedState) ECRLifecyclePolicySpec {
	return ECRLifecyclePolicySpec{Region: regionFromRepositoryARN(observed.RepositoryArn), RepositoryName: observed.RepositoryName, LifecyclePolicyText: observed.LifecyclePolicyText}
}

func normalizePolicy(value string) string {
	if value == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return value
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return value
	}
	return string(encoded)
}

func regionFromRepositoryARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) > 3 {
		return parts[3]
	}
	return ""
}

func IsNotFound(err error) bool {
	if awserr.HasCode(err, "LifecyclePolicyNotFoundException", "RepositoryNotFoundException") {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "LifecyclePolicyNotFoundException" || apiErr.ErrorCode() == "RepositoryNotFoundException"
	}
	return strings.Contains(err.Error(), "not found")
}

func IsInvalidParameter(err error) bool {
	return awserr.HasCode(err, "InvalidParameterException", "ValidationException")
}

func IsRepositoryNotFound(err error) bool {
	return awserr.HasCode(err, "RepositoryNotFoundException")
}

func validatePolicyJSON(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("lifecyclePolicyText is required")
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return fmt.Errorf("lifecyclePolicyText must be valid JSON: %w", err)
	}
	return nil
}