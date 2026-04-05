package natgw

import (
	"github.com/shirvan/praxis/internal/drivers"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := NATGatewaySpec{ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "dev"}}
	observed := ObservedState{State: "available", ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "dev", "praxis:managed-key": "us-east-1~nat-a"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_TagChanged(t *testing.T) {
	desired := NATGatewaySpec{ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "prod"}}
	observed := ObservedState{State: "available", ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "dev"}}
	assert.True(t, HasDrift(desired, observed))
}

func TestHasDrift_NonAvailableSkipped(t *testing.T) {
	desired := NATGatewaySpec{ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "prod"}}
	observed := ObservedState{State: "pending", ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "dev"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_SubnetChangedNoDrift(t *testing.T) {
	desired := NATGatewaySpec{SubnetId: "subnet-new", ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a"}}
	observed := ObservedState{State: "available", SubnetId: "subnet-old", ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a"}}
	assert.False(t, HasDrift(desired, observed))
}

func TestComputeFieldDiffs_Tags(t *testing.T) {
	diffs := ComputeFieldDiffs(
		NATGatewaySpec{ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "prod"}},
		ObservedState{State: "available", ConnectivityType: "public", Tags: map[string]string{"Name": "nat-a", "env": "dev"}},
	)
	assert.Contains(t, diffs, FieldDiffEntry{Path: "tags.env", OldValue: "dev", NewValue: "prod"})
}

func TestComputeFieldDiffs_ImmutableSubnet(t *testing.T) {
	diffs := ComputeFieldDiffs(
		NATGatewaySpec{SubnetId: "subnet-new", ConnectivityType: "public", Tags: map[string]string{}},
		ObservedState{SubnetId: "subnet-old", ConnectivityType: "public", Tags: map[string]string{}},
	)
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.subnetId (immutable, requires replacement)", OldValue: "subnet-old", NewValue: "subnet-new"})
}

func TestComputeFieldDiffs_ImmutableConnectivity(t *testing.T) {
	diffs := ComputeFieldDiffs(
		NATGatewaySpec{ConnectivityType: "private", Tags: map[string]string{}},
		ObservedState{ConnectivityType: "public", Tags: map[string]string{}},
	)
	assert.Contains(t, diffs, FieldDiffEntry{Path: "spec.connectivityType (immutable, requires replacement)", OldValue: "public", NewValue: "private"})
}

func TestTagsMatch_IgnoresPraxisTags(t *testing.T) {
	assert.True(t, drivers.TagsMatch(
		map[string]string{"Name": "nat-a"},
		map[string]string{"Name": "nat-a", "praxis:managed-key": "us-east-1~nat-a"},
	))
}
