package ekscluster

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

type eksDriftSink struct{}

func (eksDriftSink) ServiceName() string                                            { return eventing.ResourceEventBridgeServiceName }
func (eksDriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

type statefulEKSAPI struct {
	mu sync.Mutex

	items   map[string]ObservedState
	creates int
	reads   int
	updates int
	deletes int

	createStatus string
	createErrors []error
}

func newStatefulEKSAPI() *statefulEKSAPI {
	return &statefulEKSAPI{items: map[string]ObservedState{}, createStatus: "ACTIVE"}
}

func (f *statefulEKSAPI) CreateCluster(_ context.Context, spec EKSClusterSpec) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.items[spec.Name]; exists {
		return ObservedState{}, &smithy.GenericAPIError{Code: "ResourceInUseException", Message: "already exists"}
	}
	f.creates++
	observed := observedEKS(spec, f.createStatus)
	f.items[spec.Name] = observed
	if len(f.createErrors) > 0 {
		err := f.createErrors[0]
		f.createErrors = f.createErrors[1:]
		return ObservedState{}, err
	}
	return cloneEKSObserved(observed), nil
}

func (f *statefulEKSAPI) DescribeCluster(_ context.Context, name string) (ObservedState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	observed, exists := f.items[name]
	return cloneEKSObserved(observed), exists, nil
}

func (f *statefulEKSAPI) UpdateClusterConfig(_ context.Context, spec EKSClusterSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, exists := f.items[spec.Name]
	if !exists {
		return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "missing"}
	}
	observed.EndpointPublicAccess = spec.EndpointPublicAccess
	observed.EndpointPrivateAccess = spec.EndpointPrivateAccess
	observed.PublicAccessCidrs = append([]string{}, spec.PublicAccessCidrs...)
	observed.EnabledLoggingTypes = append([]string{}, spec.EnabledLoggingTypes...)
	f.items[spec.Name] = observed
	f.updates++
	return nil
}

func (f *statefulEKSAPI) UpdateClusterLogging(_ context.Context, name string, enabled []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, exists := f.items[name]
	if !exists {
		return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "missing"}
	}
	observed.EnabledLoggingTypes = append([]string{}, enabled...)
	f.items[name] = observed
	f.updates++
	return nil
}

func (f *statefulEKSAPI) UpdateClusterVersion(_ context.Context, name, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed, exists := f.items[name]
	if !exists {
		return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "missing"}
	}
	observed.Version = version
	f.items[name] = observed
	f.updates++
	return nil
}

func (f *statefulEKSAPI) DeleteCluster(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.items[name]; !exists {
		return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "missing"}
	}
	delete(f.items, name)
	f.deletes++
	return nil
}

func (f *statefulEKSAPI) TagResource(_ context.Context, arn string, tags map[string]string) error {
	return f.updateTags(arn, func(observed *ObservedState) { maps.Copy(observed.Tags, tags) })
}

func (f *statefulEKSAPI) UntagResource(_ context.Context, arn string, keys []string) error {
	return f.updateTags(arn, func(observed *ObservedState) {
		for _, key := range keys {
			delete(observed.Tags, key)
		}
	})
}

func (f *statefulEKSAPI) updateTags(arn string, mutate func(*ObservedState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.items {
		observed := f.items[name]
		if observed.ARN == arn {
			mutate(&observed)
			f.items[name] = observed
			f.updates++
			return nil
		}
	}
	return &smithy.GenericAPIError{Code: "ResourceNotFoundException", Message: "missing"}
}

func (f *statefulEKSAPI) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulEKSAPI) seed(observed ObservedState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[observed.Name] = cloneEKSObserved(observed)
}

func (f *statefulEKSAPI) setStatus(name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	observed := f.items[name]
	observed.Status = status
	f.items[name] = observed
}

func (f *statefulEKSAPI) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, name)
}

func observedEKS(spec EKSClusterSpec, status string) ObservedState {
	return ObservedState{
		ARN:  "arn:aws:eks:us-east-1:123456789012:cluster/" + spec.Name,
		Name: spec.Name, Status: status, Version: spec.Version, PlatformVersion: "eks.1",
		RoleArn: spec.RoleArn, SubnetIds: append([]string{}, spec.SubnetIds...),
		SecurityGroupIds:     append([]string{}, spec.SecurityGroupIds...),
		EndpointPublicAccess: spec.EndpointPublicAccess, EndpointPrivateAccess: spec.EndpointPrivateAccess,
		PublicAccessCidrs:   append([]string{}, spec.PublicAccessCidrs...),
		EnabledLoggingTypes: append([]string{}, spec.EnabledLoggingTypes...),
		Tags:                managedTags(spec.Tags, spec.ManagedKey),
	}
}

func setupGenericEKS(t *testing.T, api EKSClusterAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := newGenericEKSClusterDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) EKSClusterAPI { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(eksDriftSink{})).Ingress()
}

func managedEKSSpec(name string) EKSClusterSpec {
	return EKSClusterSpec{
		Account: "test", Region: "us-east-1", Name: name,
		RoleArn: "arn:aws:iam::123456789012:role/eks", SubnetIds: []string{"subnet-a", "subnet-b"},
		Version: "1.30", EndpointPublicAccess: true, PublicAccessCidrs: []string{"10.0.0.0/8"},
		Tags: map[string]string{"env": "test"},
	}
}

func TestGenericEKSCoreLifecycle(t *testing.T) {
	api := newStatefulEKSAPI()
	client := setupGenericEKS(t, api)
	key := "us-east-1~generic-eks"
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[EKSClusterSpec, EKSClusterOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: managedEKSSpec("generic-eks"), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs EKSClusterSpec) { assert.Equal(t, key, inputs.ManagedKey) },
	})
}

func TestGenericEKSObservedImportLifecycle(t *testing.T) {
	api := newStatefulEKSAPI()
	spec := managedEKSSpec("existing-eks")
	spec.ManagedKey = ""
	api.seed(observedEKS(spec, "ACTIVE"))
	client := setupGenericEKS(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[EKSClusterOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~existing-eks",
		Ref: types.ImportRef{Account: "test", ResourceID: "existing-eks"}, Snapshot: api.snapshot,
	})
}

func TestGenericEKSPendingProgressesToReady(t *testing.T) {
	api := newStatefulEKSAPI()
	api.createStatus = "CREATING"
	client := setupGenericEKS(t, api)
	key := "us-east-1~pending-eks"
	outputs, err := ingress.Object[types.ProvisionRequest, EKSClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedEKSSpec("pending-eks")))
	require.NoError(t, err)
	assert.Equal(t, "CREATING", outputs.Status)
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusPending, status.Status)

	api.setStatus("pending-eks", "ACTIVE")
	_, err = ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	status, err = ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
}

func TestGenericEKSFailedReadinessBecomesError(t *testing.T) {
	api := newStatefulEKSAPI()
	api.createStatus = "FAILED"
	client := setupGenericEKS(t, api)
	key := "us-east-1~failed-eks"
	_, err := ingress.Object[types.ProvisionRequest, EKSClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedEKSSpec("failed-eks")))
	require.Error(t, err)
	status, statusErr := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, statusErr)
	assert.Equal(t, types.StatusError, status.Status)
	assert.Contains(t, status.Error, "FAILED")
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericEKSRecoversAmbiguousCreateWithoutDuplicate(t *testing.T) {
	api := newStatefulEKSAPI()
	api.createErrors = []error{errors.New("create response lost")}
	client := setupGenericEKS(t, api)
	outputs, err := ingress.Object[types.ProvisionRequest, EKSClusterOutputs](client, ServiceName, "us-east-1~ambiguous-eks", "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedEKSSpec("ambiguous-eks")))
	require.NoError(t, err)
	assert.Equal(t, "ambiguous-eks", outputs.Name)
	assert.Equal(t, 1, api.snapshot().Creates)
}

func TestGenericEKSRejectsImmutablePlacement(t *testing.T) {
	api := newStatefulEKSAPI()
	client := setupGenericEKS(t, api)
	key := "us-east-1~immutable-eks"
	spec := managedEKSSpec("immutable-eks")
	_, err := ingress.Object[types.ProvisionRequest, EKSClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	spec.RoleArn = "arn:aws:iam::123456789012:role/other"
	_, err = ingress.Object[types.ProvisionRequest, EKSClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "roleArn is immutable")
	stored, storedErr := ingress.Object[restate.Void, EKSClusterSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, storedErr)
	assert.Equal(t, "arn:aws:iam::123456789012:role/eks", stored.RoleArn)
}

func TestGenericEKSProvisionChangeRejectsEveryImmutableField(t *testing.T) {
	previous := managedEKSSpec("identity-eks")
	cases := map[string]func(*EKSClusterSpec){
		"account":         func(spec *EKSClusterSpec) { spec.Account = "other" },
		"region":          func(spec *EKSClusterSpec) { spec.Region = "us-west-2" },
		"name":            func(spec *EKSClusterSpec) { spec.Name = "other" },
		"role":            func(spec *EKSClusterSpec) { spec.RoleArn += "-other" },
		"subnets":         func(spec *EKSClusterSpec) { spec.SubnetIds = []string{"subnet-c", "subnet-d"} },
		"security-groups": func(spec *EKSClusterSpec) { spec.SecurityGroupIds = []string{"sg-other"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			next := previous
			mutate(&next)
			_, err := (&genericOperations{}).ConvergeProvisionChange(nil, previous, next, ObservedState{}, EKSClusterOutputs{})
			require.Error(t, err)
			assert.EqualValues(t, 409, restate.ErrorCode(err))
		})
	}
}

func TestGenericEKSExternalDeleteRequiresExplicitProvision(t *testing.T) {
	api := newStatefulEKSAPI()
	client := setupGenericEKS(t, api)
	key := "us-east-1~external-eks"
	_, err := ingress.Object[types.ProvisionRequest, EKSClusterOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, managedEKSSpec("external-eks")))
	require.NoError(t, err)
	before := api.snapshot()
	api.remove("external-eks")
	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Equal(t, before.Creates, api.snapshot().Creates)
}
