// DBParameterGroup provider adapter — descriptor for the GenericAdapter.
package provider

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/dbparametergroup"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type DBParameterGroupAdapter = GenericAdapter[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs, dbparametergroup.ObservedState]

func dbParameterGroupDescriptor() GenericDescriptor[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs, dbparametergroup.ObservedState] {
	return GenericDescriptor[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs, dbparametergroup.ObservedState]{
		Kind:  dbparametergroup.ServiceName,
		Scope: KeyScopeRegion,
		DecodeSpec: func(raw json.RawMessage, metadataName string) (dbparametergroup.DBParameterGroupSpec, error) {
			var spec struct {
				Region      string            `json:"region"`
				Type        string            `json:"type"`
				Family      string            `json:"family"`
				Description string            `json:"description"`
				Parameters  map[string]string `json:"parameters"`
				Tags        map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(raw, &spec); err != nil {
				return dbparametergroup.DBParameterGroupSpec{}, fmt.Errorf("decode DBParameterGroup spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return dbparametergroup.DBParameterGroupSpec{}, fmt.Errorf("DBParameterGroup metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return dbparametergroup.DBParameterGroupSpec{}, fmt.Errorf("DBParameterGroup spec.region is required")
			}
			return dbparametergroup.DBParameterGroupSpec{
				Region: strings.TrimSpace(spec.Region), GroupName: name, Type: strings.TrimSpace(spec.Type),
				Family: spec.Family, Description: spec.Description, Parameters: spec.Parameters, Tags: spec.Tags,
			}, nil
		},
		KeyFromSpec: func(spec dbparametergroup.DBParameterGroupSpec, _ string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			if err := ValidateKeyPart("db parameter group name", spec.GroupName); err != nil {
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
		PrepareSpec: func(spec dbparametergroup.DBParameterGroupSpec, _ string, account string) dbparametergroup.DBParameterGroupSpec {
			spec.Account = account
			return spec
		},
		NormalizeOutputs: func(out dbparametergroup.DBParameterGroupOutputs) map[string]any {
			return map[string]any{"groupName": out.GroupName, "arn": out.ARN, "family": out.Family, "type": out.Type}
		},
		PlanIdentity: func(desired dbparametergroup.DBParameterGroupSpec, outputs dbparametergroup.DBParameterGroupOutputs) (string, bool) {
			if outputs.GroupName != "" {
				return outputs.GroupName, true
			}
			return desired.GroupName, desired.GroupName != ""
		},
		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs, dbparametergroup.ObservedState] {
			return dbParameterGroupProbe(dbparametergroup.NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg)))
		},
		NewLookupProbe: func(cfg aws.Config) LookupProbeFunc[dbparametergroup.DBParameterGroupOutputs] {
			return dbParameterGroupLookupProbe(dbparametergroup.NewDBParameterGroupAPI(awsclient.NewRDSClient(cfg)))
		},
		DiffFields: func(desired dbparametergroup.DBParameterGroupSpec, observed dbparametergroup.ObservedState, _ dbparametergroup.DBParameterGroupOutputs) []types.FieldDiff {
			return dbparametergroup.ComputeFieldDiffs(desired, observed)
		},
	}
}

func dbParameterGroupLookupProbe(api dbparametergroup.DBParameterGroupAPI) LookupProbeFunc[dbparametergroup.DBParameterGroupOutputs] {
	return func(ctx restate.RunContext, filter LookupFilter) (dbparametergroup.DBParameterGroupOutputs, bool, error) {
		if strings.TrimSpace(filter.Name) != "" || strings.TrimSpace(filter.ID) == "" {
			return dbparametergroup.DBParameterGroupOutputs{}, false, restate.TerminalError(fmt.Errorf("DBParameterGroup lookup requires a parameter-group ARN in filter.id"), 400)
		}
		groupName, groupType, err := dbParameterGroupIdentityFromARN(filter.ID)
		if err != nil {
			return dbparametergroup.DBParameterGroupOutputs{}, false, restate.TerminalError(err, 400)
		}
		observed, err := api.DescribeParameterGroup(ctx, groupName, groupType)
		if err != nil {
			if isLookupNotFound(err, dbparametergroup.IsNotFound) {
				return dbparametergroup.DBParameterGroupOutputs{}, false, nil
			}
			return dbparametergroup.DBParameterGroupOutputs{}, false, err
		}
		if observed.ARN != strings.TrimSpace(filter.ID) || !matchesLookupTags(observed.Tags, LookupFilter{Tag: filter.Tag}) {
			return dbparametergroup.DBParameterGroupOutputs{}, false, nil
		}
		return dbparametergroup.DBParameterGroupOutputs{
			GroupName: observed.GroupName, ARN: observed.ARN, Family: observed.Family, Type: observed.Type,
		}, true, nil
	}
}

func dbParameterGroupIdentityFromARN(value string) (string, string, error) {
	arn := strings.TrimSpace(value)
	for marker, groupType := range map[string]string{
		":cluster-pg:": dbparametergroup.TypeCluster,
		":pg:":         dbparametergroup.TypeDB,
	} {
		if index := strings.Index(arn, marker); index >= 0 {
			name := strings.TrimSpace(arn[index+len(marker):])
			if name != "" {
				return name, groupType, nil
			}
		}
	}
	return "", "", fmt.Errorf("DBParameterGroup filter.id must be an RDS parameter-group ARN")
}

func dbParameterGroupProbe(api dbparametergroup.DBParameterGroupAPI) PlanProbeFunc[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs, dbparametergroup.ObservedState] {
	return func(ctx restate.RunContext, input PlanProbeInput[dbparametergroup.DBParameterGroupSpec, dbparametergroup.DBParameterGroupOutputs]) (dbparametergroup.ObservedState, bool, error) {
		observed, err := api.DescribeParameterGroup(ctx, input.Identity, input.Desired.Type)
		if err != nil {
			if dbparametergroup.IsNotFound(err) {
				return dbparametergroup.ObservedState{}, false, nil
			}
			return dbparametergroup.ObservedState{}, false, err
		}
		return observed, true, nil
	}
}

func NewDBParameterGroupAdapterWithAuth(auth authservice.AuthClient) *DBParameterGroupAdapter {
	return NewGenericAdapter(dbParameterGroupDescriptor(), auth)
}

func NewDBParameterGroupAdapterWithAPI(api dbparametergroup.DBParameterGroupAPI) *DBParameterGroupAdapter {
	return NewGenericAdapterWithProbes(dbParameterGroupDescriptor(), dbParameterGroupProbe(api), dbParameterGroupLookupProbe(api))
}
