package lambda

import (
	"fmt"

	"github.com/shirvan/praxis/internal/drivers"
)

func outputsFromObserved(o ObservedState) LambdaFunctionOutputs {
	return LambdaFunctionOutputs{FunctionArn: o.FunctionArn, FunctionName: o.FunctionName, Version: o.Version, State: o.State, LastModified: o.LastModified, LastUpdateStatus: o.LastUpdateStatus, CodeSha256: o.CodeSha256}
}
func specFromObserved(o ObservedState) LambdaFunctionSpec {
	s := applyDefaults(LambdaFunctionSpec{FunctionName: o.FunctionName, Role: o.Role, PackageType: o.PackageType, Runtime: o.Runtime, Handler: o.Handler, Description: o.Description, MemorySize: o.MemorySize, Timeout: o.Timeout, Environment: o.Environment, Layers: append([]string(nil), o.Layers...), Tags: drivers.FilterPraxisTags(o.Tags)})
	if len(o.VpcConfig.SubnetIds) > 0 || len(o.VpcConfig.SecurityGroupIds) > 0 {
		s.VPCConfig = &VPCConfigSpec{SubnetIds: append([]string(nil), o.VpcConfig.SubnetIds...), SecurityGroupIds: append([]string(nil), o.VpcConfig.SecurityGroupIds...)}
	}
	if o.DeadLetterTarget != "" {
		s.DeadLetterConfig = &DeadLetterConfigSpec{TargetArn: o.DeadLetterTarget}
	}
	if o.TracingMode != "" {
		s.TracingConfig = &TracingConfigSpec{Mode: o.TracingMode}
	}
	if len(o.Architectures) > 0 {
		s.Architectures = append([]string(nil), o.Architectures...)
	}
	if o.EphemeralSize > 0 {
		s.EphemeralStorage = &EphemeralStorageSpec{Size: o.EphemeralSize}
	}
	if o.ImageURI != "" {
		s.Code.ImageURI = o.ImageURI
	}
	return s
}
func applyDefaults(s LambdaFunctionSpec) LambdaFunctionSpec {
	if s.MemorySize == 0 {
		s.MemorySize = 128
	}
	if s.Timeout == 0 {
		s.Timeout = 3
	}
	if s.PackageType == "" {
		if s.Code.ImageURI != "" {
			s.PackageType = "Image"
		} else {
			s.PackageType = "Zip"
		}
	}
	if len(s.Architectures) == 0 {
		s.Architectures = []string{"x86_64"}
	}
	if s.Tags == nil {
		s.Tags = map[string]string{}
	}
	return s
}
func validateProvisionSpec(s LambdaFunctionSpec) error {
	if s.Region == "" {
		return fmt.Errorf("region is required")
	}
	if s.FunctionName == "" {
		return fmt.Errorf("functionName is required")
	}
	if s.Role == "" {
		return fmt.Errorf("role is required")
	}
	if s.PackageType != "Image" {
		if s.Runtime == "" {
			return fmt.Errorf("runtime is required for Zip functions")
		}
		if s.Handler == "" {
			return fmt.Errorf("handler is required for Zip functions")
		}
	}
	return validateCode(s.Code)
}
