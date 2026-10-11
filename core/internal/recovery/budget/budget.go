package budget

import (
	"errors"
	"time"

	"github.com/retnd/retnd/core/internal/placement"
)

// Stub: signatures only, so the tests in budget_test.go fail for the right
// reason before the implementation lands.

type Name string

const (
	ConcurrentReads  Name = "concurrent_reads"
	DestinationReads Name = "destination_reads"
	StagingReserve   Name = "staging_reserve"
	TickWork         Name = "tick_work"
	ValidatorOutput  Name = "validator_output"
	DownloadStreams  Name = "download_streams"
	ScheduledEgress  Name = "scheduled_egress"
)

var All = []Name{}

func ExitCode(Name) int { return 0 }

var ErrOverBudget = errors.New("budget: over budget")

type Refusal struct {
	Budget      Name
	Limit       int64
	Destination string
}

func (r *Refusal) Error() string        { return "" }
func (r *Refusal) Is(target error) bool { return false }

type Limits struct {
	MaxConcurrentReads     int
	MaxReadsPerDestination int
	StagingReserveBytes    int64
	MaxCopiesPerTick       int
	ValidatorOutputBytes   int64
	MaxDownloadStreams     int
}

const (
	DefaultMaxConcurrentReads     = 0
	DefaultMaxReadsPerDestination = 0
	DefaultStagingReserveBytes    = 0
	DefaultMaxCopiesPerTick       = 0
	DefaultValidatorOutputBytes   = 0
	DefaultMaxDownloadStreams     = 0
)

func Defaults() Limits                { return Limits{} }
func (l Limits) WithDefaults() Limits { return l }
func (l Limits) Validate() error      { return nil }

func ScheduledCeiling() placement.Class { return placement.Content }

type Gate struct{}

func NewGate(Name, int) *Gate            { return &Gate{} }
func (g *Gate) Acquire() (func(), error) { return func() {}, nil }
func (g *Gate) InFlight() int            { return 0 }
func (g *Gate) Peak() int                { return 0 }

type Reads struct{}

func NewReads(Limits) *Reads                    { return &Reads{} }
func (r *Reads) Acquire(string) (func(), error) { return func() {}, nil }
func (r *Reads) Peaks() (int, map[string]int)   { return 0, nil }

type Candidate struct {
	ID          string
	Destination string
	Due         time.Time
}

func SelectTick(due []Candidate, l Limits) (considered, deferred []Candidate) { return due, nil }
