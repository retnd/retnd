package capabilities_test

import (
	"testing"

	"github.com/retnd/retnd/apps/common/platform/capabilities"
)

// Recovery and scrubbing spend host resources that every provider shares: the
// staging filesystem's free space, how many full reads the disks and the link
// can take at once, how many downloads one process streams. EPIC Q (#983)
// puts those through this contract and not into any one provider, so a
// provider that knows its host is smaller than the default can say so once and
// every backup set is held to it.
//
// The question the engine asks is optional on purpose. The contract says in
// its own docs that providers implement it two ways (embedding
// BasePlatformAdapter, or by hand and returning nil), and making this a
// method on PlatformAdapter would break every one that does it by hand.

type limitsAdapter struct {
	fakeAdapter
	limits capabilities.HostLimits
}

func (l limitsAdapter) HostLimits() capabilities.HostLimits { return l.limits }

func TestAPlatformThatSaysNothingHasNoOpinionAboutHostLimits(t *testing.T) {
	for name, p := range map[string]capabilities.PlatformAdapter{
		"hand written, no method": fakeAdapter{},
		"embeds the base":         minimalAdapter{},
	} {
		got, err := capabilities.LimitsOf(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != (capabilities.HostLimits{}) {
			t.Errorf("%s: %+v, want no opinion (all zero) so the engine's defaults apply", name, got)
		}
	}
	if got, err := capabilities.LimitsOf(nil); err != nil || got != (capabilities.HostLimits{}) {
		t.Fatalf("a nil platform: %+v, %v", got, err)
	}
}

func TestAPlatformMayAdjustHostLimitsForEverySetAtOnce(t *testing.T) {
	want := capabilities.HostLimits{StagingReserveBytes: 8 << 30, MaxConcurrentReads: 2, MaxReadsPerDestination: 1, MaxDownloadStreams: 1}
	got, err := capabilities.LimitsOf(limitsAdapter{limits: want})
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestHostLimitsThatCannotMeanAnythingAreRefusedNotQuietlyIgnored(t *testing.T) {
	for name, l := range map[string]capabilities.HostLimits{
		"negative reserve":          {StagingReserveBytes: -1},
		"negative reads":            {MaxConcurrentReads: -1},
		"negative per destination":  {MaxReadsPerDestination: -2},
		"negative streams":          {MaxDownloadStreams: -3},
		"per destination above all": {MaxConcurrentReads: 2, MaxReadsPerDestination: 3},
	} {
		if _, err := capabilities.LimitsOf(limitsAdapter{limits: l}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A destination limit with no global one set is fine: the global limit
	// is then the engine's default, and the service checks the pair.
	if _, err := capabilities.LimitsOf(limitsAdapter{limits: capabilities.HostLimits{MaxReadsPerDestination: 9}}); err != nil {
		t.Errorf("a lone per-destination limit was refused: %v", err)
	}
}

func TestTheBaseAdapterCarriesNoLimitsOfItsOwn(t *testing.T) {
	var base capabilities.BasePlatformAdapter
	if base.HostLimits() != (capabilities.HostLimits{}) {
		t.Fatal("the base adapter has an opinion about the host it knows nothing about")
	}
}
