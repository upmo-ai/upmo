package domain

import (
	"errors"
	"regexp"
	"strings"
)

// ReplyRunes is the longest reply a model may add to a chat.
const ReplyRunes = 32000

// Line statuses. A reply is done, or failed when it broke off.
const (
	StatusDone   = "done"
	StatusFailed = "failed"
)

// Roles of a line.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ErrRoute is a route name that cannot be one.
var ErrRoute = errors.New("chat: route")

var routeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Route trims the route a person picked for a line. Empty is allowed and
// means the default route.
func Route(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name != "" && !routeName.MatchString(name) {
		return "", ErrRoute
	}
	return name, nil
}

// Attempt is one model a reply tried and the class of the failure that ended
// it. It is stored on a failed line and sent as a notice before a fallback.
type Attempt struct {
	Model string `json:"model"`
	Class string `json:"class"`
}

// Reason is the phrase a person sees for a failure class. It never carries a
// provider's own words.
func Reason(class string) string {
	switch class {
	case "rate_limit":
		return "rate limited"
	case "transient":
		return "not reachable"
	case "credential":
		return "key rejected"
	case "model_access", "overloaded":
		return "model unavailable"
	case "timeout":
		return "timed out"
	case "access":
		return "you no longer have access to this chat"
	case "unwritten":
		return "the reply could not be written"
	case "no_replies":
		return "replies are not set up"
	default:
		return "stopped"
	}
}
