// DBSubnetGroup provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + subnet group name.
// DB subnet groups are region-scoped; the key combines the AWS region and
// subnet group name.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dbsubnetgroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// DBSubnetGroupAdapter is the descriptor-driven adapter for DBSubnetGroup.
type DBSubnetGroupAdapter = GenericAdapter[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs, dbsubnetgroup.ObservedState]

func dbSubnetGroupDescriptor() GenericDescriptor[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs, dbsubnetgroup.ObservedState] {
	return GenericDescriptor[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs, dbsubnetgroup.ObservedState]{
		Kind:  dbsubnetgroup.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (dbsubnetgroup.DBSubnetGroupSpec, error) {
			var spec struct {
				Region      string            `json:"region"`
				Description string            `json:"description"`
				SubnetIds   []string          `json:"subnetIds"`
				Tags        map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return dbsubnetgroup.DBSubnetGroupSpec{}, fmt.Errorf("decode DBSubnetGroup spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return dbsubnetgroup.DBSubnetGroupSpec{}, fmt.Errorf("DBSubnetGroup metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return dbsubnetgroup.DBSubnetGroupSpec{}, fmt.Errorf("DBSubnetGroup spec.region is required")
			}
			return dbsubnetgroup.DBSubnetGroupSpec{Region: strings.TrimSpace(spec.Region), GroupName: name, Description: spec.Description, SubnetIds: spec.SubnetIds, Tags: spec.Tags}, nil
		},

		KeyFromSpec: func(spec dbsubnetgroup.DBSubnetGroupSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("db subnet group name", spec.GroupName); err != nil {
				return "", err
			}
			return JoinKey(spec.Region, spec.GroupName), nil
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

		PrepareSpec: func(spec dbsubnetgroup.DBSubnetGroupSpec, _ string, account string) dbsubnetgroup.DBSubnetGroupSpec {
			spec.Account = account
			return spec
		},

		NormalizeOutputs: func(out dbsubnetgroup.DBSubnetGroupOutputs) map[string]any {
			return map[string]any{
				"groupName":         out.GroupName,
				"arn":               out.ARN,
				"vpcId":             out.VpcId,
				"subnetIds":         out.SubnetIds,
				"availabilityZones": out.AvailabilityZones,
				"status":            out.Status,
			}
		},

		PlanIdentity: storedPlanIdentity[dbsubnetgroup.DBSubnetGroupSpec](func(out dbsubnetgroup.DBSubnetGroupOutputs) string { return out.GroupName }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs, dbsubnetgroup.ObservedState] {
			return dbSubnetGroupProbe(dbsubnetgroup.NewDBSubnetGroupAPI(awsclient.NewRDSClient(cfg)))
		},

		DiffFields: func(desired dbsubnetgroup.DBSubnetGroupSpec, observed dbsubnetgroup.ObservedState, _ dbsubnetgroup.DBSubnetGroupOutputs) []types.FieldDiff {
			return dbsubnetgroup.ComputeFieldDiffs(desired, observed)
		},
	}
}

// dbSubnetGroupProbe adapts the driver API to the generic plan probe shape.
func dbSubnetGroupProbe(api dbsubnetgroup.DBSubnetGroupAPI) PlanProbeFunc[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs, dbsubnetgroup.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[dbsubnetgroup.DBSubnetGroupSpec, dbsubnetgroup.DBSubnetGroupOutputs]) (dbsubnetgroup.ObservedState, bool, error) {
		groupName := input.Identity
		obs, err := api.DescribeDBSubnetGroup(runCtx, groupName)
		if err != nil {
			if dbsubnetgroup.IsNotFound(err) {
				return dbsubnetgroup.ObservedState{}, false, nil
			}
			return dbsubnetgroup.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewDBSubnetGroupAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewDBSubnetGroupAdapterWithAuth(auth authservice.AuthClient) *DBSubnetGroupAdapter {
	return NewGenericAdapter(dbSubnetGroupDescriptor(), auth)
}

// NewDBSubnetGroupAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewDBSubnetGroupAdapterWithAPI(api dbsubnetgroup.DBSubnetGroupAPI) *DBSubnetGroupAdapter {
	return NewGenericAdapterWithProbe(dbSubnetGroupDescriptor(), dbSubnetGroupProbe(api))
}
