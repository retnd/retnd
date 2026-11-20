package webhost

import (
	"context"

	"github.com/retnd/retnd/core/service"
)

// The observability block's doubles (EPIC O, #898). The route-level behaviour
// is proved against the REAL service in observability_route_test.go; these
// exist so the two fake backends satisfy ObservabilityAccess and the contract
// tests can drive the handlers.

func (f *syncFakeBackend) ObservabilitySettings(context.Context) (service.ObservabilitySettings, error) {
	return service.ObservabilitySettings{}, nil
}

func (f *syncFakeBackend) UpdateObservabilitySettings(context.Context, service.UpdateObservabilitySettingsRequest) (service.ObservabilitySettings, error) {
	return service.ObservabilitySettings{}, nil
}

func (f *asyncFakeBackend) ObservabilitySettings(context.Context) (service.ObservabilitySettings, error) {
	return service.ObservabilitySettings{}, nil
}

func (f *asyncFakeBackend) UpdateObservabilitySettings(context.Context, service.UpdateObservabilitySettingsRequest) (service.ObservabilitySettings, error) {
	return service.ObservabilitySettings{}, nil
}
