package command

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/shirvan/praxis/internal/core/orchestrator"
	"github.com/shirvan/praxis/pkg/types"
)

const SavedPlanVersion = 1

func executionPlanFromCompiled(
	compiled *compiledTemplate,
	deploymentKey string,
	account string,
	workspace string,
	variables map[string]any,
	targets []string,
) *types.ExecutionPlan {
	if compiled == nil {
		return nil
	}
	plan := &types.ExecutionPlan{
		DeploymentKey: deploymentKey,
		Account:       account,
		Workspace:     workspace,
		Variables:     cloneAnyMap(variables),
		TemplatePath:  compiled.TemplatePath,
		Targets:       append([]string(nil), targets...),
		Resources:     make([]types.ExecutionPlanResource, 0, len(compiled.PlanResources)),
	}
	for i := range compiled.PlanResources {
		resource := compiled.PlanResources[i]
		plan.Resources = append(plan.Resources, types.ExecutionPlanResource{
			Name:          resource.Name,
			Kind:          resource.Kind,
			DriverService: resource.DriverService,
			Key:           resource.Key,
			Spec:          append(json.RawMessage(nil), resource.Spec...),
			Dependencies:  append([]string(nil), resource.Dependencies...),
			Expressions:   cloneStringMap(resource.Expressions),
			Lifecycle:     cloneLifecycle(resource.Lifecycle),
		})
	}
	return plan
}

func executionPlanToDeploymentPlan(
	plan types.ExecutionPlan,
	createdAt time.Time,
	maxParallelism int,
	retryConfig *orchestrator.RetryConfig,
) orchestrator.DeploymentPlan {
	resources := make([]orchestrator.PlanResource, 0, len(plan.Resources))
	for i := range plan.Resources {
		resource := plan.Resources[i]
		resources = append(resources, orchestrator.PlanResource{
			Name:          resource.Name,
			Kind:          resource.Kind,
			DriverService: resource.DriverService,
			Key:           resource.Key,
			Spec:          append(json.RawMessage(nil), resource.Spec...),
			Dependencies:  append([]string(nil), resource.Dependencies...),
			Expressions:   cloneStringMap(resource.Expressions),
			Lifecycle:     cloneLifecycle(resource.Lifecycle),
		})
	}
	return orchestrator.DeploymentPlan{
		Key:            plan.DeploymentKey,
		Account:        plan.Account,
		Workspace:      plan.Workspace,
		Resources:      resources,
		Variables:      cloneAnyMap(plan.Variables),
		CreatedAt:      createdAt,
		TemplatePath:   plan.TemplatePath,
		MaxParallelism: maxParallelism,
		RetryConfig:    retryConfig,
	}
}

func ComputeSavedPlanHash(plan types.ExecutionPlan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("marshal execution plan: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func SignSavedPlanHash(contentHash string, signingKey []byte) string {
	mac := hmac.New(sha256.New, signingKey)
	_, _ = mac.Write([]byte(contentHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifySavedPlan(saved types.SavedPlan, signingKey []byte) error {
	hash, err := ComputeSavedPlanHash(saved.Plan)
	if err != nil {
		return err
	}
	if hash != saved.ContentHash {
		return fmt.Errorf("saved plan content hash mismatch")
	}
	if saved.Signature == "" {
		return nil
	}
	if len(signingKey) == 0 {
		return fmt.Errorf("saved plan is signed but no signing key was provided")
	}
	if expected := SignSavedPlanHash(saved.ContentHash, signingKey); !hmac.Equal([]byte(expected), []byte(saved.Signature)) {
		return fmt.Errorf("saved plan signature verification failed")
	}
	return nil
}

func TemplateSourceHash(source string) string {
	hash := sha256.Sum256([]byte(source))
	return hex.EncodeToString(hash[:])
}

func WriteSavedPlanFile(path string, saved types.SavedPlan) error {
	encoded, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal saved plan: %w", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("write saved plan %q: %w", path, err)
	}
	return nil
}

func ReadSavedPlanFile(path string) (types.SavedPlan, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return types.SavedPlan{}, fmt.Errorf("read saved plan %q: %w", path, err)
	}
	var saved types.SavedPlan
	if err := json.Unmarshal(encoded, &saved); err != nil {
		return types.SavedPlan{}, fmt.Errorf("decode saved plan %q: %w", path, err)
	}
	if saved.Version != SavedPlanVersion {
		return types.SavedPlan{}, fmt.Errorf("unsupported saved plan version %d", saved.Version)
	}
	return saved, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(input))
	maps.Copy(cloned, input)
	return cloned
}

func cloneLifecycle(input *types.LifecyclePolicy) *types.LifecyclePolicy {
	if input == nil {
		return nil
	}
	cloned := *input
	if input.IgnoreChanges != nil {
		cloned.IgnoreChanges = append([]string(nil), input.IgnoreChanges...)
	}
	if input.Finalizers != nil {
		cloned.Finalizers = append([]string(nil), input.Finalizers...)
	}
	if input.Retry != nil {
		retry := *input.Retry
		if input.Retry.MaxRetries != nil {
			value := *input.Retry.MaxRetries
			retry.MaxRetries = &value
		}
		cloned.Retry = &retry
	}
	if input.Timeouts != nil {
		timeouts := *input.Timeouts
		cloned.Timeouts = &timeouts
	}
	if input.Wait != nil {
		wait := *input.Wait
		if input.Wait.Enabled != nil {
			value := *input.Wait.Enabled
			wait.Enabled = &value
		}
		cloned.Wait = &wait
	}
	return &cloned
}
