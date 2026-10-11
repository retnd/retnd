package export

import (
	"errors"
	"io"
	"os"
	"time"
)

var (
	ErrBelowReserve      = errors.New("recovery: staging disk is below its reserve")
	ErrTargetOutsideRoot = errors.New("recovery: target is outside the recovery staging root")
	ErrTargetExists      = errors.New("recovery: target already exists")
)

type File interface {
	io.Writer
	io.Closer
	Name() string
	Sync() error
}

type Staging struct {
	Root       string
	PartialTTL time.Duration
	OutputTTL  time.Duration
	Reserve    int64
	Now        func() time.Time
	FreeBytes  func(dir string) (int64, error)
	OpenFile   func(path string) (File, error)
	Remove     func(path string) error
}

type CleanupFailure struct {
	Path string
	Err  error
}

type SweepReport struct {
	Removed []string
	Failed  []CleanupFailure
}

type CleanupError struct{ Failures []CleanupFailure }

func (e *CleanupError) Error() string { return "recovery: staging cleanup failed" }

func (s *Staging) Begin(need int64) (File, error)                  { return nil, nil }
func (s *Staging) Sweep() (SweepReport, error)                     { return SweepReport{}, nil }
func (s *Staging) Reconcile() (SweepReport, error)                 { return SweepReport{}, nil }
func (s *Staging) Entries() ([]string, error)                      { return nil, nil }
func (s *Staging) Publish(from string, rel string) (string, error) { return "", nil }

var _ = os.O_EXCL
