package domain

// FailureClass is the one reason a provider attempt failed. Dispatch
// consults it; code never special-cases a provider name.
type FailureClass string

const (
	ClassOK          FailureClass = ""
	ClassTransient   FailureClass = "transient"
	ClassRateLimit   FailureClass = "rate_limit"
	ClassOverloaded  FailureClass = "overloaded"
	ClassTimeout     FailureClass = "timeout"
	ClassCredential  FailureClass = "credential"
	ClassModelAccess FailureClass = "model_access"
	ClassCaller      FailureClass = "caller"
	ClassCanceled    FailureClass = "canceled"
	// ClassInternal is a gateway fault, such as a panic in an adapter; it
	// is never retried and never advances the route.
	ClassInternal FailureClass = "internal"
)

// Action is what dispatch does after a failed attempt.
type Action int

const (
	ActionFail Action = iota
	ActionRetry
	ActionNext
)

// NextAction is the failover table. Transient, rate-limit, overloaded and
// timeout errors retry the link, then advance. Credential and model-access
// errors skip the link at once. Caller faults and cancellation never advance.
func NextAction(class FailureClass, retriesLeft int) Action {
	switch class {
	case ClassOK:
		return ActionFail
	case ClassCaller, ClassCanceled, ClassInternal:
		return ActionFail
	case ClassCredential, ClassModelAccess:
		return ActionNext
	case ClassTransient, ClassRateLimit, ClassOverloaded, ClassTimeout:
		if retriesLeft > 0 {
			return ActionRetry
		}
		return ActionNext
	default:
		return ActionFail
	}
}
