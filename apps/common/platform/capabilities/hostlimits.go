package capabilities

// Stub until hostlimits_test.go is red.

type HostLimits struct {
	StagingReserveBytes    int64
	MaxConcurrentReads     int
	MaxReadsPerDestination int
	MaxDownloadStreams     int
}

type HostLimitsProvider interface{ HostLimits() HostLimits }

func LimitsOf(PlatformAdapter) (HostLimits, error) { return HostLimits{StagingReserveBytes: 1}, nil }

func (BasePlatformAdapter) HostLimits() HostLimits { return HostLimits{StagingReserveBytes: 1} }
