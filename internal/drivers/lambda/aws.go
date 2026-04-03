package lambda

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	restate "github.com/restatedev/sdk-go"
	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// LambdaAPI abstracts AWS Lambda operations for testability.
type LambdaAPI interface {
	// CreateFunction creates a new Lambda function from the spec.
	CreateFunction(ctx context.Context, spec LambdaFunctionSpec) (string, error)
	// UpdateFunctionCode updates only the code deployment artifact.
	UpdateFunctionCode(ctx context.Context, spec LambdaFunctionSpec) error
	// UpdateFunctionConfiguration updates runtime config (role, memory, timeout, etc.).
	UpdateFunctionConfiguration(ctx context.Context, spec LambdaFunctionSpec, observed ObservedState) error
	// DescribeFunction fetches the current function state via GetFunction.
	DescribeFunction(ctx context.Context, functionName string) (ObservedState, error)
	// DeleteFunction removes the function.
	DeleteFunction(ctx context.Context, functionName string) error
	// UpdateTags synchronizes tags (untag stale + tag desired).
	UpdateTags(ctx context.Context, functionArn string, tags map[string]string) error
	// WaitForFunctionStable polls until the function reaches Active/Successful state.
	WaitForFunctionStable(ctx context.Context, functionName string, timeout time.Duration) error
}

// realLambdaAPI is the production implementation backed by the Lambda SDK client.
type realLambdaAPI struct {
	client  *lambdasdk.Client
	limiter *ratelimit.Limiter
}

// NewLambdaAPI creates a production LambdaAPI with rate limiting (15 tokens/s, burst 10).
func NewLambdaAPI(client *lambdasdk.Client) LambdaAPI {
	return &realLambdaAPI{client: client, limiter: ratelimit.New("lambda-function", 15, 10)}
}

// CreateFunction creates a new Lambda function from the full spec.
// Maps CodeSpec to FunctionCode, handles Zip vs Image package types,
// and applies managed-key tag alongside user tags.
func (r *realLambdaAPI) CreateFunction(ctx context.Context, spec LambdaFunctionSpec) (string, error) {
	if err := validateCode(spec.Code); err != nil {
		return "", err
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return "", err
	}
	input := &lambdasdk.CreateFunctionInput{
		FunctionName: aws.String(spec.FunctionName),
		Role:         aws.String(spec.Role),
		Description:  optionalString(spec.Description),
		MemorySize:   aws.Int32(spec.MemorySize),
		Timeout:      aws.Int32(spec.Timeout),
		Tags:         withManagedKey(spec.ManagedKey, spec.Tags),
		Code:         functionCode(spec.Code),
	}
	if spec.PackageType != "" {
		input.PackageType = lambdatypes.PackageType(spec.PackageType)
	} else {
		input.PackageType = lambdatypes.PackageTypeZip
	}
	if input.PackageType == lambdatypes.PackageTypeZip {
		input.Runtime = lambdatypes.Runtime(spec.Runtime)
		input.Handler = optionalString(spec.Handler)
	}
	if env := normalizeEnv(spec.Environment); env != nil {
		input.Environment = &lambdatypes.Environment{Variables: env}
	}
	if len(spec.Layers) > 0 {
		input.Layers = append([]string(nil), spec.Layers...)
	}
	if len(spec.Architectures) > 0 {
		input.Architectures = toArchitectures(spec.Architectures)
	}
	if spec.VPCConfig != nil {
		input.VpcConfig = &lambdatypes.VpcConfig{SubnetIds: append([]string(nil), spec.VPCConfig.SubnetIds...), SecurityGroupIds: append([]string(nil), spec.VPCConfig.SecurityGroupIds...)}
	}
	if spec.DeadLetterConfig != nil {
		input.DeadLetterConfig = &lambdatypes.DeadLetterConfig{TargetArn: aws.String(spec.DeadLetterConfig.TargetArn)}
	}
	if spec.TracingConfig != nil {
		input.TracingConfig = &lambdatypes.TracingConfig{Mode: lambdatypes.TracingMode(spec.TracingConfig.Mode)}
	}
	if spec.EphemeralStorage != nil {
		input.EphemeralStorage = &lambdatypes.EphemeralStorage{Size: aws.Int32(spec.EphemeralStorage.Size)}
	}
	output, err := r.client.CreateFunction(ctx, input)
	if err != nil {
		return "", err
	}
	return aws.ToString(output.FunctionArn), nil
}

// UpdateFunctionCode replaces the function's deployment artifact (S3, zip, or image).
func (r *realLambdaAPI) UpdateFunctionCode(ctx context.Context, spec LambdaFunctionSpec) error {
	if err := validateCode(spec.Code); err != nil {
		return err
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &lambdasdk.UpdateFunctionCodeInput{FunctionName: aws.String(spec.FunctionName)}
	if spec.Code.S3 != nil {
		input.S3Bucket = aws.String(spec.Code.S3.Bucket)
		input.S3Key = aws.String(spec.Code.S3.Key)
		input.S3ObjectVersion = optionalString(spec.Code.S3.ObjectVersion)
	}
	if spec.Code.ZipFile != "" {
		decoded, err := base64.StdEncoding.DecodeString(spec.Code.ZipFile)
		if err != nil {
			return fmt.Errorf("decode zipFile: %w", err)
		}
		input.ZipFile = decoded
	}
	if spec.Code.ImageURI != "" {
		input.ImageUri = aws.String(spec.Code.ImageURI)
	}
	_, err := r.client.UpdateFunctionCode(ctx, input)
	return err
}

// UpdateFunctionConfiguration updates runtime configuration fields.
// Covers role, description, memory, timeout, environment, layers, VPC, DLQ, tracing, ephemeral storage.
func (r *realLambdaAPI) UpdateFunctionConfiguration(ctx context.Context, spec LambdaFunctionSpec, observed ObservedState) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	input := &lambdasdk.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(spec.FunctionName),
		Role:         aws.String(spec.Role),
		Description:  optionalString(spec.Description),
		MemorySize:   aws.Int32(spec.MemorySize),
		Timeout:      aws.Int32(spec.Timeout),
	}
	if spec.PackageType == "" || spec.PackageType == string(lambdatypes.PackageTypeZip) {
		input.Runtime = lambdatypes.Runtime(spec.Runtime)
		input.Handler = optionalString(spec.Handler)
	}
	if env := normalizeEnv(spec.Environment); env != nil || len(observed.Environment) > 0 {
		input.Environment = &lambdatypes.Environment{Variables: env}
	}
	input.Layers = append([]string(nil), spec.Layers...)
	if spec.VPCConfig != nil {
		input.VpcConfig = &lambdatypes.VpcConfig{SubnetIds: append([]string(nil), spec.VPCConfig.SubnetIds...), SecurityGroupIds: append([]string(nil), spec.VPCConfig.SecurityGroupIds...)}
	}
	if spec.DeadLetterConfig != nil {
		input.DeadLetterConfig = &lambdatypes.DeadLetterConfig{TargetArn: aws.String(spec.DeadLetterConfig.TargetArn)}
	}
	if spec.TracingConfig != nil {
		input.TracingConfig = &lambdatypes.TracingConfig{Mode: lambdatypes.TracingMode(spec.TracingConfig.Mode)}
	}
	if spec.EphemeralStorage != nil {
		input.EphemeralStorage = &lambdatypes.EphemeralStorage{Size: aws.Int32(spec.EphemeralStorage.Size)}
	}
	_, err := r.client.UpdateFunctionConfiguration(ctx, input)
	return err
}

// DescribeFunction calls GetFunction and maps the result to ObservedState.
// Extracts configuration, environment, layers, VPC, DLQ, tracing, architectures,
// ephemeral storage, tags, and code metadata.
func (r *realLambdaAPI) DescribeFunction(ctx context.Context, functionName string) (ObservedState, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	output, err := r.client.GetFunction(ctx, &lambdasdk.GetFunctionInput{FunctionName: aws.String(functionName)})
	if err != nil {
		return ObservedState{}, err
	}
	if output.Configuration == nil {
		return ObservedState{}, fmt.Errorf("lambda function %s returned empty configuration", functionName)
	}
	conf := output.Configuration
	observed := ObservedState{
		FunctionArn:      aws.ToString(conf.FunctionArn),
		FunctionName:     aws.ToString(conf.FunctionName),
		Role:             aws.ToString(conf.Role),
		PackageType:      string(conf.PackageType),
		Runtime:          string(conf.Runtime),
		Handler:          aws.ToString(conf.Handler),
		Description:      aws.ToString(conf.Description),
		MemorySize:       aws.ToInt32(conf.MemorySize),
		Timeout:          aws.ToInt32(conf.Timeout),
		Environment:      map[string]string{},
		Tags:             output.Tags,
		Version:          aws.ToString(conf.Version),
		State:            string(conf.State),
		LastModified:     aws.ToString(conf.LastModified),
		LastUpdateStatus: string(conf.LastUpdateStatus),
		CodeSha256:       aws.ToString(conf.CodeSha256),
	}
	if conf.Environment != nil && conf.Environment.Variables != nil {
		observed.Environment = conf.Environment.Variables
	}
	for _, layer := range conf.Layers {
		observed.Layers = append(observed.Layers, aws.ToString(layer.Arn))
	}
	if conf.VpcConfig != nil {
		observed.VpcConfig = VPCConfigSpec{SubnetIds: append([]string(nil), conf.VpcConfig.SubnetIds...), SecurityGroupIds: append([]string(nil), conf.VpcConfig.SecurityGroupIds...)}
	}
	if conf.DeadLetterConfig != nil {
		observed.DeadLetterTarget = aws.ToString(conf.DeadLetterConfig.TargetArn)
	}
	if conf.TracingConfig != nil {
		observed.TracingMode = string(conf.TracingConfig.Mode)
	}
	for _, arch := range conf.Architectures {
		observed.Architectures = append(observed.Architectures, string(arch))
	}
	if conf.EphemeralStorage != nil {
		observed.EphemeralSize = aws.ToInt32(conf.EphemeralStorage.Size)
	}
	if output.Code != nil {
		observed.ImageURI = aws.ToString(output.Code.ImageUri)
	}
	return observed, nil
}

// DeleteFunction removes the Lambda function.
func (r *realLambdaAPI) DeleteFunction(ctx context.Context, functionName string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteFunction(ctx, &lambdasdk.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	return err
}

// UpdateTags synchronizes tags on the function using untag-stale + tag-desired pattern.
// Stale non-praxis: tags are removed first, then desired tags are applied.
func (r *realLambdaAPI) UpdateTags(ctx context.Context, functionArn string, tags map[string]string) error {
	observed, err := r.DescribeFunction(ctx, functionArn)
	if err != nil {
		return err
	}
	desired := withManagedKey("", tags)
	current := observed.Tags
	var stale []string
	for key := range current {
		if strings.HasPrefix(key, "praxis:") {
			continue
		}
		if _, ok := desired[key]; !ok {
			stale = append(stale, key)
		}
	}
	slices.Sort(stale)
	if len(stale) > 0 {
		if err := r.limiter.Wait(ctx); err != nil {
			return err
		}
		if _, err := r.client.UntagResource(ctx, &lambdasdk.UntagResourceInput{Resource: aws.String(functionArn), TagKeys: stale}); err != nil {
			return err
		}
	}
	if len(desired) == 0 {
		return nil
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err = r.client.TagResource(ctx, &lambdasdk.TagResourceInput{Resource: aws.String(functionArn), Tags: desired})
	return err
}

// WaitForFunctionStable polls DescribeFunction until LastUpdateStatus=Successful and State=Active,
// or until the timeout is reached. Returns a terminal error if the update failed.
func (r *realLambdaAPI) WaitForFunctionStable(ctx context.Context, functionName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		obs, err := r.DescribeFunction(ctx, functionName)
		if err != nil {
			return err
		}
		if (obs.LastUpdateStatus == "" || obs.LastUpdateStatus == string(lambdatypes.LastUpdateStatusSuccessful)) &&
			(obs.State == "" || obs.State == string(lambdatypes.StateActive)) {
			return nil
		}
		if obs.LastUpdateStatus == string(lambdatypes.LastUpdateStatusFailed) {
			return restate.TerminalError(fmt.Errorf("lambda function %s update failed", functionName), 500)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for Lambda function %s to stabilize", functionName)
		}
		time.Sleep(2 * time.Second)
	}
}

// validateCode ensures exactly one code source is set (S3, ZipFile, or ImageURI).
func validateCode(code CodeSpec) error {
	count := 0
	if code.S3 != nil {
		count++
	}
	if code.ZipFile != "" {
		count++
	}
	if code.ImageURI != "" {
		count++
	}
	if count != 1 {
		return fmt.Errorf("exactly one Lambda code source must be set")
	}
	return nil
}

// functionCode converts CodeSpec to the Lambda SDK FunctionCode type.
func functionCode(code CodeSpec) *lambdatypes.FunctionCode {
	input := &lambdatypes.FunctionCode{}
	if code.S3 != nil {
		input.S3Bucket = aws.String(code.S3.Bucket)
		input.S3Key = aws.String(code.S3.Key)
		input.S3ObjectVersion = optionalString(code.S3.ObjectVersion)
	}
	if code.ZipFile != "" {
		decoded, _ := base64.StdEncoding.DecodeString(code.ZipFile)
		input.ZipFile = decoded
	}
	if code.ImageURI != "" {
		input.ImageUri = aws.String(code.ImageURI)
	}
	return input
}

// toArchitectures converts string slice to Lambda Architecture enum slice.
func toArchitectures(values []string) []lambdatypes.Architecture {
	out := make([]lambdatypes.Architecture, 0, len(values))
	for _, value := range values {
		out = append(out, lambdatypes.Architecture(value))
	}
	return out
}

// normalizeEnv returns a defensive copy of the env map, or nil if empty.
func normalizeEnv(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	maps.Copy(out, values)
	return out
}

// withManagedKey merges user tags with the praxis:managed-key system tag.
func withManagedKey(managedKey string, tags map[string]string) map[string]string {
	if len(tags) == 0 && managedKey == "" {
		return nil
	}
	out := make(map[string]string, len(tags)+1)
	maps.Copy(out, tags)
	if managedKey != "" {
		out["praxis:managed-key"] = managedKey
	}
	return out
}

// optionalString returns a *string or nil if the value is empty.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return aws.String(value)
}

// IsNotFound returns true if the Lambda function does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException")
}

// IsConflict returns true if a conflicting operation is in progress.
func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceConflictException")
}

// IsInvalidParameter returns true if a parameter value is invalid.
func IsInvalidParameter(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValueException")
}

// IsAccessDenied returns true if the caller lacks IAM permissions.
func IsAccessDenied(err error) bool {
	return awserr.HasCode(err, "AccessDeniedException")
}

// IsThrottled returns true if the request was rate-limited by AWS.
func IsThrottled(err error) bool {
	return awserr.HasCode(err, "TooManyRequestsException")
}
