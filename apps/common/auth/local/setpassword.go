package local

import "errors"

// SetPasswordConfig is the input to SetAdminPassword.
type SetPasswordConfig struct {
	StorePath string
	Username  string
	Password  string
}

// SetAdminPassword is a stub until #1155 lands, so the tests written for it
// fail on an assertion and not on a build error.
func SetAdminPassword(SetPasswordConfig) error {
	return errors.New("local: SetAdminPassword is not implemented")
}
