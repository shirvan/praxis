package lambdalayer

import (
	"fmt"
	"slices"
	"strings"
)

func applyDefaults(s LambdaLayerSpec) LambdaLayerSpec {
	if s.Permissions == nil {
		s.Permissions = &PermissionsSpec{}
	} else {
		p := normalizePermissions(*s.Permissions)
		s.Permissions = &p
	}
	if s.CompatibleRuntimes == nil {
		s.CompatibleRuntimes = []string{}
	} else {
		s.CompatibleRuntimes = append([]string(nil), s.CompatibleRuntimes...)
		slices.Sort(s.CompatibleRuntimes)
	}
	if s.CompatibleArchitectures == nil {
		s.CompatibleArchitectures = []string{}
	} else {
		s.CompatibleArchitectures = append([]string(nil), s.CompatibleArchitectures...)
		slices.Sort(s.CompatibleArchitectures)
	}
	return s
}
func validateProvisionSpec(s LambdaLayerSpec) error {
	if strings.TrimSpace(s.Region) == "" {
		return fmt.Errorf("region is required")
	}
	if strings.TrimSpace(s.LayerName) == "" {
		return fmt.Errorf("layerName is required")
	}
	return validateCode(s.Code)
}
func layerContentChanged(a, b CodeSpec) bool {
	if (a.S3 == nil) != (b.S3 == nil) {
		return true
	}
	if a.S3 != nil && b.S3 != nil && *a.S3 != *b.S3 {
		return true
	}
	return a.ZipFile != b.ZipFile
}
func layerMetadataChanged(a, b LambdaLayerSpec) bool {
	return a.Description != b.Description || a.LicenseInfo != b.LicenseInfo || !slices.Equal(a.CompatibleRuntimes, b.CompatibleRuntimes) || !slices.Equal(a.CompatibleArchitectures, b.CompatibleArchitectures)
}
func specFromObserved(o ObservedState) LambdaLayerSpec {
	p := o.Permissions
	return applyDefaults(LambdaLayerSpec{LayerName: o.LayerName, Description: o.Description, CompatibleRuntimes: append([]string(nil), o.CompatibleRuntimes...), CompatibleArchitectures: append([]string(nil), o.CompatibleArchitectures...), LicenseInfo: o.LicenseInfo, Permissions: &p})
}
func outputsFromObserved(o ObservedState) LambdaLayerOutputs {
	return LambdaLayerOutputs{LayerArn: o.LayerArn, LayerVersionArn: o.LayerVersionArn, LayerName: o.LayerName, Version: o.Version, CodeSize: o.CodeSize, CodeSha256: o.CodeSha256, CreatedDate: o.CreatedDate}
}
func desiredPermissions(s LambdaLayerSpec) PermissionsSpec {
	if s.Permissions == nil {
		return PermissionsSpec{}
	}
	return normalizePermissions(*s.Permissions)
}
