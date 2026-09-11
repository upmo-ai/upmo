package domain

const UserCap = 2

// Room is true when this person has fewer than UserCap admitted runs.
func Room(active int) bool {
	return active >= 0 && active < UserCap
}

type RunState string

const (
	RunQueued   RunState = "queued"
	RunAdmitted RunState = "admitted"
	RunEnded    RunState = "ended"
)

type RunKind string

const (
	RunAdmit RunKind = "admit"
	RunEnd   RunKind = "end"
)

type RunInput struct {
	Kind RunKind
	Room bool
}

// RunStep returns the next run state for an input.
func RunStep(state RunState, input RunInput) (RunState, error) {
	switch state {
	case RunEnded:
		return state, ErrTerminal
	case "":
		switch input.Kind {
		case RunAdmit:
			if input.Room {
				return RunAdmitted, nil
			}
			return RunQueued, nil
		case RunEnd:
			return state, ErrInput
		default:
			return state, ErrInput
		}
	case RunQueued:
		switch input.Kind {
		case RunAdmit:
			if input.Room {
				return RunAdmitted, nil
			}
			return RunQueued, nil
		case RunEnd:
			return RunEnded, nil
		default:
			return state, ErrInput
		}
	case RunAdmitted:
		switch input.Kind {
		case RunAdmit:
			return RunAdmitted, nil
		case RunEnd:
			return RunEnded, nil
		default:
			return state, ErrInput
		}
	default:
		return state, ErrInput
	}
}
