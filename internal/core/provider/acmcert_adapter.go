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

type ACMCertificateAdapter struct {
	auth              authservice.AuthClient
	staticPlanningAPI acmcert.CertificateAPI
	apiFactory        func(aws.Config) acmcert.CertificateAPI
}

func NewACMCertificateAdapterWithAuth(auth authservice.AuthClient) *ACMCertificateAdapter {
	return &ACMCertificateAdapter{
		auth: auth,
		apiFactory: func(cfg aws.Config) acmcert.CertificateAPI {
			return acmcert.NewCertificateAPI(awsclient.NewACMClient(cfg))
		},
	}
}

func NewACMCertificateAdapterWithAPI(api acmcert.CertificateAPI) *ACMCertificateAdapter {
	return &ACMCertificateAdapter{staticPlanningAPI: api}
}

func (a *ACMCertificateAdapter) Kind() string        { return acmcert.ServiceName }
func (a *ACMCertificateAdapter) ServiceName() string { return acmcert.ServiceName }
func (a *ACMCertificateAdapter) Scope() KeyScope     { return KeyScopeRegion }

func (a *ACMCertificateAdapter) BuildKey(resourceDoc json.RawMessage) (string, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return "", err
	}
	spec, err := a.decodeSpec(doc)
	if err != nil {
		return "", err
	}
	if err := ValidateKeyPart("region", spec.Region); err != nil {
		return "", err
	}
	name := strings.TrimSpace(doc.Metadata.Name)
	if err := ValidateKeyPart("ACM certificate name", name); err != nil {
		return "", err
	}
	return JoinKey(spec.Region, name), nil
}

func (a *ACMCertificateAdapter) DecodeSpec(resourceDoc json.RawMessage) (any, error) {
	doc, err := decodeResourceDocument(resourceDoc)
	if err != nil {
		return nil, err
	}
	return a.decodeSpec(doc)
}

func (a *ACMCertificateAdapter) Provision(ctx restate.Context, key string, account string, spec any) (ProvisionInvocation, error) {
	typedSpec, err := castSpec[acmcert.ACMCertificateSpec](spec)
	if err != nil {
		return nil, err
	}
	typedSpec.Account = account
	typedSpec.ManagedKey = key
	fut := restate.WithRequestType[acmcert.ACMCertificateSpec, acmcert.ACMCertificateOutputs](
		restate.Object[acmcert.ACMCertificateOutputs](ctx, a.ServiceName(), key, "Provision"),
	).RequestFuture(typedSpec)
	return &provisionHandle[acmcert.ACMCertificateOutputs]{id: fut.GetInvocationId(), raw: fut, normalize: a.NormalizeOutputs}, nil
}

func (a *ACMCertificateAdapter) Delete(ctx restate.Context, key string) (DeleteInvocation, error) {
	fut := restate.WithRequestType[restate.Void, restate.Void](
		restate.Object[restate.Void](ctx, a.ServiceName(), key, "Delete"),
	).RequestFuture(restate.Void{})
	return &deleteHandle{id: fut.GetInvocationId(), raw: fut}, nil
}

func (a *ACMCertificateAdapter) NormalizeOutputs(raw any) (map[string]any, error) {
	out, err := castOutput[acmcert.ACMCertificateOutputs](raw)
	if err != nil {
		return nil, err
	}
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
	return result, nil
}

func (a *ACMCertificateAdapter) Plan(ctx restate.Context, key string, account string, desiredSpec any) (types.DiffOperation, []types.FieldDiff, error) {
	desired, err := castSpec[acmcert.ACMCertificateSpec](desiredSpec)
	if err != nil {
		return "", nil, err
	}
	outputs, getErr := restate.Object[acmcert.ACMCertificateOutputs](ctx, a.ServiceName(), key, "GetOutputs").Request(restate.Void{})
	if getErr != nil {
		return "", nil, fmt.Errorf("ACMCertificate Plan: failed to read outputs for key %q: %w", key, getErr)
	}
	if outputs.CertificateArn == "" {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	planningAPI, err := a.planningAPI(ctx, account)
	if err != nil {
		return "", nil, err
	}
	type describePlanResult struct {
		State acmcert.ObservedState
		Found bool
	}
	result, err := restate.Run(ctx, func(runCtx restate.RunContext) (describePlanResult, error) {
		obs, descErr := planningAPI.DescribeCertificate(runCtx, outputs.CertificateArn)
		if descErr != nil {
			if acmcert.IsNotFound(descErr) {
				return describePlanResult{Found: false}, nil
			}
			return describePlanResult{}, restate.TerminalError(descErr, 500)
		}
		return describePlanResult{State: obs, Found: true}, nil
	})
	if err != nil {
		return "", nil, err
	}
	if !result.Found {
		fields, fieldErr := createFieldDiffsFromSpec(desired)
		if fieldErr != nil {
			return "", nil, fieldErr
		}
		return types.OpCreate, fields, nil
	}
	rawDiffs := acmcert.ComputeFieldDiffs(desired, result.State)
	if len(rawDiffs) == 0 {
		return types.OpNoOp, nil, nil
	}
	fields := make([]types.FieldDiff, 0, len(rawDiffs))
	for _, diff := range rawDiffs {
		fields = append(fields, types.FieldDiff{Path: diff.Path, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	return types.OpUpdate, fields, nil
}

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

func (a *ACMCertificateAdapter) Import(ctx restate.Context, key string, account string, ref types.ImportRef) (types.ResourceStatus, map[string]any, error) {
	ref.Account = account
	output, err := restate.WithRequestType[types.ImportRef, acmcert.ACMCertificateOutputs](
		restate.Object[acmcert.ACMCertificateOutputs](ctx, a.ServiceName(), key, "Import"),
	).Request(ref)
	if err != nil {
		return "", nil, err
	}
	outputs, err := a.NormalizeOutputs(output)
	if err != nil {
		return "", nil, err
	}
	return types.StatusReady, outputs, nil
}

func (a *ACMCertificateAdapter) decodeSpec(doc resourceDocument) (acmcert.ACMCertificateSpec, error) {
	var spec acmcert.ACMCertificateSpec
	if err := json.Unmarshal(doc.Spec, &spec); err != nil {
		return acmcert.ACMCertificateSpec{}, fmt.Errorf("decode ACMCertificate spec: %w", err)
	}
	name := strings.TrimSpace(doc.Metadata.Name)
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
}

func (a *ACMCertificateAdapter) planningAPI(ctx restate.Context, account string) (acmcert.CertificateAPI, error) {
	if a.staticPlanningAPI != nil {
		return a.staticPlanningAPI, nil
	}
	if a.auth == nil || a.apiFactory == nil {
		return nil, fmt.Errorf("ACMCertificate adapter planning API is not configured")
	}
	awsCfg, err := a.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("resolve ACMCertificate planning account %q: %w", account, err)
	}
	return a.apiFactory(awsCfg), nil
}
