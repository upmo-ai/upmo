// Package sandboxd is the privileged process tree. It may import its own
// internals.
package sandboxd

import "example.com/fixture/internal/sandboxd/driver"

var _ = driver.Name

const Name = "sandboxd"
