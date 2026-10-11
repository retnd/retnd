package capabilities

import "fmt"

// HostLimits are facts about the host that every provider shares and that
// recovery and scrubbing must respect (EPIC Q, FR-74): how much free space the
// staging filesystem must keep, how many full reads of retained copies the
// disks and the link can take at once, and how many downloads one process
// streams. They are properties of the host and the process, never of one
// backup set, which is why they sit in this contract and not in a provider
// and not in a set's configuration.
//
// Zero in any field means "no opinion": the engine's own default applies.
// That is the least-privilege direction, the same one PlatformCapabilities
// takes, and it means a provider that has never heard of this type behaves
// exactly as it did before it existed.
type HostLimits struct {
	// StagingReserveBytes is the free space that must remain on the
	// staging filesystem after a copy is staged.
	StagingReserveBytes int64

	// MaxConcurrentReads bounds full reads of retained copies running at
	// once, across every destination.
	MaxConcurrentReads int

	// MaxReadsPerDestination bounds the same against one destination.
	MaxReadsPerDestination int

	// MaxDownloadStreams bounds browser downloads in progress.
	MaxDownloadStreams int
}

// Validate refuses limits that cannot mean anything. A negative value is a
// mistake, and treating it as "no opinion" would hide it.
func (h HostLimits) Validate() error {
	switch {
	case h.StagingReserveBytes < 0:
		return fmt.Errorf("capabilities: a negative staging reserve (%d)", h.StagingReserveBytes)
	case h.MaxConcurrentReads < 0:
		return fmt.Errorf("capabilities: a negative concurrent-read limit (%d)", h.MaxConcurrentReads)
	case h.MaxReadsPerDestination < 0:
		return fmt.Errorf("capabilities: a negative per-destination read limit (%d)", h.MaxReadsPerDestination)
	case h.MaxDownloadStreams < 0:
		return fmt.Errorf("capabilities: a negative download limit (%d)", h.MaxDownloadStreams)
	case h.MaxConcurrentReads > 0 && h.MaxReadsPerDestination > h.MaxConcurrentReads:
		return fmt.Errorf("capabilities: a per-destination read limit (%d) above the global one (%d) could never bind",
			h.MaxReadsPerDestination, h.MaxConcurrentReads)
	}
	return nil
}

// HostLimitsProvider is the optional question the engine asks a platform. It
// is a separate interface, discovered by type assertion, rather than a method
// on PlatformAdapter: this package's own docs say providers implement the
// contract two ways (embedding BasePlatformAdapter, or by hand returning nil),
// and a new method on the interface would break every one that does it by
// hand. BasePlatformAdapter implements it with no opinion, so embedding it
// costs nothing.
type HostLimitsProvider interface {
	HostLimits() HostLimits
}

// LimitsOf returns what p says about its host, validated. A nil platform, or
// one that does not implement HostLimitsProvider, has no opinion.
func LimitsOf(p PlatformAdapter) (HostLimits, error) {
	hp, ok := p.(HostLimitsProvider)
	if !ok || p == nil {
		return HostLimits{}, nil
	}
	l := hp.HostLimits()
	if err := l.Validate(); err != nil {
		return HostLimits{}, err
	}
	return l, nil
}

// HostLimits is BasePlatformAdapter's answer: no opinion.
func (BasePlatformAdapter) HostLimits() HostLimits { return HostLimits{} }
