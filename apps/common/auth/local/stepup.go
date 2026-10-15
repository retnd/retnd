package local

import (
	"context"
	"errors"
)

// Step-up re-authentication (issue #1163).
//
// A restore on a local-account profile writes a tree into a directory
// somebody named. A live session cookie is enough to see the interface and
// not enough for that: a stolen or left-open session should not be able to
// do it, so the restore asks for the administrator's password again, and
// this is the check.
//
// It is the same comparison password rotation and the recovery settings use
// (verifyPassword against the stored hash), with three differences. It
// changes nothing: no session is rotated or revoked, and the store is only
// read. It is rate limited on its own, across the whole process rather than
// per IP, because the caller already holds a session and the thing being
// protected is the password; the limit holds for the right password as well,
// since a limiter that lets the correct guess through is one a patient
// attacker walks past. And it says only yes or no: the errors carry no text
// from the request, so nothing here can echo a password into a log.

// ErrStepUpRefused is every way a step-up check can fail, as one value: a
// wrong password, an empty one, a name that is not the administrator's, a
// store that cannot be read. Distinguishing them would tell a caller which
// of the two halves of a guess was right.
var ErrStepUpRefused = errors.New("local: step-up re-authentication refused")

// ErrStepUpRateLimited is returned when too many step-up checks were asked
// for in the window, before the password is looked at.
var ErrStepUpRateLimited = errors.New("local: too many step-up attempts")

// VerifyStepUp reports nil when password is the stored password of the
// administrator named username. It satisfies webhost.StepUpVerifier.
func (s *Service) VerifyStepUp(_ context.Context, username, password string) error {
	if !s.stepUpLimiter.Allow("step-up") {
		return ErrStepUpRateLimited
	}
	if username == "" || password == "" {
		return ErrStepUpRefused
	}
	admin, err := s.store.Admin()
	if err != nil || admin == nil || admin.Username != username {
		return ErrStepUpRefused
	}
	if err := verifyPassword(admin.PasswordHash, password); err != nil {
		return ErrStepUpRefused
	}
	return nil
}
