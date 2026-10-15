package local

import (
	"fmt"
)

// Setting the administrator's password from the command line, without the
// browser (issue #1155).
//
// This exists for the account that is locked out: a forgotten password on a
// deployment with no mail server configured has no way back in through the
// browser, and create-admin is single-shot by design (Store.Enroll refuses
// once an administrator exists), so it cannot be the way back.
//
// The trust argument is CreateAdmin's, unchanged: this is not reachable from
// the network, it needs write access to the store path, and that is a
// boundary this product already hands everything else to. Somebody who has
// it already owns what the account could reach.
//
// # Sessions
//
// Sessions live in a running server's memory and nowhere on disk, and a
// running server holds the exclusive store lock for its whole life. So this
// takes the same lock and refuses rather than waits: the only process that
// could be holding a signed-in session is the one that makes this refuse.
// Every session therefore ends with the server being stopped, which is the
// revocation, and nothing here has to find and kill one.

// SetPasswordConfig is the input to SetAdminPassword.
type SetPasswordConfig struct {
	// StorePath is the same administrator store a Service and CreateAdmin
	// use.
	StorePath string

	// Username must name the administrator already on record. It is a
	// check and not a selector (there is only ever one account), and it
	// is required so that a typo in a runbook fails loudly instead of
	// silently resetting whichever account happens to exist.
	Username string

	// Password is the new password, held in memory only. It is validated
	// against the same minimum the browser flow enforces and is persisted
	// only as hashPassword's output.
	Password string
}

// SetAdminPassword replaces the administrator's password hash, leaving the
// username, the creation time and the recovery settings alone.
func SetAdminPassword(cfg SetPasswordConfig) error {
	if cfg.StorePath == "" {
		return fmt.Errorf("local: SetPasswordConfig.StorePath is required")
	}
	if cfg.Username == "" {
		return fmt.Errorf("local: a username is required")
	}
	if len(cfg.Password) < minPasswordLength {
		return fmt.Errorf("local: password must be at least %d characters", minPasswordLength)
	}

	lock, err := acquireStoreLock(cfg.StorePath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.release() }()

	store := NewStore(cfg.StorePath)
	admin, err := store.Admin()
	if err != nil {
		return err
	}
	if admin == nil {
		return ErrNotEnrolled
	}
	if admin.Username != cfg.Username {
		// Offline and under filesystem access, so naming the real account
		// is not an oracle: whoever can run this can read the file. It is
		// what makes the typo fixable.
		return fmt.Errorf("local: %q is not the administrator of this store (the account is %q)", cfg.Username, admin.Username)
	}

	hash, err := hashPassword(cfg.Password)
	if err != nil {
		return fmt.Errorf("local: hash password: %w", err)
	}
	return store.SetPassword(hash)
}
