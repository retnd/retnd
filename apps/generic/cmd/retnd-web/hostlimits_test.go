package main

import (
	"context"
	"testing"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
	"github.com/retnd/retnd/core/service"
)

// The process that serves is the one that knows which host it is on, so it is
// the one that hands the platform's limits (the staging reserve, the read and
// download ceilings) to the recovery engine, before anything is served and on
// both start branches. A platform that says nothing leaves the engine's own
// defaults in force.

type limitedPlatform struct {
	capabilities.BasePlatformAdapter
	limits capabilities.HostLimits
}

func (limitedPlatform) ID() capabilities.PlatformID { return capabilities.PlatformGeneric }
func (limitedPlatform) Capabilities() capabilities.PlatformCapabilities {
	return capabilities.PlatformCapabilities{}
}
func (limitedPlatform) PlatformInfo(context.Context) (capabilities.PlatformInfo, error) {
	return capabilities.PlatformInfo{ID: capabilities.PlatformGeneric, Name: "limited"}, nil
}
func (l limitedPlatform) HostLimits() capabilities.HostLimits { return l.limits }

func openBackend(t *testing.T) *service.BackupService {
	t.Helper()
	backend, closeFn, err := service.Open(context.Background(), configWithALocalHookAndNoRunner(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = closeFn() })
	return backend
}

func TestAPlatformsHostLimitsReachTheRecoveryEngineBeforeAnythingIsServed(t *testing.T) {
	backend := openBackend(t)
	platform := limitedPlatform{limits: capabilities.HostLimits{StagingReserveBytes: 7 << 30, MaxConcurrentReads: 3, MaxReadsPerDestination: 1, MaxDownloadStreams: 2}}
	if err := openWorkflowEngineOn(context.Background(), backend, platform); err != nil {
		t.Fatal(err)
	}
	got := backend.RecoveryLimits()
	if got.StagingReserveBytes != 7<<30 || got.MaxConcurrentReads != 3 || got.MaxReadsPerDestination != 1 || got.MaxDownloadStreams != 2 {
		t.Fatalf("the engine runs with %+v, not the platform's limits", got)
	}
}

func TestAPlatformWithNoOpinionLeavesTheEngineDefaults(t *testing.T) {
	backend := openBackend(t)
	before := backend.RecoveryLimits()
	if err := openWorkflowEngineOn(context.Background(), backend, limitedPlatform{}); err != nil {
		t.Fatal(err)
	}
	if backend.RecoveryLimits() != before {
		t.Fatalf("a platform that said nothing changed the limits: %+v -> %+v", before, backend.RecoveryLimits())
	}
}

func TestLimitsThatCannotMeanAnythingStopTheStartAndSayWhy(t *testing.T) {
	backend := openBackend(t)
	err := openWorkflowEngineOn(context.Background(), backend, limitedPlatform{limits: capabilities.HostLimits{MaxConcurrentReads: 2, MaxReadsPerDestination: 5}})
	if err == nil {
		t.Fatal("a platform declared a per-destination limit above the global one and the process started anyway")
	}
}
