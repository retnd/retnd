package app

import (
	"context"
	"time"

	"github.com/retnd/retnd/core/internal/copycrypt"
	"github.com/retnd/retnd/core/internal/obs"
)

// defaultSuiteNoter is the slice of the journal that remembers which build
// default suite this deployment last ran under (migration 0021).
// *state.Journal satisfies it; a journal that does not is one with nothing
// to announce.
type defaultSuiteNoter interface {
	NoteDefaultSuite(ctx context.Context, current string, at time.Time) (previous string, changed bool, err error)
}

// noteDefaultSuite emits EventEncryptionDefaultSuiteChanged on the first
// cycle under a build whose copycrypt.Default() differs from the one this
// deployment last ran under, once per change and never on a steady build
// (EPIC T, #1314). It mints no key and changes no policy: a deployment
// with an active key keeps that key's suite (ADR 0033 D4). A journal that
// cannot answer is logged as an error and the cycle goes on, because an
// announcement is not worth a backup.
func (s *Service) noteDefaultSuite(ctx context.Context, at time.Time) {
	n, ok := s.Journal.(defaultSuiteNoter)
	if !ok {
		return
	}
	current := copycrypt.Default().ID()
	previous, changed, err := n.NoteDefaultSuite(ctx, current, at)
	switch {
	case err != nil:
		s.logger().EncryptionDefaultSuiteChanged(ctx, previous, current, obs.ResultError)
	case changed:
		s.logger().EncryptionDefaultSuiteChanged(ctx, previous, current, obs.ResultSuccess)
	}
}
