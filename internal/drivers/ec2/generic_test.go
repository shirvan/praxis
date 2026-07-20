package ec2

import (
	"context"
	"maps"
	"sort"
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
	"github.com/shirvan/praxis/internal/drivers/awserr"
	"github.com/shirvan/praxis/internal/drivers/drivertest"
	"github.com/shirvan/praxis/internal/eventing"
	restatetest "github.com/shirvan/praxis/internal/restatetest"
	"github.com/shirvan/praxis/pkg/types"
)

type statefulEC2API struct {
	mu sync.Mutex

	observed   ObservedState
	managedKey string
	findErr    error

	creates int
	reads   int
	updates int
	deletes int
	waits   int

	waitFailures int
	clientTokens []string
}

type noOpEC2DriftSink struct{}

func (noOpEC2DriftSink) ServiceName() string { return eventing.ResourceEventBridgeServiceName }

func (noOpEC2DriftSink) ReportDrift(restate.Context, eventing.DriftReportRequest) error { return nil }

func (f *statefulEC2API) RunInstance(_ context.Context, spec EC2InstanceSpec, clientToken string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.clientTokens = append(f.clientTokens, clientToken)
	tags := maps.Clone(spec.Tags)
	if tags == nil {
		tags = map[string]string{}
	}
	tags["praxis:managed-key"] = spec.ManagedKey
	rootType := "gp3"
	rootSize := int32(20)
	rootEncrypted := true
	if spec.RootVolume != nil {
		if spec.RootVolume.VolumeType != "" {
			rootType = spec.RootVolume.VolumeType
		}
		if spec.RootVolume.SizeGiB != 0 {
			rootSize = spec.RootVolume.SizeGiB
		}
		rootEncrypted = spec.RootVolume.Encrypted
	}
	f.managedKey = spec.ManagedKey
	f.observed = ObservedState{
		InstanceId: "i-created", ImageId: spec.ImageId, InstanceType: spec.InstanceType,
		KeyName: spec.KeyName, SubnetId: spec.SubnetId, VpcId: "vpc-1",
		SecurityGroupIds:   append([]string(nil), spec.SecurityGroupIds...),
		IamInstanceProfile: instanceProfileName(spec.IamInstanceProfile), Monitoring: spec.Monitoring,
		State: "pending", PrivateIpAddress: "10.0.0.10", PrivateDnsName: "ip-10-0-0-10.internal",
		RootVolumeType: rootType, RootVolumeSizeGiB: rootSize, RootVolumeEncrypted: rootEncrypted,
		Tags: tags,
	}
	sort.Strings(f.observed.SecurityGroupIds)
	return f.observed.InstanceId, nil
}

func (f *statefulEC2API) DescribeInstance(_ context.Context, instanceID string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.observed.InstanceId == "" || f.observed.InstanceId != instanceID {
		return ObservedState{}, awserr.NotFound("instance " + instanceID + " not found")
	}
	return cloneObserved(f.observed), nil
}

func (f *statefulEC2API) TerminateInstance(_ context.Context, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observed.InstanceId == "" || f.observed.InstanceId != instanceID {
		return awserr.NotFound("instance " + instanceID + " not found")
	}
	f.deletes++
	f.observed = ObservedState{}
	f.managedKey = ""
	return nil
}

func (f *statefulEC2API) WaitUntilRunning(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	if f.waitFailures > 0 {
		f.waitFailures--
		return &smithy.GenericAPIError{Code: "RequestLimitExceeded", Message: "temporary EC2 control-plane throttle"}
	}
	f.observed.State = "running"
	return nil
}

func (f *statefulEC2API) ModifyInstanceType(_ context.Context, _ string, instanceType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.InstanceType = instanceType
	f.observed.State = "running"
	return nil
}

func (f *statefulEC2API) ModifySecurityGroups(_ context.Context, _ string, securityGroupIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.SecurityGroupIds = append([]string(nil), securityGroupIDs...)
	sort.Strings(f.observed.SecurityGroupIds)
	return nil
}

func (f *statefulEC2API) UpdateIAMInstanceProfile(_ context.Context, _ string, profile string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.IamInstanceProfile = instanceProfileName(profile)
	return nil
}

func (f *statefulEC2API) UpdateMonitoring(_ context.Context, _ string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	f.observed.Monitoring = enabled
	return nil
}

func (f *statefulEC2API) UpdateTags(_ context.Context, _ string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	managedKey := f.observed.Tags["praxis:managed-key"]
	f.observed.Tags = maps.Clone(tags)
	if f.observed.Tags == nil {
		f.observed.Tags = map[string]string{}
	}
	if managedKey != "" {
		f.observed.Tags["praxis:managed-key"] = managedKey
	}
	return nil
}

func (f *statefulEC2API) FindByManagedKey(_ context.Context, managedKey string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.findErr != nil {
		return "", f.findErr
	}
	if f.managedKey == managedKey && f.observed.InstanceId != "" && !isTerminating(f.observed.State) {
		return f.observed.InstanceId, nil
	}
	return "", nil
}

func (f *statefulEC2API) FindByTags(_ context.Context, tags map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.findErr != nil {
		return "", f.findErr
	}
	if f.observed.InstanceId == "" || isTerminating(f.observed.State) {
		return "", nil
	}
	for key, value := range tags {
		if f.observed.Tags[key] != value {
			return "", nil
		}
	}
	return f.observed.InstanceId, nil
}

func (f *statefulEC2API) snapshot() drivertest.ProviderSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return drivertest.ProviderSnapshot{Creates: f.creates, Reads: f.reads, Updates: f.updates, Deletes: f.deletes}
}

func (f *statefulEC2API) counters() (creates, waits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.waits
}

func (f *statefulEC2API) tokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.clientTokens...)
}

func (f *statefulEC2API) injectDrift() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.InstanceType = "m7i.large"
	f.observed.SecurityGroupIds = []string{"sg-drift"}
	f.observed.IamInstanceProfile = "drift-profile"
	f.observed.Monitoring = false
	f.observed.Tags = map[string]string{"env": "drift", "stale": "true", "praxis:managed-key": f.managedKey}
}

func (f *statefulEC2API) terminateExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed.State = "terminated"
}

func (f *statefulEC2API) current() ObservedState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneObserved(f.observed)
}

func cloneObserved(input ObservedState) ObservedState {
	input.SecurityGroupIds = append([]string(nil), input.SecurityGroupIds...)
	input.Tags = maps.Clone(input.Tags)
	return input
}

func setupGenericEC2(t *testing.T, api EC2API) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")
	driver := NewGenericEC2InstanceDriverWithFactory(
		authservice.NewLocalAuthClient(auth.LoadFromEnv()),
		func(aws.Config) EC2API { return api },
	)
	return restatetest.Start(t, restate.Reflect(driver), restate.Reflect(noOpEC2DriftSink{})).Ingress()
}

func genericEC2Spec(key string) EC2InstanceSpec {
	return EC2InstanceSpec{
		Account: "test", Region: "us-east-1", ImageId: "ami-12345678",
		InstanceType: "t3.micro", SubnetId: "subnet-1",
		SecurityGroupIds: []string{"sg-2", "sg-1"}, Monitoring: true,
		IamInstanceProfile: "desired-profile",
		RootVolume:         &RootVolumeSpec{}, Tags: map[string]string{"Name": "generic", "env": "test"},
		ManagedKey: key,
	}
}

func TestGenericEC2CoreLifecycleAndLateInitialization(t *testing.T) {
	api := &statefulEC2API{}
	key := "us-east-1~generic"
	client := setupGenericEC2(t, api)
	drivertest.RunCoreLifecycle(t, drivertest.CoreLifecycleFixture[EC2InstanceSpec, EC2InstanceOutputs]{
		Client: client, ServiceName: ServiceName, Key: key, Spec: genericEC2Spec(key), Snapshot: api.snapshot,
		AssertInputs: func(t *testing.T, inputs EC2InstanceSpec) {
			assert.Equal(t, key, inputs.ManagedKey)
			require.NotNil(t, inputs.RootVolume)
			assert.Equal(t, "gp3", inputs.RootVolume.VolumeType)
			assert.Equal(t, int32(20), inputs.RootVolume.SizeGiB)
		},
	})
}

func TestGenericEC2ObservedImportLifecycle(t *testing.T) {
	api := &statefulEC2API{
		managedKey: "old-owner",
		observed: ObservedState{
			InstanceId: "i-existing", ImageId: "ami-existing", InstanceType: "t3.small",
			SubnetId: "subnet-2", VpcId: "vpc-2", SecurityGroupIds: []string{"sg-existing"},
			Monitoring: true, State: "running", PrivateIpAddress: "10.0.1.4",
			Tags: map[string]string{"Name": "existing", "praxis:managed-key": "old-owner"},
		},
	}
	client := setupGenericEC2(t, api)
	drivertest.RunObservedImportLifecycle(t, drivertest.ObservedImportFixture[EC2InstanceOutputs]{
		Client: client, ServiceName: ServiceName, Key: "us-east-1~i-existing",
		Ref: types.ImportRef{ResourceID: "i-existing", Account: "test"}, Snapshot: api.snapshot,
	})
}

func TestGenericEC2RejectsCreateOnlyChangesAndRetainsInputs(t *testing.T) {
	api := &statefulEC2API{}
	key := "us-east-1~immutable-ec2"
	client := setupGenericEC2(t, api)
	_, err := ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericEC2Spec(key)))
	require.NoError(t, err)
	accepted, err := ingress.Object[restate.Void, EC2InstanceSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
	require.NoError(t, err)

	tests := []struct {
		field  string
		mutate func(*EC2InstanceSpec)
	}{
		{field: "imageId", mutate: func(s *EC2InstanceSpec) { s.ImageId = "ami-different" }},
		{field: "subnetId", mutate: func(s *EC2InstanceSpec) { s.SubnetId = "subnet-different" }},
		{field: "keyName", mutate: func(s *EC2InstanceSpec) { s.KeyName = "different-key" }},
		{field: "userData", mutate: func(s *EC2InstanceSpec) { s.UserData = "different-user-data" }},
		{field: "rootVolume", mutate: func(s *EC2InstanceSpec) {
			root := *s.RootVolume
			root.SizeGiB++
			s.RootVolume = &root
		}},
	}
	for _, tt := range tests {
		changed := accepted
		tt.mutate(&changed)
		_, err = ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, changed))
		require.Error(t, err)
		assert.Contains(t, err.Error(), tt.field+" is immutable")
		retained, getErr := ingress.Object[restate.Void, EC2InstanceSpec](client, ServiceName, key, "GetInputs").Request(t.Context(), restate.Void{})
		require.NoError(t, getErr)
		assert.Equal(t, accepted, retained)
	}
}

func TestGenericEC2RejectsImportOfTerminatedInstance(t *testing.T) {
	api := &statefulEC2API{observed: ObservedState{
		InstanceId: "i-terminated", State: "terminated",
	}}
	client := setupGenericEC2(t, api)
	_, err := ingress.Object[types.ImportRef, EC2InstanceOutputs](client, ServiceName, "us-east-1~i-terminated", "Import").Request(
		t.Context(), types.ImportRef{ResourceID: "i-terminated", Account: "test"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is terminated")
	assert.Zero(t, api.snapshot().Creates)
}

func TestGenericEC2RecoversInstanceByManagedKeyWithoutDuplicateCreate(t *testing.T) {
	key := "us-east-1~recovered"
	spec := genericEC2Spec(key)
	api := &statefulEC2API{managedKey: key, observed: ObservedState{
		InstanceId: "i-recovered", ImageId: spec.ImageId, InstanceType: spec.InstanceType,
		SubnetId: spec.SubnetId, VpcId: "vpc-1", SecurityGroupIds: []string{"sg-1", "sg-2"},
		Monitoring: spec.Monitoring, State: "running", RootVolumeType: "gp3", RootVolumeSizeGiB: 20,
		Tags: map[string]string{"Name": "generic", "env": "test", "praxis:managed-key": key},
	}}
	client := setupGenericEC2(t, api)
	outputs, err := ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, "i-recovered", outputs.InstanceId)
	creates, _ := api.counters()
	assert.Zero(t, creates, "observe-before-create must recover the existing managed instance")
}

func TestGenericEC2WaiterRetriesWithoutLaunchingSecondInstance(t *testing.T) {
	api := &statefulEC2API{waitFailures: 1}
	key := "us-east-1~wait-retry"
	client := setupGenericEC2(t, api)
	outputs, err := ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericEC2Spec(key)))
	require.NoError(t, err)
	assert.Equal(t, "running", outputs.State)
	creates, waits := api.counters()
	assert.Equal(t, 1, creates, "a retryable waiter failure must not replay RunInstances")
	assert.Equal(t, 2, waits, "the waiter itself should be retried by Restate")
	require.Len(t, api.tokens(), 1)
	assert.Len(t, api.tokens()[0], 64, "RunInstances must receive a bounded idempotency token")
}

func TestGenericEC2ManagedReconcileConvergesIndependentProviderDrift(t *testing.T) {
	api := &statefulEC2API{}
	key := "us-east-1~drift"
	spec := genericEC2Spec(key)
	client := setupGenericEC2(t, api)
	_, err := ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	api.injectDrift()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.Drift)
	assert.True(t, result.Correcting)
	current := api.current()
	assert.Equal(t, spec.InstanceType, current.InstanceType)
	assert.ElementsMatch(t, spec.SecurityGroupIds, current.SecurityGroupIds)
	assert.Equal(t, instanceProfileName(spec.IamInstanceProfile), current.IamInstanceProfile)
	assert.Equal(t, spec.Monitoring, current.Monitoring)
	assert.Equal(t, map[string]string{"Name": "generic", "env": "test", "praxis:managed-key": key}, current.Tags)
}

func TestGenericEC2ConvergesIAMInstanceProfileAddReplaceAndRemove(t *testing.T) {
	api := &statefulEC2API{}
	key := "us-east-1~profile-convergence"
	client := setupGenericEC2(t, api)
	spec := genericEC2Spec(key)
	spec.IamInstanceProfile = ""

	_, err := ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Empty(t, api.current().IamInstanceProfile)

	spec.IamInstanceProfile = "app-profile"
	_, err = ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, "app-profile", api.current().IamInstanceProfile)

	spec.IamInstanceProfile = "arn:aws:iam::123456789012:instance-profile/platform/replacement-profile"
	_, err = ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Equal(t, "replacement-profile", api.current().IamInstanceProfile)

	spec.IamInstanceProfile = ""
	_, err = ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, spec))
	require.NoError(t, err)
	assert.Empty(t, api.current().IamInstanceProfile)
}

func TestGenericEC2ExternalTerminationRequiresCoreReplacement(t *testing.T) {
	api := &statefulEC2API{}
	key := "us-east-1~external-delete"
	client := setupGenericEC2(t, api)
	_, err := ingress.Object[types.ProvisionRequest, EC2InstanceOutputs](client, ServiceName, key, "Provision").Request(t.Context(), drivertest.ProvisionRequest(t, genericEC2Spec(key)))
	require.NoError(t, err)
	api.terminateExternally()

	result, err := ingress.Object[restate.Void, types.ReconcileResult](client, ServiceName, key, "Reconcile").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.True(t, result.ReplacementRequired)
	assert.Contains(t, result.Error, "deleted externally")
	assert.Equal(t, types.StatusError, getGenericEC2Status(t, client, key).Status)
}

func getGenericEC2Status(t *testing.T, client *ingress.Client, key string) types.StatusResponse {
	t.Helper()
	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	return status
}
