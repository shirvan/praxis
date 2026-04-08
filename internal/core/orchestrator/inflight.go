package orchestrator

import (
	"sort"
	"time"

	"github.com/shirvan/praxis/internal/core/provider"
)

type inFlightProvision struct {
	invocation provider.ProvisionInvocation
	adapter    provider.Adapter
	timeout    time.Duration
	startedAt  time.Time
}

type inFlightDelete struct {
	invocation provider.DeleteInvocation
	adapter    provider.Adapter
	timeout    time.Duration
}

func nextInFlightCompletion(inFlight map[string]*inFlightProvision) (string, *inFlightProvision) {
	names := make([]string, 0, len(inFlight))
	for name := range inFlight {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0], inFlight[names[0]]
}

func nextInFlightDeleteCompletion(inFlight map[string]*inFlightDelete) (string, *inFlightDelete) {
	names := make([]string, 0, len(inFlight))
	for name := range inFlight {
		names = append(names, name)
	}
	sort.Strings(names)
	return names[0], inFlight[names[0]]
}
