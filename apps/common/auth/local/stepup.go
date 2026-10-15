package local

import (
	"context"
	"errors"
)

// ErrStepUpRefused is every way a step-up check can fail, as one value.
var ErrStepUpRefused = errors.New("local: step-up re-authentication refused")

// ErrStepUpRateLimited is returned when too many step-up checks were asked
// for in the window.
var ErrStepUpRateLimited = errors.New("local: too many step-up attempts")

// VerifyStepUp is a stub until the implementation lands.
func (s *Service) VerifyStepUp(ctx context.Context, username, password string) error {
	return ErrStepUpRefused
}
