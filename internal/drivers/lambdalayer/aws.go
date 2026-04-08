package lambdalayer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	lambdasdk "github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/shirvan/praxis/internal/drivers/awserr"

	"github.com/shirvan/praxis/internal/infra/ratelimit"
)

// LayerAPI abstracts AWS Lambda Layer operations for testability.
type LayerAPI interface {
	// PublishLayerVersion publishes a new immutable layer version.
	PublishLayerVersion(ctx context.Context, spec LambdaLayerSpec) (LambdaLayerOutputs, error)
	// GetLatestLayerVersion returns the highest-numbered version with permissions.
	GetLatestLayerVersion(ctx context.Context, layerName string) (ObservedState, error)
	// DeleteLayerVersion removes a single layer version.
	DeleteLayerVersion(ctx context.Context, layerName string, version int64) error
	// ListLayerVersions returns all version numbers sorted descending.
	ListLayerVersions(ctx context.Context, layerName string) ([]int64, error)
	// SyncLayerVersionPermissions converges cross-account and public access permissions.
	SyncLayerVersionPermissions(ctx context.Context, layerName string, version int64, desired PermissionsSpec) (PermissionsSpec, error)
}

// realLayerAPI is the production implementation backed by the Lambda SDK client.
type realLayerAPI struct {
	client  *lambdasdk.Client
	limiter *ratelimit.Limiter
}

// NewLayerAPI creates a production LayerAPI with rate limiting (15 tokens/s, burst 10).
func NewLayerAPI(client *lambdasdk.Client) LayerAPI {
	return &realLayerAPI{client: client, limiter: ratelimit.New("lambda-layer", 15, 10)}
}

// PublishLayerVersion creates a new immutable layer version from the spec.
func (r *realLayerAPI) PublishLayerVersion(ctx context.Context, spec LambdaLayerSpec) (LambdaLayerOutputs, error) {
	if err := validateCode(spec.Code); err != nil {
		return LambdaLayerOutputs{}, err
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return LambdaLayerOutputs{}, err
	}
	input := &lambdasdk.PublishLayerVersionInput{
		LayerName:   aws.String(spec.LayerName),
		Description: optionalString(spec.Description),
		Content:     layerContent(spec.Code),
	}
	if len(spec.CompatibleRuntimes) > 0 {
		input.CompatibleRuntimes = toLayerRuntimes(spec.CompatibleRuntimes)
	}
	if len(spec.CompatibleArchitectures) > 0 {
		input.CompatibleArchitectures = toArchitectures(spec.CompatibleArchitectures)
	}
	if spec.LicenseInfo != "" {
		input.LicenseInfo = aws.String(spec.LicenseInfo)
	}
	out, err := r.client.PublishLayerVersion(ctx, input)
	if err != nil {
		return LambdaLayerOutputs{}, err
	}
	return LambdaLayerOutputs{
		LayerArn:        aws.ToString(out.LayerArn),
		LayerVersionArn: aws.ToString(out.LayerVersionArn),
		LayerName:       spec.LayerName,
		Version:         out.Version,
		CodeSize:        out.Content.CodeSize,
		CodeSha256:      aws.ToString(out.Content.CodeSha256),
		CreatedDate:     aws.ToString(out.CreatedDate),
	}, nil
}

// GetLatestLayerVersion finds the highest version number and returns its full state,
// including permissions from the layer's resource-based policy.
func (r *realLayerAPI) GetLatestLayerVersion(ctx context.Context, layerName string) (ObservedState, error) {
	versions, err := r.ListLayerVersions(ctx, layerName)
	if err != nil {
		return ObservedState{}, err
	}
	if len(versions) == 0 {
		return ObservedState{}, awserr.NotFound(fmt.Sprintf("layer %s not found", layerName))
	}
	if err := r.limiter.Wait(ctx); err != nil {
		return ObservedState{}, err
	}
	out, err := r.client.GetLayerVersion(ctx, &lambdasdk.GetLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(versions[0])})
	if err != nil {
		return ObservedState{}, err
	}
	permissions, err := r.getLayerVersionPermissions(ctx, layerName, versions[0])
	if err != nil {
		return ObservedState{}, err
	}
	return ObservedState{
		LayerArn:                aws.ToString(out.LayerArn),
		LayerVersionArn:         aws.ToString(out.LayerVersionArn),
		LayerName:               layerName,
		Version:                 out.Version,
		Description:             aws.ToString(out.Description),
		CompatibleRuntimes:      fromLayerRuntimes(out.CompatibleRuntimes),
		CompatibleArchitectures: fromArchitectures(out.CompatibleArchitectures),
		LicenseInfo:             aws.ToString(out.LicenseInfo),
		CodeSize:                out.Content.CodeSize,
		CodeSha256:              aws.ToString(out.Content.CodeSha256),
		CreatedDate:             aws.ToString(out.CreatedDate),
		Permissions:             permissions,
	}, nil
}

// DeleteLayerVersion removes a single version of the layer.
func (r *realLayerAPI) DeleteLayerVersion(ctx context.Context, layerName string, version int64) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.DeleteLayerVersion(ctx, &lambdasdk.DeleteLayerVersionInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(version)})
	return err
}

// ListLayerVersions returns all version numbers sorted descending (newest first).
func (r *realLayerAPI) ListLayerVersions(ctx context.Context, layerName string) ([]int64, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	out, err := r.client.ListLayerVersions(ctx, &lambdasdk.ListLayerVersionsInput{LayerName: aws.String(layerName)})
	if err != nil {
		return nil, err
	}
	versions := make([]int64, 0, len(out.LayerVersions))
	for _, version := range out.LayerVersions {
		versions = append(versions, version.Version)
	}
	slices.SortFunc(versions, func(a, b int64) int {
		switch {
		case a > b:
			return -1
		case a < b:
			return 1
		default:
			return 0
		}
	})
	return versions, nil
}

// SyncLayerVersionPermissions converges the layer's resource-based policy.
// Adds missing account grants, removes stale ones, and toggles public access.
func (r *realLayerAPI) SyncLayerVersionPermissions(ctx context.Context, layerName string, version int64, desired PermissionsSpec) (PermissionsSpec, error) {
	current, err := r.getLayerVersionPermissions(ctx, layerName, version)
	if err != nil && !IsNotFound(err) {
		return PermissionsSpec{}, err
	}
	desired = normalizePermissions(desired)
	current = normalizePermissions(current)
	toAddAccounts, toRemoveAccounts := diffStrings(desired.AccountIds, current.AccountIds)
	for _, accountID := range toAddAccounts {
		if err := r.addLayerVersionPermission(ctx, layerName, version, accountID, accountStatementID(accountID)); err != nil {
			return PermissionsSpec{}, err
		}
	}
	for _, accountID := range toRemoveAccounts {
		if err := r.removeLayerVersionPermission(ctx, layerName, version, accountStatementID(accountID)); err != nil && !IsNotFound(err) {
			return PermissionsSpec{}, err
		}
	}
	if desired.Public != current.Public {
		statementID := publicStatementID()
		if desired.Public {
			if err := r.addLayerVersionPermission(ctx, layerName, version, "*", statementID); err != nil {
				return PermissionsSpec{}, err
			}
		} else {
			if err := r.removeLayerVersionPermission(ctx, layerName, version, statementID); err != nil && !IsNotFound(err) {
				return PermissionsSpec{}, err
			}
		}
	}
	return r.getLayerVersionPermissions(ctx, layerName, version)
}

// addLayerVersionPermission grants lambda:GetLayerVersion to a principal.
func (r *realLayerAPI) addLayerVersionPermission(ctx context.Context, layerName string, version int64, principal string, statementID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.AddLayerVersionPermission(ctx, &lambdasdk.AddLayerVersionPermissionInput{
		Action:        aws.String("lambda:GetLayerVersion"),
		LayerName:     aws.String(layerName),
		Principal:     aws.String(principal),
		StatementId:   aws.String(statementID),
		VersionNumber: aws.Int64(version),
	})
	return err
}

// removeLayerVersionPermission revokes a permission statement by ID.
func (r *realLayerAPI) removeLayerVersionPermission(ctx context.Context, layerName string, version int64, statementID string) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return err
	}
	_, err := r.client.RemoveLayerVersionPermission(ctx, &lambdasdk.RemoveLayerVersionPermissionInput{LayerName: aws.String(layerName), StatementId: aws.String(statementID), VersionNumber: aws.Int64(version)})
	return err
}

// getLayerVersionPermissions reads the resource-based policy and extracts permissions.
func (r *realLayerAPI) getLayerVersionPermissions(ctx context.Context, layerName string, version int64) (PermissionsSpec, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return PermissionsSpec{}, err
	}
	out, err := r.client.GetLayerVersionPolicy(ctx, &lambdasdk.GetLayerVersionPolicyInput{LayerName: aws.String(layerName), VersionNumber: aws.Int64(version)})
	if err != nil {
		if IsPolicyNotFound(err) {
			return PermissionsSpec{}, nil
		}
		return PermissionsSpec{}, err
	}
	return permissionsFromPolicy(aws.ToString(out.Policy)), nil
}

// layerContent converts CodeSpec to the Lambda SDK layer content input.
func layerContent(code CodeSpec) *lambdatypes.LayerVersionContentInput {
	content := &lambdatypes.LayerVersionContentInput{}
	if code.S3 != nil {
		content.S3Bucket = aws.String(code.S3.Bucket)
		content.S3Key = aws.String(code.S3.Key)
		content.S3ObjectVersion = optionalString(code.S3.ObjectVersion)
	}
	if code.ZipFile != "" {
		decoded, _ := base64.StdEncoding.DecodeString(code.ZipFile)
		content.ZipFile = decoded
	}
	return content
}

func toArchitectures(values []string) []lambdatypes.Architecture {
	out := make([]lambdatypes.Architecture, 0, len(values))
	for _, value := range values {
		out = append(out, lambdatypes.Architecture(value))
	}
	return out
}

func fromArchitectures(values []lambdatypes.Architecture) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	slices.Sort(out)
	return out
}

func toLayerRuntimes(values []string) []lambdatypes.Runtime {
	out := make([]lambdatypes.Runtime, 0, len(values))
	for _, value := range values {
		out = append(out, lambdatypes.Runtime(value))
	}
	return out
}

func fromLayerRuntimes(values []lambdatypes.Runtime) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	slices.Sort(out)
	return out
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return aws.String(value)
}

// permissionsFromPolicy parses the IAM policy JSON to extract account IDs and public flag.
func permissionsFromPolicy(policy string) PermissionsSpec {
	var doc struct {
		Statement []struct {
			Principal any `json:"Principal"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policy), &doc); err != nil {
		return PermissionsSpec{}
	}
	permissions := PermissionsSpec{}
	for _, stmt := range doc.Statement {
		switch principal := stmt.Principal.(type) {
		case string:
			if principal == "*" {
				permissions.Public = true
			} else {
				permissions.AccountIds = append(permissions.AccountIds, principal)
			}
		case map[string]any:
			if awsPrincipal, ok := principal["AWS"]; ok {
				switch value := awsPrincipal.(type) {
				case string:
					if value == "*" {
						permissions.Public = true
					} else {
						permissions.AccountIds = append(permissions.AccountIds, value)
					}
				case []any:
					for _, item := range value {
						if accountID, ok := item.(string); ok {
							permissions.AccountIds = append(permissions.AccountIds, accountID)
						}
					}
				}
			}
		}
	}
	return normalizePermissions(permissions)
}

// normalizePermissions sorts and deduplicates account IDs, defaulting to empty slice.
func normalizePermissions(spec PermissionsSpec) PermissionsSpec {
	if len(spec.AccountIds) == 0 {
		spec.AccountIds = []string{}
	} else {
		spec.AccountIds = append([]string(nil), spec.AccountIds...)
		slices.Sort(spec.AccountIds)
		spec.AccountIds = slices.Compact(spec.AccountIds)
	}
	return spec
}

// diffStrings returns lists of items to add and remove to go from observed to desired.
func diffStrings(desired, observed []string) ([]string, []string) {
	desiredSet := make(map[string]struct{}, len(desired))
	observedSet := make(map[string]struct{}, len(observed))
	for _, value := range desired {
		desiredSet[value] = struct{}{}
	}
	for _, value := range observed {
		observedSet[value] = struct{}{}
	}
	var add []string
	for _, value := range desired {
		if _, ok := observedSet[value]; !ok {
			add = append(add, value)
		}
	}
	var remove []string
	for _, value := range observed {
		if _, ok := desiredSet[value]; !ok {
			remove = append(remove, value)
		}
	}
	return add, remove
}

func publicStatementID() string { return "praxis-public" }

func accountStatementID(accountID string) string {
	return "praxis-account-" + strings.ReplaceAll(accountID, ":", "-")
}

// validateCode ensures exactly one code source is set (S3 or ZipFile).
func validateCode(code CodeSpec) error {
	count := 0
	if code.S3 != nil {
		count++
	}
	if code.ZipFile != "" {
		count++
	}
	if count != 1 {
		return fmt.Errorf("exactly one Lambda layer code source must be set")
	}
	return nil
}

// IsNotFound returns true if the layer or version does not exist.
func IsNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException") || awserr.IsNotFoundErr(err)
}

// IsInvalidParameter returns true if a parameter value is invalid.
func IsInvalidParameter(err error) bool {
	return awserr.HasCode(err, "InvalidParameterValueException")
}

// IsConflict returns true if a conflicting operation is in progress.
func IsConflict(err error) bool {
	return awserr.HasCode(err, "ResourceConflictException")
}

// IsPolicyNotFound returns true if the layer version has no resource-based policy.
func IsPolicyNotFound(err error) bool {
	return awserr.HasCode(err, "ResourceNotFoundException") && strings.Contains(strings.ToLower(err.Error()), "policy")
}
