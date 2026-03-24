package iaminstanceprofile

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	restate "github.com/restatedev/sdk-go"

	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
	"github.com/shirvan/praxis/internal/core/auth"
	"github.com/shirvan/praxis/internal/core/authservice"

	"github.com/shirvan/praxis/pkg/types"
)

type roleCall struct {
	ProfileName string
	RoleName    string
}

type fakeIAMInstanceProfileAPI struct {
	mu              sync.Mutex
	profiles        map[string]ObservedState
	createCalls     int
	deleteCalls     int
	addRoleCalls    []roleCall
	removeRoleCalls []roleCall
	tagCalls        int
	untagCalls      int
}

func newFakeIAMInstanceProfileAPI() *fakeIAMInstanceProfileAPI {
	return &fakeIAMInstanceProfileAPI{profiles: map[string]ObservedState{}}
}

func (f *fakeIAMInstanceProfileAPI) CreateInstanceProfile(ctx context.Context, spec IAMInstanceProfileSpec) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.profiles[spec.InstanceProfileName]; exists {
		return "", "", &mockAPIError{code: "EntityAlreadyExists", message: "exists"}
	}
	f.createCalls++
	obs := ObservedState{
		Arn:                 "arn:aws:iam::123456789012:instance-profile/" + spec.InstanceProfileName,
		InstanceProfileId:   fmt.Sprintf("AIP%03d", f.createCalls),
		InstanceProfileName: spec.InstanceProfileName,
		Path:                spec.Path,
		Tags:                cloneTags(spec.Tags),
	}
	f.profiles[spec.InstanceProfileName] = obs
	return obs.Arn, obs.InstanceProfileId, nil
}

func (f *fakeIAMInstanceProfileAPI) DescribeInstanceProfile(ctx context.Context, name string) (ObservedState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obs, ok := f.profiles[name]
	if !ok {
		return ObservedState{}, &mockAPIError{code: "NoSuchEntity", message: "missing"}
	}
	return cloneObservedState(obs), nil
}

func (f *fakeIAMInstanceProfileAPI) DeleteInstanceProfile(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	obs, ok := f.profiles[name]
	if !ok {
		return &mockAPIError{code: "NoSuchEntity", message: "missing"}
	}
	if obs.RoleName != "" {
		return &mockAPIError{code: "DeleteConflict", message: "role still attached"}
	}
	delete(f.profiles, name)
	return nil
}

func (f *fakeIAMInstanceProfileAPI) AddRoleToInstanceProfile(ctx context.Context, name, roleName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addRoleCalls = append(f.addRoleCalls, roleCall{ProfileName: name, RoleName: roleName})
	obs, ok := f.profiles[name]
	if !ok {
		return &mockAPIError{code: "NoSuchEntity", message: "missing"}
	}
	if obs.RoleName != "" {
		return &mockAPIError{code: "LimitExceeded", message: "only one role allowed"}
	}
	obs.RoleName = roleName
	f.profiles[name] = obs
	return nil
}

func (f *fakeIAMInstanceProfileAPI) RemoveRoleFromInstanceProfile(ctx context.Context, name, roleName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeRoleCalls = append(f.removeRoleCalls, roleCall{ProfileName: name, RoleName: roleName})
	obs, ok := f.profiles[name]
	if !ok {
		return &mockAPIError{code: "NoSuchEntity", message: "missing"}
	}
	obs.RoleName = ""
	f.profiles[name] = obs
	return nil
}

func (f *fakeIAMInstanceProfileAPI) TagInstanceProfile(ctx context.Context, name string, tags map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tagCalls++
	obs := f.profiles[name]
	if obs.Tags == nil {
		obs.Tags = map[string]string{}
	}
	for key, value := range tags {
		obs.Tags[key] = value
	}
	f.profiles[name] = obs
	return nil
}

func (f *fakeIAMInstanceProfileAPI) UntagInstanceProfile(ctx context.Context, name string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.untagCalls++
	obs := f.profiles[name]
	for _, key := range keys {
		delete(obs.Tags, key)
	}
	f.profiles[name] = obs
	return nil
}

func cloneObservedState(obs ObservedState) ObservedState {
	clone := obs
	clone.Tags = cloneTags(obs.Tags)
	return clone
}

func cloneTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	clone := make(map[string]string, len(tags))
	for key, value := range tags {
		clone[key] = value
	}
	return clone
}

func setupIAMInstanceProfileDriver(t *testing.T, api IAMInstanceProfileAPI) *ingress.Client {
	t.Helper()
	t.Setenv("PRAXIS_ACCOUNT_NAME", "test")
	t.Setenv("PRAXIS_ACCOUNT_REGION", "us-east-1")
	t.Setenv("PRAXIS_ACCOUNT_CREDENTIAL_SOURCE", "static")
	t.Setenv("PRAXIS_ACCOUNT_ACCESS_KEY_ID", "test")
	t.Setenv("PRAXIS_ACCOUNT_SECRET_ACCESS_KEY", "test")

	driver := NewIAMInstanceProfileDriverWithFactory(authservice.NewLocalAuthClient(auth.LoadFromEnv()), func(cfg aws.Config) IAMInstanceProfileAPI {
		return api
	})
	env := restatetest.Start(t, restate.Reflect(driver))
	return env.Ingress()
}

func testIAMInstanceProfileSpec(name, role string, tags map[string]string) IAMInstanceProfileSpec {
	if tags == nil {
		tags = map[string]string{"Name": name}
	}
	return IAMInstanceProfileSpec{
		Account:             "test",
		Path:                "/",
		InstanceProfileName: name,
		RoleName:            role,
		Tags:                tags,
	}
}

func TestServiceName(t *testing.T) {
	drv := NewIAMInstanceProfileDriver(nil)
	assert.Equal(t, ServiceName, drv.ServiceName())
}

func TestSpecFromObserved_RoundTrip(t *testing.T) {
	obs := ObservedState{
		Arn:                 "arn:aws:iam::123456789012:instance-profile/app-profile",
		InstanceProfileId:   "AIPAJFEXAMPLE",
		InstanceProfileName: "app-profile",
		Path:                "/app/",
		RoleName:            "app-role",
		Tags:                map[string]string{"env": "dev", "Name": "app-profile", "praxis:managed-key": "ignore-me"},
	}

	spec := specFromObserved(obs)
	assert.Equal(t, obs.InstanceProfileName, spec.InstanceProfileName)
	assert.Equal(t, obs.Path, spec.Path)
	assert.Equal(t, obs.RoleName, spec.RoleName)
	assert.Equal(t, map[string]string{"env": "dev", "Name": "app-profile"}, spec.Tags)
}

func TestOutputsFromObserved(t *testing.T) {
	outputs := outputsFromObserved(ObservedState{
		Arn:                 "arn:aws:iam::123456789012:instance-profile/app-profile",
		InstanceProfileId:   "AIPAJFEXAMPLE",
		InstanceProfileName: "app-profile",
	})

	assert.Equal(t, "arn:aws:iam::123456789012:instance-profile/app-profile", outputs.Arn)
	assert.Equal(t, "AIPAJFEXAMPLE", outputs.InstanceProfileId)
	assert.Equal(t, "app-profile", outputs.InstanceProfileName)
}

func TestDiffTags(t *testing.T) {
	add, remove := diffTags(
		map[string]string{"env": "prod", "team": "platform"},
		map[string]string{"env": "dev", "owner": "alice"},
	)

	require.Equal(t, map[string]string{"env": "prod", "team": "platform"}, add)
	assert.Equal(t, []string{"owner"}, remove)
}

func TestImport_DefaultsToObserved(t *testing.T) {
	api := newFakeIAMInstanceProfileAPI()
	api.profiles["app-profile"] = ObservedState{
		Arn:                 "arn:aws:iam::123456789012:instance-profile/app-profile",
		InstanceProfileId:   "AIP123",
		InstanceProfileName: "app-profile",
		Path:                "/",
		RoleName:            "app-role",
		Tags:                map[string]string{"Name": "app-profile"},
	}
	client := setupIAMInstanceProfileDriver(t, api)

	_, err := ingress.Object[types.ImportRef, IAMInstanceProfileOutputs](client, ServiceName, "app-profile", "Import").Request(t.Context(), types.ImportRef{ResourceID: "app-profile", Account: "test"})
	require.NoError(t, err)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, "app-profile", "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.ModeObserved, status.Mode)
}

func TestProvision_CreatesAndAssociatesRole(t *testing.T) {
	api := newFakeIAMInstanceProfileAPI()
	client := setupIAMInstanceProfileDriver(t, api)
	key := "app-profile"

	outputs, err := ingress.Object[IAMInstanceProfileSpec, IAMInstanceProfileOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testIAMInstanceProfileSpec(key, "app-role", map[string]string{"Name": key, "env": "dev"}))
	require.NoError(t, err)
	assert.Equal(t, key, outputs.InstanceProfileName)
	assert.Equal(t, 1, api.createCalls)
	assert.Len(t, api.addRoleCalls, 1)

	status, err := ingress.Object[restate.Void, types.StatusResponse](client, ServiceName, key, "GetStatus").Request(t.Context(), restate.Void{})
	require.NoError(t, err)
	assert.Equal(t, types.StatusReady, status.Status)
	assert.Equal(t, types.ModeManaged, status.Mode)
}

func TestProvision_PathChangeBlocked(t *testing.T) {
	api := newFakeIAMInstanceProfileAPI()
	client := setupIAMInstanceProfileDriver(t, api)
	key := "app-profile"

	_, err := ingress.Object[IAMInstanceProfileSpec, IAMInstanceProfileOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testIAMInstanceProfileSpec(key, "app-role", nil))
	require.NoError(t, err)

	spec := testIAMInstanceProfileSpec(key, "app-role", nil)
	spec.Path = "/other/"
	_, err = ingress.Object[IAMInstanceProfileSpec, IAMInstanceProfileOutputs](client, ServiceName, key, "Provision").Request(t.Context(), spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path is immutable")
}

func TestProvision_ChangeRole_RemovesThenAdds(t *testing.T) {
	api := newFakeIAMInstanceProfileAPI()
	client := setupIAMInstanceProfileDriver(t, api)
	key := "app-profile"

	_, err := ingress.Object[IAMInstanceProfileSpec, IAMInstanceProfileOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testIAMInstanceProfileSpec(key, "role-a", nil))
	require.NoError(t, err)

	_, err = ingress.Object[IAMInstanceProfileSpec, IAMInstanceProfileOutputs](client, ServiceName, key, "Provision").Request(t.Context(), testIAMInstanceProfileSpec(key, "role-b", nil))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(api.removeRoleCalls), 1)
	assert.GreaterOrEqual(t, len(api.addRoleCalls), 2)
	assert.Equal(t, "role-a", api.removeRoleCalls[0].RoleName)
	assert.Equal(t, "role-b", api.addRoleCalls[len(api.addRoleCalls)-1].RoleName)
}

func TestDelete_ObservedModeBlocked(t *testing.T) {
	api := newFakeIAMInstanceProfileAPI()
	api.profiles["app-profile"] = ObservedState{
		Arn:                 "arn:aws:iam::123456789012:instance-profile/app-profile",
		InstanceProfileId:   "AIP123",
		InstanceProfileName: "app-profile",
		Path:                "/",
		RoleName:            "app-role",
		Tags:                map[string]string{"Name": "app-profile"},
	}
	client := setupIAMInstanceProfileDriver(t, api)

	_, err := ingress.Object[types.ImportRef, IAMInstanceProfileOutputs](client, ServiceName, "app-profile", "Import").Request(t.Context(), types.ImportRef{ResourceID: "app-profile", Account: "test"})
	require.NoError(t, err)

	_, err = ingress.Object[restate.Void, restate.Void](client, ServiceName, "app-profile", "Delete").Request(t.Context(), restate.Void{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Observed mode")
}
