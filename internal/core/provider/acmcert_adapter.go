// ACMCertificate provider adapter — descriptor for the GenericAdapter.
//
// Key scope: region-scoped.
// Key parts: region + certificate name.
// ACM certificates are region-scoped; the key combines the AWS region and the
// certificate name (the metadata.name).
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/acmcert"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

// ACMCertificateAdapter is the descriptor-driven adapter for ACMCertificate,
// extended with an import-key resolver that maps a certificate ARN to the
// certificate's domain name when a static planning API is available.
type ACMCertificateAdapter struct {
	*GenericAdapter[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs, acmcert.ObservedState]
	staticPlanningAPI acmcert.CertificateAPI
}

func acmCertificateDescriptor() GenericDescriptor[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs, acmcert.ObservedState] {
	return GenericDescriptor[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs, acmcert.ObservedState]{
		Kind:  acmcert.ServiceName,
		Scope: KeyScopeRegion,

		DecodeSpec: func(rawSpec json.RawMessage, metadataName string) (acmcert.ACMCertificateSpec, error) {
			var spec acmcert.ACMCertificateSpec
			if err := json.Unmarshal(rawSpec, &spec); err != nil {
				return acmcert.ACMCertificateSpec{}, fmt.Errorf("decode ACMCertificate spec: %w", err)
			}
			name := strings.TrimSpace(metadataName)
			if name == "" {
				return acmcert.ACMCertificateSpec{}, fmt.Errorf("ACMCertificate metadata.name is required")
			}
			if strings.TrimSpace(spec.Region) == "" {
				return acmcert.ACMCertificateSpec{}, fmt.Errorf("ACMCertificate spec.region is required")
			}
			if strings.TrimSpace(spec.DomainName) == "" {
				return acmcert.ACMCertificateSpec{}, fmt.Errorf("ACMCertificate spec.domainName is required")
			}
			if spec.Tags == nil {
				spec.Tags = make(map[string]string)
			}
			if spec.Tags["Name"] == "" {
				spec.Tags["Name"] = name
			}
			if spec.ValidationMethod == "" {
				spec.ValidationMethod = "DNS"
			}
			if spec.KeyAlgorithm == "" {
				spec.KeyAlgorithm = "RSA_2048"
			}
			if spec.Options == nil {
				spec.Options = &acmcert.CertificateOptions{CertificateTransparencyLoggingPreference: "ENABLED"}
			}
			spec.Account = ""
			return spec, nil
		},

		KeyFromSpec: func(spec acmcert.ACMCertificateSpec, metadataName string) (string, error) {
			if err := ValidateKeyPart("region", spec.Region); err != nil {
				return "", err
			}
			name := strings.TrimSpace(metadataName)
			if err := ValidateKeyPart("ACM certificate name", name); err != nil {
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

		PrepareSpec: func(spec acmcert.ACMCertificateSpec, key, account string) acmcert.ACMCertificateSpec {
			spec.Account = account
			spec.ManagedKey = key
			return spec
		},

		NormalizeOutputs: func(out acmcert.ACMCertificateOutputs) map[string]any {
			result := map[string]any{
				"certificateArn": out.CertificateArn,
				"domainName":     out.DomainName,
				"status":         out.Status,
			}
			if out.NotBefore != "" {
				result["notBefore"] = out.NotBefore
			}
			if out.NotAfter != "" {
				result["notAfter"] = out.NotAfter
			}
			if len(out.DNSValidationRecords) > 0 {
				records := make([]map[string]any, 0, len(out.DNSValidationRecords))
				for _, record := range out.DNSValidationRecords {
					records = append(records, map[string]any{
						"domainName":          record.DomainName,
						"resourceRecordName":  record.ResourceRecordName,
						"resourceRecordType":  record.ResourceRecordType,
						"resourceRecordValue": record.ResourceRecordValue,
					})
				}
				result["dnsValidationRecords"] = records
			}
			return result
		},

		PlanIdentity: storedPlanIdentity[acmcert.ACMCertificateSpec](func(out acmcert.ACMCertificateOutputs) string { return out.CertificateArn }),

		NewPlanProbe: func(cfg aws.Config) PlanProbeFunc[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs, acmcert.ObservedState] {
			return acmCertificateProbe(acmcert.NewCertificateAPI(awsclient.NewACMClient(cfg)))
		},

		DiffFields: func(desired acmcert.ACMCertificateSpec, observed acmcert.ObservedState, _ acmcert.ACMCertificateOutputs) []types.FieldDiff {
			rawDiffs := acmcert.ComputeFieldDiffs(desired, observed)
			fields := make([]types.FieldDiff, 0, len(rawDiffs))
			for _, diff := range rawDiffs {
				fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
			}
			return fields
		},
	}
}

// acmCertificateProbe adapts the driver API to the generic plan probe shape.
func acmCertificateProbe(api acmcert.CertificateAPI) PlanProbeFunc[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs, acmcert.ObservedState] {
	return func(runCtx restate.RunContext, input PlanProbeInput[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs]) (acmcert.ObservedState, bool, error) {
		certificateArn := input.Identity
		obs, err := api.DescribeCertificate(runCtx, certificateArn)
		if err != nil {
			if acmcert.IsNotFound(err) {
				return acmcert.ObservedState{}, false, nil
			}
			return acmcert.ObservedState{}, false, err
		}
		return obs, true, nil
	}
}

// NewACMCertificateAdapterWithAuth builds the production adapter; plan-time
// credentials are resolved through the Auth Service.
func NewACMCertificateAdapterWithAuth(auth authservice.AuthClient) *ACMCertificateAdapter {
	return &ACMCertificateAdapter{GenericAdapter: NewGenericAdapter(acmCertificateDescriptor(), auth)}
}

// NewACMCertificateAdapterWithAPI builds an adapter with a fixed planning API.
// Used by tests.
func NewACMCertificateAdapterWithAPI(api acmcert.CertificateAPI) *ACMCertificateAdapter {
	return &ACMCertificateAdapter{
		GenericAdapter:    NewGenericAdapterWithProbe(acmCertificateDescriptor(), acmCertificateProbe(api)),
		staticPlanningAPI: api,
	}
}

// BuildImportKey derives the canonical Restate object key for importing an
// existing ACMCertificate resource. When the resource ID is a certificate ARN
// and a static planning API is available, the certificate's domain name is
// resolved and used as the key part instead of the raw ARN.
func (a *ACMCertificateAdapter) BuildImportKey(region, resourceID string) (string, error) {
	if err := ValidateKeyPart("region", region); err != nil {
		return "", err
	}
	if err := ValidateKeyPart("resource ID", resourceID); err != nil {
		return "", err
	}
	if a.staticPlanningAPI != nil && strings.Contains(resourceID, ":certificate/") {
		obs, err := a.staticPlanningAPI.DescribeCertificate(context.Background(), resourceID)
		if err == nil && strings.TrimSpace(obs.DomainName) != "" {
			return JoinKey(region, obs.DomainName), nil
		}
	}
	return JoinKey(region, resourceID), nil
}
