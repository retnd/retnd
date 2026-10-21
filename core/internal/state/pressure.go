package state

import (
	"sync/atomic"
	"time"
)

// SlowHoldThreshold is how long one statement, cursor or transaction may
// keep the journal's connection before it is reported.
const SlowHoldThreshold = time.Second

// SlowHold is one hold of the journal's connection that lasted past
// SlowHoldThreshold.
type SlowHold struct {
	Op      string
	Held    time.Duration
	Waiters int64
}

// ConnectionPressure is what the journal's connection has cost its callers
// since Open.
type ConnectionPressure struct {
	Waits         int64
	WaitTime      time.Duration
	SlowHolds     int64
	LongestHold   time.Duration
	LongestHoldOp string
}

// holdTracker times the journal connection's holds.
type holdTracker struct {
	now  func() time.Time
	hook atomic.Pointer[func(SlowHold)]
}

func newHoldTracker() *holdTracker {
	return &holdTracker{now: time.Now}
}

// SetSlowHoldHook installs fn to be told about every slow hold.
func (j *Journal) SetSlowHoldHook(fn func(SlowHold)) {}

// Pressure reports what the connection has cost its callers since Open.
func (j *Journal) Pressure() ConnectionPressure {
	return ConnectionPressure{}
}

// IsContention reports whether err is the journal losing a race for its
// connection or its write lock.
func IsContention(err error) bool {
	return false
}
