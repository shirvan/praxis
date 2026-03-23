package nlb

import (
	"strings"
	"testing"
)

func TestHasDrift_TagsAndSubnets(t *testing.T) {
	spec := applyDefaults(NLBSpec{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1", "subnet-2"},
		Tags:          map[string]string{"env": "dev"},
	})
	observed := ObservedState{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1", "subnet-2"},
		Tags:          map[string]string{"env": "prod"},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from tag difference")
	}
}

func TestHasDrift_NoDrift(t *testing.T) {
	spec := applyDefaults(NLBSpec{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1", "subnet-2"},
		Tags:          map[string]string{"env": "dev"},
	})
	observed := ObservedState{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1", "subnet-2"},
		Tags:          map[string]string{"env": "dev"},
	}
	if HasDrift(spec, observed) {
		t.Fatal("expected no drift")
	}
}

func TestHasDrift_SubnetOrder(t *testing.T) {
	spec := applyDefaults(NLBSpec{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-2", "subnet-1"},
		Tags:          map[string]string{},
	})
	observed := ObservedState{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1", "subnet-2"},
		Tags:          map[string]string{},
	}
	if HasDrift(spec, observed) {
		t.Fatal("expected no drift when subnets are same but differently ordered")
	}
}

func TestHasDrift_CrossZone(t *testing.T) {
	spec := applyDefaults(NLBSpec{
		Scheme:                 "internet-facing",
		IpAddressType:          "ipv4",
		Subnets:                []string{"subnet-1"},
		CrossZoneLoadBalancing: true,
		Tags:                   map[string]string{},
	})
	observed := ObservedState{
		Scheme:                 "internet-facing",
		IpAddressType:          "ipv4",
		Subnets:                []string{"subnet-1"},
		CrossZoneLoadBalancing: false,
		Tags:                   map[string]string{},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from cross-zone difference")
	}
}

func TestComputeFieldDiffs_ImmutableScheme(t *testing.T) {
	spec := applyDefaults(NLBSpec{
		Scheme:        "internal",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1"},
		Tags:          map[string]string{},
	})
	observed := ObservedState{
		Scheme:        "internet-facing",
		IpAddressType: "ipv4",
		Subnets:       []string{"subnet-1"},
		Tags:          map[string]string{},
	}
	diffs := ComputeFieldDiffs(spec, observed)
	found := false
	for _, d := range diffs {
		if strings.Contains(d.Path, "scheme") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected scheme diff in %v", diffs)
	}
}
