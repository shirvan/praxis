package alb

import (
	"strings"
	"testing"
)

func TestHasDrift_TagsAndSubnets(t *testing.T) {
	spec := applyDefaults(ALBSpec{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{"env": "dev"},
	})
	observed := ObservedState{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{"env": "prod"},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from tag difference")
	}
}

func TestHasDrift_NoDrift(t *testing.T) {
	spec := applyDefaults(ALBSpec{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{"env": "dev"},
	})
	observed := ObservedState{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{"env": "dev"},
	}
	if HasDrift(spec, observed) {
		t.Fatal("expected no drift")
	}
}

func TestHasDrift_SubnetOrder(t *testing.T) {
	spec := applyDefaults(ALBSpec{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-2", "subnet-1"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{},
	})
	observed := ObservedState{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{},
	}
	if HasDrift(spec, observed) {
		t.Fatal("expected no drift when subnets are same but differently ordered")
	}
}

func TestHasDrift_AccessLogs(t *testing.T) {
	spec := applyDefaults(ALBSpec{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		AccessLogs:     &AccessLogConfig{Enabled: true, Bucket: "my-bucket"},
		IdleTimeout:    60,
		Tags:           map[string]string{},
	})
	observed := ObservedState{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{},
	}
	if !HasDrift(spec, observed) {
		t.Fatal("expected drift from access logs difference")
	}
}

func TestComputeFieldDiffs_ImmutableScheme(t *testing.T) {
	spec := applyDefaults(ALBSpec{
		Scheme:         "internal",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{},
	})
	observed := ObservedState{
		Scheme:         "internet-facing",
		IpAddressType:  "ipv4",
		Subnets:        []string{"subnet-1", "subnet-2"},
		SecurityGroups: []string{"sg-1"},
		IdleTimeout:    60,
		Tags:           map[string]string{},
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
		t.Fatal("expected scheme diff in ComputeFieldDiffs")
	}
}
