package provider

import (
	"fmt"
	"strings"
)

const (
	// KeySeparator is the separator used in resource keys.
	// '~' is chosen because it is URL-safe and does not collide with characters
	// valid in AWS resource names.
	KeySeparator = "~"

	// DefaultAWSRegion is a compatibility fallback for resource kinds whose schema
	// does not yet carry region explicitly.
	DefaultAWSRegion = "us-east-1"
)

// KeyScope describes the uniqueness scope of a resource kind's key.
//
// Adapters declare their scope so that the CLI and SDK can assemble the
// correct key from user input plus ambient context (e.g. PRAXIS_REGION).
type KeyScope int

const (
	// KeyScopeGlobal means the resource name is globally unique (e.g. S3 bucket).
	// Key format: <name>
	KeyScopeGlobal KeyScope = iota

	// KeyScopeRegion means the resource name is unique within a region.
	// Key format: <region>~<name>
	KeyScopeRegion

	// KeyScopeCustom means the adapter uses a resource-specific compound key
	// (e.g. SecurityGroup: <vpcId>~<groupName>).
	KeyScopeCustom
)

// JoinKey joins key segments with the canonical separator.
func JoinKey(parts ...string) string {
	return strings.Join(parts, KeySeparator)
}

// ValidateKeyPart checks that a single key segment is non-empty and does not
// contain the separator.
func ValidateKeyPart(label, value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return fmt.Errorf("%s is required to build a resource key", label)
	}
	if strings.Contains(v, KeySeparator) {
		return fmt.Errorf("%s %q cannot contain %q", label, v, KeySeparator)
	}
	return nil
}
