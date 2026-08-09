package domain

import (
	"errors"

	"github.com/upmo-ai/upmo/internal/kernel"
)

// ErrNoSuchPerson is an unknown or disabled person.
var ErrNoSuchPerson = errors.New("identity: no such person")

// User is an enabled person.
type User struct {
	ID    UserID
	Email string
	Name  string
}

// NewUser creates a new User with a fresh ID.
func NewUser(email, name string) (User, error) {
	id, err := kernel.NewID[userID]()
	if err != nil {
		return User{}, err
	}
	return User{
		ID:    id,
		Email: email,
		Name:  name,
	}, nil
}
