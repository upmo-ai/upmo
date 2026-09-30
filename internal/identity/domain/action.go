package domain

// ActionRules are optional per-action policies layered on a connector grant.
// An empty field means no rule for that action, so the grant decides.
type ActionRules struct {
	Read   ChangePolicy
	Change ChangePolicy
	Delete ChangePolicy
}

// DecideAction applies the grant scope, then any per-action rule, for one
// action on one resource. Out of scope always blocks. Delete in scope always
// asks, whatever rules.Delete says. An empty rule falls back to
// ConnectorDecide. An unknown action or an unrecognised policy blocks.
func DecideAction(grant ConnectorGrant, rules ActionRules, action ConnectorAction, resource string) ConnectorVerdict {
	if !inScope(grant, resource) {
		return ConnectorBlock
	}
	var policy ChangePolicy
	switch action {
	case ConnectorDelete:
		return ConnectorAsk
	case ConnectorRead:
		policy = rules.Read
	case ConnectorChange:
		policy = rules.Change
	default:
		return ConnectorBlock
	}
	switch policy {
	case "":
		return ConnectorDecide(grant, action, resource)
	case ChangesAllow:
		return ConnectorAllow
	case ChangesAsk:
		return ConnectorAsk
	default:
		return ConnectorBlock
	}
}
