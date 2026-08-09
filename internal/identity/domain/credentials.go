package domain

import "errors"

// ErrLastPasskey refuses removal of the only way to sign in.
var ErrLastPasskey = errors.New("identity: the last way to sign in cannot be removed")

// Credentials holds the sign-in methods for one person.
type Credentials struct {
	Passkeys    int
	HasPassword bool
}

// AddPasskey notes one more passkey.
func (c *Credentials) AddPasskey() {
	c.Passkeys++
}

// SetPassword notes a password is present.
func (c *Credentials) SetPassword() {
	c.HasPassword = true
}

// RemovePasskey removes one passkey, or returns ErrLastPasskey when it is the only way left to sign in.
func (c *Credentials) RemovePasskey() error {
	if c.Passkeys == 0 || (c.Passkeys == 1 && !c.HasPassword) {
		return ErrLastPasskey
	}
	c.Passkeys--
	return nil
}

// RemovePassword clears the password, or returns ErrLastPasskey when no passkeys remain.
func (c *Credentials) RemovePassword() error {
	if !c.HasPassword || c.Passkeys == 0 {
		return ErrLastPasskey
	}
	c.HasPassword = false
	return nil
}
