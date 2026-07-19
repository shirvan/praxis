package acmcert

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulCertificateAPI struct {
	mu                               sync.Mutex
	certificates                     map[string]ObservedState
	creates, reads, updates, deletes int
	failCreateResponseOnce           bool
}

func newStatefulCertificateAPI() *statefulCertificateAPI {
	return &statefulCertificateAPI{certificates: map[string]ObservedState{}}
}

func (f *statefulCertificateAPI) RequestCertificate(_ context.Context, spec ACMCertificateSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for arn := range f.certificates {
		observed := f.certificates[arn]
		if observed.Tags["praxis:managed-key"] == spec.ManagedKey {
			return arn, nil
		}
	}
	f.creates++
	arn := "arn:aws:acm:us-east-1:123456789012:certificate/" + spec.ManagedKey
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags["praxis:managed-key"] = spec.ManagedKey
	observed := ObservedState{
		CertificateArn: arn, DomainName: spec.DomainName,
		SubjectAlternativeNames: append([]string(nil), spec.SubjectAlternativeNames...),
		ValidationMethod:        spec.ValidationMethod, KeyAlgorithm: spec.KeyAlgorithm,
		CertificateAuthorityArn: spec.CertificateAuthorityArn, Status: "PENDING_VALIDATION",
		Options: CertificateOptions{CertificateTransparencyLoggingPreference: normalizeTransparencyPreference(spec.Options)},
		Tags:    tags,
	}
	f.certificates[arn] = normalizeObservedState(observed)
	if f.failCreateResponseOnce {
		f.failCreateResponseOnce = false
		return "", errors.New("timeout after RequestCertificate response was lost")
	}
	return arn, nil
}

func (f *statefulCertificateAPI) DescribeCertificate(_ context.Context, arn string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, ok := f.certificates[arn]
	if !ok {
		return ObservedState{}, &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "not found"}
	}
	return cloneCertificate(observed), nil
}

func (f *statefulCertificateAPI) UpdateCertificateOptions(_ context.Context, arn string, options *CertificateOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.certificates[arn]
	f.updates++
	observed.Options.CertificateTransparencyLoggingPreference = normalizeTransparencyPreference(options)
	f.certificates[arn] = observed
	return nil
}

func (f *statefulCertificateAPI) UpdateTags(_ context.Context, arn string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.certificates[arn]
	f.updates++
	managedKey := observed.Tags["praxis:managed-key"]
	observed.Tags = maps.Clone(tags)
	if observed.Tags == nil {
		observed.Tags = map[string]string{}
	}
	observed.Tags["praxis:managed-key"] = managedKey
	f.certificates[arn] = observed
	return nil
}

func (f *statefulCertificateAPI) DeleteCertificate(_ context.Context, arn string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.certificates[arn]; !ok {
		return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "not found"}
	}
	f.deletes++
	delete(f.certificates, arn)
	return nil
}

func (f *statefulCertificateAPI) FindByManagedKey(_ context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	for arn := range f.certificates {
		observed := f.certificates[arn]
		if observed.Tags["praxis:managed-key"] == managedKey {
			return arn, nil
		}
	}
	return "", nil
}

func (f *statefulCertificateAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.certificates[observed.CertificateArn] = normalizeObservedState(observed)
}

func (f *statefulCertificateAPI) remove(arn string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.certificates, arn)
}

func (f *statefulCertificateAPI) mutate(arn string, fn func(*ObservedState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.certificates[arn]
	fn(&observed)
	f.certificates[arn] = observed
}

func (f *statefulCertificateAPI) get(arn string) ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneCertificate(f.certificates[arn])
}

func (f *statefulCertificateAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func cloneCertificate(observed ObservedState) ObservedState {
	observed.Tags = maps.Clone(observed.Tags)
	observed.SubjectAlternativeNames = append([]string(nil), observed.SubjectAlternativeNames...)
	observed.DNSValidationRecords = append([]DNSValidationRecord(nil), observed.DNSValidationRecords...)
	return observed
}

type certificateDriftSink struct{}

func (certificateDriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }
func (certificateDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error {
	return nil
}

func setupGenericCertificate(t *testing.T, api CertificateAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericACMCertificateDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(aws.Config) CertificateAPI { return api })
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(certificateDriftSink{})).Ingress()
}

func certificateSpec(domain string) ACMCertificateSpec {
	return ACMCertificateSpec{Account: "test", Region: "us-east-1", DomainName: domain, ValidationMethod: "DNS", KeyAlgorithm: "RSA_2048", Tags: map[string]string{"env": "test"}}
}

func provisionCertificate(t *testing.T, client *ingress.Client, key string, spec ACMCertificateSpec) ACMCertificateOutputs {
	t.Helper()
	outputs, err := ingress.Object[types.ProvisionRequest, ACMCertificateOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	return outputs
}

func TestGenericCertificateCoreLifecycle(t *testing.T) {
	api := newStatefulCertificateAPI()
	client := setupGenericCertificate(t, api)
	spec := certificateSpec("core.example.com")
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[ACMCertificateSpec, ACMCertificateOutputs]{
		Client: client, ServiceName: ServiceName, Key: "core-cert", Spec: spec, Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, got ACMCertificateSpec) {
			assert.Equal(t, "core-cert", got.ManagedKey)
			assert.Equal(t, spec.DomainName, got.DomainName)
			assert.Equal(t, "ENABLED", got.Options.CertificateTransparencyLoggingPreference)
		},
	})
}

func TestGenericCertificateObservedImportLifecycle(t *testing.T) {
	api := newStatefulCertificateAPI()
	arn := "arn:aws:acm:us-east-1:123456789012:certificate/imported"
	api.seed(ObservedState{CertificateArn: arn, DomainName: "imported.example.com", ValidationMethod: "DNS", KeyAlgorithm: "RSA_2048", Status: "ISSUED", Options: CertificateOptions{CertificateTransparencyLoggingPreference: "ENABLED"}, Tags: map[string]string{"env": "prod"}})
	client := setupGenericCertificate(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[ACMCertificateOutputs]{Client: client, ServiceName: ServiceName, Key: "imported-cert", Ref: types.ImportRef{ResourceID: arn, Account: "test"}, Snapshot: api.snapshot})
}

func TestGenericCertificateAmbiguousCreateRecoversExactlyOnce(t *testing.T) {
	api := newStatefulCertificateAPI()
	api.failCreateResponseOnce = true
	client := setupGenericCertificate(t, api)
	outputs := provisionCertificate(t, client, "ambiguous-cert", certificateSpec("ambiguous.example.com"))
	assert.NotEmpty(t, outputs.CertificateArn)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericCertificateRejectsImmutableIdentityChange(t *testing.T) {
	api := newStatefulCertificateAPI()
	client := setupGenericCertificate(t, api)
	key := "immutable-cert"
	provisionCertificate(t, client, key, certificateSpec("old.example.com"))
	_, err := ingress.Object[types.ProvisionRequest, ACMCertificateOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, certificateSpec("new.example.com")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity is immutable")
}

func TestGenericCertificateConvergesMutableDrift(t *testing.T) {
	api := newStatefulCertificateAPI()
	client := setupGenericCertificate(t, api)
	key := "drift-cert"
	outputs := provisionCertificate(t, client, key, certificateSpec("drift.example.com"))
	api.mutate(outputs.CertificateArn, func(observed *ObservedState) {
		observed.Options.CertificateTransparencyLoggingPreference = "DISABLED"
		observed.Tags["env"] = "stale"
	})
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	observed := api.get(outputs.CertificateArn)
	assert.Equal(t, "ENABLED", observed.Options.CertificateTransparencyLoggingPreference)
	assert.Equal(t, "test", observed.Tags["env"])
}

func TestGenericCertificateExternalDeleteRequiresReplacement(t *testing.T) {
	api := newStatefulCertificateAPI()
	client := setupGenericCertificate(t, api)
	key := "gone-cert"
	outputs := provisionCertificate(t, client, key, certificateSpec("gone.example.com"))
	api.remove(outputs.CertificateArn)
	before := api.snapshot()
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}

func TestGenericCertificateInvalidInputIsTerminal(t *testing.T) {
	api := newStatefulCertificateAPI()
	client := setupGenericCertificate(t, api)
	_, err := ingress.Object[types.ProvisionRequest, ACMCertificateOutputs](client, ServiceName, "invalid-cert", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, ACMCertificateSpec{Account: "test"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "domainName is required")
}
