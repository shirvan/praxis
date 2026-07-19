package acmcert

import (
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers"
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/kernel"
	"github.com/shirvan/praxis/internal/infra/awsclient"
	"github.com/shirvan/praxis/pkg/types"
)

type genericCertificateOperations struct {
	auth       authservice.AuthClient
	apiFactory func(aws.Config) CertificateAPI
}

// NewGenericACMCertificateDriver binds ACM certificate provider behavior to
// the shared lifecycle kernel.
func NewGenericACMCertificateDriver(auth authservice.AuthClient) *kernel.Driver[ACMCertificateSpec, ACMCertificateOutputs, ObservedState] {
	return newGenericACMCertificateDriverWithFactory(auth, nil)
}

func newGenericACMCertificateDriverWithFactory(auth authservice.AuthClient, factory func(aws.Config) CertificateAPI) *kernel.Driver[ACMCertificateSpec, ACMCertificateOutputs, ObservedState] {
	if factory == nil {
		factory = func(cfg aws.Config) CertificateAPI { return NewCertificateAPI(awsclient.NewACMClient(cfg)) }
	}
	ops := &genericCertificateOperations{auth: auth, apiFactory: factory}
	return kernel.MustNew(kernel.Descriptor[ACMCertificateSpec, ACMCertificateOutputs, ObservedState]{
		ServiceName: ServiceName,
		Capabilities: kernel.Capabilities{
			Declared: true, Import: true, ObservedMode: true, Delete: true,
		},
		Operations: ops,
		Prepare: func(ctx restate.ObjectContext, spec ACMCertificateSpec) (ACMCertificateSpec, error) {
			_, region, err := ops.apiForAccount(ctx, spec.Account)
			if err != nil {
				return ACMCertificateSpec{}, drivers.ClassifyCredentialError(err)
			}
			spec = applyDefaults(spec)
			if strings.TrimSpace(spec.Region) == "" {
				spec.Region = region
			}
			spec.ManagedKey = restate.Key(ctx)
			spec.Tags = drivers.FilterPraxisTags(spec.Tags)
			return spec, nil
		},
		Validate: validateSpec,
		DesiredFromObserved: func(ref types.ImportRef, observed ObservedState) ACMCertificateSpec {
			spec := specFromObserved(observed)
			spec.Account = ref.Account
			return spec
		},
		OutputsFromObserved: func(observed ObservedState, seed ACMCertificateOutputs) ACMCertificateOutputs {
			outputs := outputsFromObserved(observed)
			if outputs.CertificateArn == "" {
				outputs.CertificateArn = seed.CertificateArn
			}
			return outputs
		},
		FieldDiffs: ComputeFieldDiffs,
		HasDrift:   hasAnyDrift,
	})
}

func (o *genericCertificateOperations) Observe(ctx restate.ObjectContext, desired ACMCertificateSpec, outputs ACMCertificateOutputs) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	certificateArn := strings.TrimSpace(outputs.CertificateArn)
	if certificateArn == "" && desired.ManagedKey != "" {
		certificateArn, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
			return api.FindByManagedKey(rc, desired.ManagedKey)
		}, classifyCertificateFind)
		if err != nil || certificateArn == "" {
			return kernel.Observation[ObservedState]{}, err
		}
	}
	return observeCertificate(ctx, api, certificateArn)
}

func (o *genericCertificateOperations) Create(ctx restate.ObjectContext, desired ACMCertificateSpec) (kernel.CreateResult[ACMCertificateOutputs], error) {
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return kernel.CreateResult[ACMCertificateOutputs]{}, drivers.ClassifyCredentialError(err)
	}
	certificateArn, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (string, error) {
		return api.RequestCertificate(rc, desired)
	}, classifyCertificateMutation)
	return kernel.CreateResult[ACMCertificateOutputs]{
		SeedOutputs: ACMCertificateOutputs{CertificateArn: certificateArn},
	}, err
}

func (o *genericCertificateOperations) Converge(ctx restate.ObjectContext, desired ACMCertificateSpec, observed ObservedState, currentOutputs ACMCertificateOutputs) (ACMCertificateOutputs, error) {
	if err := validateImmutableFields(desired, observed); err != nil {
		return currentOutputs, restate.TerminalError(fmt.Errorf("ACM certificate identity is immutable: %w; delete and reprovision", err), 409)
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return currentOutputs, drivers.ClassifyCredentialError(err)
	}
	if normalizeTransparencyPreference(desired.Options) != normalizeTransparencyPreference(&observed.Options) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateCertificateOptions(rc, observed.CertificateArn, desired.Options)
		}, classifyCertificateMutation); err != nil {
			return currentOutputs, fmt.Errorf("update certificate options: %w", err)
		}
	}
	if !drivers.TagsMatch(desired.Tags, observed.Tags) {
		if _, err := drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
			return restate.Void{}, api.UpdateTags(rc, observed.CertificateArn, desired.Tags)
		}, classifyCertificateMutation); err != nil {
			return currentOutputs, fmt.Errorf("update certificate tags: %w", err)
		}
	}
	return currentOutputs, nil
}

func (o *genericCertificateOperations) Delete(ctx restate.ObjectContext, desired ACMCertificateSpec, outputs ACMCertificateOutputs) error {
	certificateArn := strings.TrimSpace(outputs.CertificateArn)
	if certificateArn == "" {
		return nil
	}
	api, _, err := o.apiForAccount(ctx, desired.Account)
	if err != nil {
		return drivers.ClassifyCredentialError(err)
	}
	_, err = drivers.RunAWS(ctx, func(rc restate.RunContext) (restate.Void, error) {
		deleteErr := api.DeleteCertificate(rc, certificateArn)
		if IsNotFound(deleteErr) {
			deleteErr = nil
		}
		return restate.Void{}, deleteErr
	}, classifyCertificateMutation)
	return err
}

func (o *genericCertificateOperations) Import(ctx restate.ObjectContext, ref types.ImportRef) (kernel.Observation[ObservedState], error) {
	api, _, err := o.apiForAccount(ctx, ref.Account)
	if err != nil {
		return kernel.Observation[ObservedState]{}, drivers.ClassifyCredentialError(err)
	}
	return observeCertificate(ctx, api, strings.TrimSpace(ref.ResourceID))
}

func observeCertificate(ctx restate.ObjectContext, api CertificateAPI, certificateArn string) (kernel.Observation[ObservedState], error) {
	if certificateArn == "" {
		return kernel.Observation[ObservedState]{}, nil
	}
	return drivers.RunAWS(ctx, func(rc restate.RunContext) (kernel.Observation[ObservedState], error) {
		observed, err := api.DescribeCertificate(rc, certificateArn)
		if IsNotFound(err) {
			return kernel.Observation[ObservedState]{}, nil
		}
		if err != nil {
			return kernel.Observation[ObservedState]{}, err
		}
		return kernel.Observation[ObservedState]{Exists: true, Value: normalizeObservedState(observed)}, nil
	}, classifyCertificateObserve)
}

func (o *genericCertificateOperations) apiForAccount(ctx restate.Context, account string) (CertificateAPI, string, error) {
	if o == nil || o.auth == nil || o.apiFactory == nil {
		return nil, "", fmt.Errorf("ACMCertificate driver is not configured with an auth registry")
	}
	cfg, err := o.auth.GetCredentials(ctx, account)
	if err != nil {
		return nil, "", fmt.Errorf("resolve ACM account %q: %w", account, err)
	}
	return o.apiFactory(cfg), cfg.Region, nil
}

func validateSpec(spec ACMCertificateSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(spec.DomainName) == "" {
		return fmt.Errorf("domainName is required")
	}
	switch spec.ValidationMethod {
	case "DNS", "EMAIL":
	default:
		return fmt.Errorf("validationMethod must be DNS or EMAIL")
	}
	switch spec.KeyAlgorithm {
	case "RSA_1024", "RSA_2048", "RSA_3072", "RSA_4096", "EC_prime256v1", "EC_secp384r1", "EC_secp521r1":
	default:
		return fmt.Errorf("unsupported keyAlgorithm %q", spec.KeyAlgorithm)
	}
	switch normalizeTransparencyPreference(spec.Options) {
	case "ENABLED", "DISABLED":
	default:
		return fmt.Errorf("certificate transparency logging preference must be ENABLED or DISABLED")
	}
	return nil
}

func hasAnyDrift(desired ACMCertificateSpec, observed ObservedState) bool {
	return HasDrift(desired, observed) || validateImmutableFields(desired, observed) != nil
}

func classifyCertificateObserve(err error) error {
	if err == nil || IsNotFound(err) {
		return err
	}
	if awserr.IsAccessDenied(err) {
		return restate.TerminalError(err, 403)
	}
	if IsInvalidArn(err) || IsInvalidDomain(err) || awserr.IsValidation(err) {
		return restate.TerminalError(err, 400)
	}
	return err
}

func classifyCertificateFind(err error) error {
	if err != nil && strings.Contains(err.Error(), "ownership corruption") {
		return restate.TerminalError(err, 409)
	}
	return classifyCertificateObserve(err)
}

func classifyCertificateMutation(err error) error {
	if err == nil {
		return nil
	}
	if IsConflict(err) || IsInvalidState(err) {
		return restate.TerminalError(err, 409)
	}
	if IsQuotaExceeded(err) {
		return restate.TerminalError(err, 503)
	}
	return classifyCertificateObserve(err)
}
