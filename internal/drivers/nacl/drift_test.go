package nacl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/praxiscloud/praxis/internal/drivers/nacl"
)

func TestHasDrift_NoDrift(t *testing.T) {
	desired := nacl.NetworkACLSpec{
		IngressRules:       []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}},
		EgressRules:        []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "-1", RuleAction: "allow", CidrBlock: "0.0.0.0/0"}},
		SubnetAssociations: []string{"subnet-a"},
		Tags:               map[string]string{"env": "dev"},
	}
	observed := nacl.ObservedState{
		IngressRules: []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}},
		EgressRules:  []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "-1", RuleAction: "allow", CidrBlock: "0.0.0.0/0"}},
		Associations: []nacl.NetworkACLAssociation{{AssociationId: "aclassoc-1", SubnetId: "subnet-a"}},
		Tags:         map[string]string{"env": "dev", "praxis:managed-key": "vpc-123~public"},
	}
	assert.False(t, nacl.HasDrift(desired, observed))
}

func TestHasDrift_RuleAdded(t *testing.T) {
	desired := nacl.NetworkACLSpec{IngressRules: []nacl.NetworkACLRule{{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80}}}
	observed := nacl.ObservedState{}
	assert.True(t, nacl.HasDrift(desired, observed))
}

func TestHasDrift_AssociationChanged(t *testing.T) {
	desired := nacl.NetworkACLSpec{SubnetAssociations: []string{"subnet-a"}}
	observed := nacl.ObservedState{Associations: []nacl.NetworkACLAssociation{{AssociationId: "aclassoc-1", SubnetId: "subnet-b"}}}
	assert.True(t, nacl.HasDrift(desired, observed))
}

func TestRulesMatch_OrderIndependent(t *testing.T) {
	desired := nacl.NetworkACLSpec{IngressRules: []nacl.NetworkACLRule{
		{RuleNumber: 200, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 443, ToPort: 443},
		{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80},
	}}
	observed := nacl.ObservedState{IngressRules: []nacl.NetworkACLRule{
		{RuleNumber: 100, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 80, ToPort: 80},
		{RuleNumber: 200, Protocol: "6", RuleAction: "allow", CidrBlock: "0.0.0.0/0", FromPort: 443, ToPort: 443},
	}}
	assert.False(t, nacl.HasDrift(desired, observed))
}

func TestComputeFieldDiffs_TagsIgnorePraxisTags(t *testing.T) {
	diffs := nacl.ComputeFieldDiffs(
		nacl.NetworkACLSpec{Tags: map[string]string{"env": "prod"}},
		nacl.ObservedState{Tags: map[string]string{"env": "dev", "praxis:managed-key": "vpc-123~public"}},
	)
	assert.Len(t, diffs, 1)
	assert.Equal(t, "tags.env", diffs[0].Path)
	assert.Equal(t, "dev", diffs[0].OldValue)
	assert.Equal(t, "prod", diffs[0].NewValue)
}

func TestComputeFieldDiffs_Associations(t *testing.T) {
	diffs := nacl.ComputeFieldDiffs(
		nacl.NetworkACLSpec{SubnetAssociations: []string{"subnet-a"}},
		nacl.ObservedState{Associations: []nacl.NetworkACLAssociation{{AssociationId: "aclassoc-1", SubnetId: "subnet-b"}}},
	)
	assert.Len(t, diffs, 2)
	assert.Contains(t, diffs[0].Path+diffs[1].Path, "spec.subnetAssociations[")
}
