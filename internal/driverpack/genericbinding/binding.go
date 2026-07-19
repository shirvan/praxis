// Package genericbinding makes generic-kernel production registrations
// statically distinguishable from legacy reflected drivers.
package genericbinding

import restate "github.com/restatedev/sdk-go"

type driver interface {
	ServiceName() string
	GenericLifecycle()
}

type definition struct {
	restate.ServiceDefinition
}

func (definition) GenericLifecycleBinding() {}

// Reflect only accepts a generic lifecycle driver and preserves a marker on
// the returned definition for inventory conformance tests.
func Reflect(value driver, opts ...restate.ServiceDefinitionOption) restate.ServiceDefinition {
	return definition{ServiceDefinition: restate.Reflect(value, opts...)}
}
