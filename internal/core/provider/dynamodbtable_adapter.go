// DynamoDBTable provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + table name.
// Table names are unique within a region; the key combines the AWS region and
// the table name (metadata.name).
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dynamodbtable"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// DynamoDBTableAdapter is the descriptor-driven adapter for DynamoDBTable.
type DynamoDBTableAdapter = GenericAdapter[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs, dynamodbtable.ObservedState]

func dynamoDBTableDescriptor() GenericDescriptor[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs, dynamodbtable.ObservedState] {
	return GenericDescriptor[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs, dynamodbtable.ObservedState]{
		Kind:  dynamodbtable.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(spec json.RawMessage, metadataName string) (dynamodbtable.DynamoDBTableSpec, error) {
			var parsed dynamodbtable.DynamoDBTableSpec
			if err := json.Unmarshal(spec, &parsed); err != nil {
				return dynamodbtable.DynamoDBTableSpec{}, fmt.Errorf("decode DynamoDBTable spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return dynamodbtable.DynamoDBTableSpec{}, fmt.Errorf("DynamoDBTable metadata.name is required")
			}
			if strings.TrimSpace(parsed.Region) == "" {
				return dynamodbtable.DynamoDBTableSpec{}, fmt.Errorf("DynamoDBTable spec.region is required")
			}
			if parsed.Tags == nil {
				parsed.Tags = map[string]string{}
			}
			parsed.Name = name
			return parsed, nil
		},

		KeyFromSpec: func(spec dynamodbtable.DynamoDBTableSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("table name", name); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, name), nil
		},

		ImportKey: func(region, resourceID string) (string, error) {
			if err := ValidateKeyPart("region", region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("resource ID", resourceID); err != nil {
				return "", err
			}
			return JoinKey(region, resourceID), nil
		},

		PrepareSpec: func(spec dynamodbtable.DynamoDBTableSpec, key, account string) dynamodbtable.DynamoDBTableSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out dynamodbtable.DynamoDBTableOutputs) map[string]any {
			result := map[string]any{
				"name":   out.Name,
				"status": out.Status,
			}
			if out.ARN != "" {
				result["arn"] = out.ARN
			}
			if out.ItemCount != 0 {
				result["itemCount"] = out.ItemCount
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[dynamodbtable.DynamoDBTableSpec](func(out dynamodbtable.DynamoDBTableOutputs) string { return out.Name }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs, dynamodbtable.ObservedState] {
			return dynamoDBTableProbe(dynamodbtable.NewDynamoDBTableAPI(awsclient.NewDynamoDBClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[dynamodbtable.DynamoDBTableOutputs] {
			return dynamoDBTableLookupProbe(dynamodbtable.NewDynamoDBTableAPI(awsclient.NewDynamoDBClient(cfg)))
		},

		DiffFields: func(desired dynamodbtable.DynamoDBTableSpec, observed dynamodbtable.ObservedState, _ dynamodbtable.DynamoDBTableOutputs) []types.FieldDiff {
			return dynamodbtable.ComputeFieldDiffs(desired, observed)
		},
	}
}

// dynamoDBTableProbe adapts the driver API to the generic plan probe shape.
func dynamoDBTableProbe(api dynamodbtable.DynamoDBTableAPI) PlanProbeFunc[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs, dynamodbtable.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[dynamodbtable.DynamoDBTableSpec, dynamodbtable.DynamoDBTableOutputs]) (dynamodbtable.ObservedState, bool, error) {
		tableName := input.Identity
		obs, found, err := api.DescribeTable(runCtx, tableName)
		if err != nil {
			if dynamodbtable.IsNotFound(err) {
				return dynamodbtable.ObservedState{}, false, nil
			}
			return dynamodbtable.ObservedState{}, false, err
		}
		return obs, found, nil
	}
}

func dynamoDBTableLookupProbe(api dynamodbtable.DynamoDBTableAPI) LookupProbeFunc[dynamodbtable.DynamoDBTableOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (dynamodbtable.DynamoDBTableOutputs, bool, error) {
		identity := nativeLookupIdentity(filter)
		if identity == "" {
			return dynamodbtable.DynamoDBTableOutputs{}, false, restate.TerminalError(
				fmt.Errorf("DynamoDBTable lookup supports id or name; tag-only lookup is not available"),
				400,
			)
		}
		observed, found, err := api.DescribeTable(ctx, identity)
		if err != nil {
			if isLookupNotFound(err, dynamodbtable.IsNotFound) {
				return dynamodbtable.DynamoDBTableOutputs{}, false, nil
			}
			return dynamodbtable.DynamoDBTableOutputs{}, false, err
		}
		if !found || !matchesNativeLookupFilter(observed.Name, observed.Tags, filter) {
			return dynamodbtable.DynamoDBTableOutputs{}, false, nil
		}
		return dynamodbtable.DynamoDBTableOutputs{
			ARN:       observed.ARN,
			Name:      observed.Name,
			Status:    observed.Status,
			ItemCount: observed.ItemCount,
		}, true, nil
	}
}

// NewDynamoDBTableAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewDynamoDBTableAdapterWithAuth(auth authservice.AuthClient) *DynamoDBTableAdapter {
	return NewGenericAdapter(dynamoDBTableDescriptor(), auth)
}

// NewDynamoDBTableAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewDynamoDBTableAdapterWithAPI(api dynamodbtable.DynamoDBTableAPI) *DynamoDBTableAdapter {
	return NewGenericAdapterWithProbes(dynamoDBTableDescriptor(), dynamoDBTableProbe(api), dynamoDBTableLookupProbe(api))
}
