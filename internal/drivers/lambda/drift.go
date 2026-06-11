// Drift detection for Lambda Functions.
// Compares desired spec against observed AWS state for all mutable configuration
// fields and tags. Immutable fields (packageType, functionName, architectures)
// are reported as informational diffs only.
package lambda

import (
	"slices"
	"strings"

	"github.com/shirvan/praxis/internal/drivers"
)

// FieldDiffEntry represents a single field difference with JSON path and old/new values.
type FieldDiffEntry struct {
	Path     string
	OldValue any
	NewValue any
}

// HasDrift returns true if any correctable field differs. Immutable fields
// (architectures, packageType, functionName) and code artifact diffs are
// excluded: drift correction only updates configuration and tags, so
// triggering on uncorrectable fields would loop forever without converging.
// ComputeFieldDiffs still reports the full set of diffs for visibility.
func HasDrift(desired LambdaFunctionSpec, observed ObservedState) bool {
	for _, diff := range ComputeFieldDiffs(desired, observed) {
		if isCorrectablePath(diff.Path) {
			return true
		}
	}
	return false
}

// isCorrectablePath reports whether a diff at the given path can be fixed by
// UpdateFunctionConfiguration/UpdateTags during drift correction.
func isCorrectablePath(path string) bool {
	return !strings.Contains(path, "(immutable, ignored)") && !strings.HasPrefix(path, "spec.code.")
}

// ComputeFieldDiffs returns per-field diffs between desired and observed state.
// Covers: role, description, runtime, handler, memorySize, timeout, environment,
// layers, vpcConfig, deadLetterConfig, tracingConfig, ephemeralStorage, tags,
// code.imageUri, and immutable info diffs for architectures, packageType, functionName.
func ComputeFieldDiffs(desired LambdaFunctionSpec, observed ObservedState) []FieldDiffEntry {
	var diffs []FieldDiffEntry

	if desired.Role != observed.Role {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.role", OldValue: observed.Role, NewValue: desired.Role})
	}
	if desired.Description != observed.Description {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.description", OldValue: observed.Description, NewValue: desired.Description})
	}
	if desired.Runtime != "" && desired.Runtime != observed.Runtime {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.runtime", OldValue: observed.Runtime, NewValue: desired.Runtime})
	}
	if desired.Handler != observed.Handler {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.handler", OldValue: observed.Handler, NewValue: desired.Handler})
	}
	if desired.MemorySize != observed.MemorySize {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.memorySize", OldValue: observed.MemorySize, NewValue: desired.MemorySize})
	}
	if desired.Timeout != observed.Timeout {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.timeout", OldValue: observed.Timeout, NewValue: desired.Timeout})
	}
	if !mapsEqual(desired.Environment, observed.Environment) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.environment", OldValue: observed.Environment, NewValue: desired.Environment})
	}
	if !slices.Equal(desired.Layers, observed.Layers) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.layers", OldValue: observed.Layers, NewValue: desired.Layers})
	}
	if !vpcConfigEqual(desired.VPCConfig, observed.VpcConfig) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.vpcConfig", OldValue: observed.VpcConfig, NewValue: desired.VPCConfig})
	}
	if deadLetterTarget(desired.DeadLetterConfig) != observed.DeadLetterTarget {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.deadLetterConfig.targetArn", OldValue: observed.DeadLetterTarget, NewValue: deadLetterTarget(desired.DeadLetterConfig)})
	}
	if tracingMode(desired.TracingConfig) != normalizeTracingMode(observed.TracingMode) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.tracingConfig.mode", OldValue: normalizeTracingMode(observed.TracingMode), NewValue: tracingMode(desired.TracingConfig)})
	}
	if !slices.Equal(normalizeArchitectures(desired.Architectures), normalizeArchitectures(observed.Architectures)) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.architectures (immutable, ignored)", OldValue: observed.Architectures, NewValue: desired.Architectures})
	}
	if ephemeralSize(desired.EphemeralStorage) != normalizeEphemeralSize(observed.EphemeralSize) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.ephemeralStorage.size", OldValue: normalizeEphemeralSize(observed.EphemeralSize), NewValue: ephemeralSize(desired.EphemeralStorage)})
	}
	if !tagsEqual(desired.Tags, observed.Tags) {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.tags", OldValue: drivers.FilterPraxisTags(observed.Tags), NewValue: drivers.FilterPraxisTags(desired.Tags)})
	}
	if desired.PackageType != "" && desired.PackageType != observed.PackageType {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.packageType (immutable, ignored)", OldValue: observed.PackageType, NewValue: desired.PackageType})
	}
	if desired.FunctionName != "" && desired.FunctionName != observed.FunctionName {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.functionName (immutable, ignored)", OldValue: observed.FunctionName, NewValue: desired.FunctionName})
	}
	if desired.Code.ImageURI != "" && desired.Code.ImageURI != observed.ImageURI {
		diffs = append(diffs, FieldDiffEntry{Path: "spec.code.imageUri", OldValue: observed.ImageURI, NewValue: desired.Code.ImageURI})
	}

	return diffs
}

// codeSpecChanged returns true if any code deployment field differs.
func codeSpecChanged(a, b CodeSpec) bool {
	if (a.S3 == nil) != (b.S3 == nil) {
		return true
	}
	if a.S3 != nil && b.S3 != nil && *a.S3 != *b.S3 {
		return true
	}
	return a.ZipFile != b.ZipFile || a.ImageURI != b.ImageURI
}

// mapsEqual compares two string maps for exact equality.
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

// tagsEqual compares tags after filtering out praxis: namespace tags.
func tagsEqual(a, b map[string]string) bool {
	return mapsEqual(drivers.FilterPraxisTags(a), drivers.FilterPraxisTags(b))
}

// vpcConfigEqual compares desired VPC config (may be nil) against observed.
func vpcConfigEqual(desired *VPCConfigSpec, observed VPCConfigSpec) bool {
	if desired == nil {
		return len(observed.SubnetIds) == 0 && len(observed.SecurityGroupIds) == 0
	}
	return slices.Equal(desired.SubnetIds, observed.SubnetIds) && slices.Equal(desired.SecurityGroupIds, observed.SecurityGroupIds)
}

// deadLetterTarget extracts the target ARN from a possibly-nil config.
func deadLetterTarget(spec *DeadLetterConfigSpec) string {
	if spec == nil {
		return ""
	}
	return spec.TargetArn
}

// tracingMode extracts the mode from a possibly-nil config.
// Returns "PassThrough" when nil, matching the AWS default.
func tracingMode(spec *TracingConfigSpec) string {
	if spec == nil {
		return "PassThrough"
	}
	return spec.Mode
}

// ephemeralSize extracts the size from a possibly-nil config.
// Returns 512 when nil, matching the AWS default (512 MB).
func ephemeralSize(spec *EphemeralStorageSpec) int32 {
	if spec == nil {
		return 512
	}
	return spec.Size
}

// normalizeTracingMode maps an unreported observed mode to the AWS default.
// GetFunctionConfiguration always reports a mode on real AWS, so "" only
// occurs with incomplete emulator responses; treating it as PassThrough
// prevents a non-convergent drift loop.
func normalizeTracingMode(mode string) string {
	if mode == "" {
		return "PassThrough"
	}
	return mode
}

// normalizeEphemeralSize maps an unreported observed size to the AWS default
// (512 MB); 0 is not a real AWS value.
func normalizeEphemeralSize(size int32) int32 {
	if size == 0 {
		return 512
	}
	return size
}

// normalizeArchitectures defaults to ["x86_64"] if empty (matches AWS default).
func normalizeArchitectures(values []string) []string {
	if len(values) == 0 {
		return []string{"x86_64"}
	}
	return append([]string(nil), values...)
}
