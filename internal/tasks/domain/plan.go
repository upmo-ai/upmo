package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// Flow is the verification depth of a task.
type Flow string

const (
	FlowLight    Flow = "light"
	FlowStandard Flow = "standard"
	FlowFull     Flow = "full"
)

// PlanApproval is the plan approval preference on an agent or task.
type PlanApproval string

const (
	PlanApprovalAlways PlanApproval = "always"
	PlanApprovalRisky  PlanApproval = "risky"
	PlanApprovalNever  PlanApproval = "never"
)

// CheckKind names one check kind from the closed verification set.
type CheckKind string

const (
	CheckExists   CheckKind = "exists"
	CheckCommand  CheckKind = "command"
	CheckCitation CheckKind = "citation"
	CheckRule     CheckKind = "rule"
)

// CriterionSource ranks who authored a criterion: template > person > model.
type CriterionSource string

const (
	SourceTemplate CriterionSource = "template"
	SourcePerson   CriterionSource = "person"
	SourceModel    CriterionSource = "model"
)

// Verdict is the outcome of running one check.
type Verdict string

const (
	VerdictPass    Verdict = "pass"
	VerdictFail    Verdict = "fail"
	VerdictUnknown Verdict = "unknown"
)

// FindingState is the lifecycle state of a row in tasks.review_findings.
type FindingState string

const (
	FindingOpen      FindingState = "open"
	FindingFixed     FindingState = "fixed"
	FindingAccepted  FindingState = "accepted"
	FindingDismissed FindingState = "dismissed"
)

const (
	MinUnits           = 1
	MaxUnits           = 12
	MinCriteria        = 2
	MinCriteriaLight   = 1
	MaxCriteria        = 6
	UnitTitleRunes     = 200
	CriterionTextRunes = 400
	CheckTargetRunes   = 400
	CheckExcerptRunes  = 4000
	StallRounds        = 2
	FailureLimit       = 3
	ReviewRounds       = 3
)

var ErrPlan = errors.New("tasks: plan")

// Criterion is one testable requirement on a unit, bound to one check kind.
type Criterion struct {
	Criterion int             `json:"criterion"`
	Text      string          `json:"text"`
	Kind      CheckKind       `json:"kind"`
	Source    CriterionSource `json:"source"`
	Target    string          `json:"target,omitempty"`
	Digest    string          `json:"digest,omitempty"`
}

// Unit is one scoped unit of work in a task plan.
type Unit struct {
	Unit     int         `json:"unit"`
	Title    string      `json:"title"`
	Criteria []Criterion `json:"criteria"`
}

// TaskPlan is the validated plan stored in tasks.tasks.plan.
type TaskPlan struct {
	Flow  Flow   `json:"flow"`
	Units []Unit `json:"units"`
}

// ReviewFinding is one reviewer finding stored in tasks.review_findings.
type ReviewFinding struct {
	Seq       int          `json:"seq"`
	Unit      int          `json:"unit"`
	Criterion int          `json:"criterion"`
	Title     string       `json:"title"`
	Detail    string       `json:"detail"`
	Evidence  string       `json:"evidence"`
	Blocking  bool         `json:"blocking"`
	State     FindingState `json:"state"`
	Round     int          `json:"round"`
	SettledBy string       `json:"settled_by,omitempty"`
}

// DueCheck is one criterion that needs verification on the current pass.
type DueCheck struct {
	Unit        int
	Criterion   int
	Kind        CheckKind
	Text        string
	Target      string
	Digest      string
	PriorPassed bool
}

// BrakeState holds the task's current loop-brake counters and review round.
type BrakeState struct {
	Stalls      int
	Failures    int
	Replanned   bool
	LastGap     string
	ReviewRound int
	HasUnknown  bool
}

// BrakeAction is the next action Brake selects.
type BrakeAction string

const (
	BrakeContinue BrakeAction = "continue"
	BrakeReplan   BrakeAction = "replan"
	BrakeHold     BrakeAction = "hold"
)

// BrakeDecision is the deterministic outcome of Brake.
type BrakeDecision struct {
	Action    BrakeAction
	Reason    string
	Stalls    int
	Failures  int
	Replanned bool
	LastGap   string
}

// Facts are the floor signals and decider output PlanNeedsPerson evaluates.
type Facts struct {
	HasImpact     bool
	Tainted       bool
	OverBudget    bool
	DigestChanged bool
	FirstPlan     bool
	DeciderAsk    bool
}

// AcceptPlan normalizes a plan, keeps locked template and person criteria, and
// returns the canonical digest stored on the task.
func AcceptPlan(flow Flow, units []Unit, locked []Criterion) (TaskPlan, string, error) {
	if flow == "" {
		flow = FlowStandard
	}
	switch flow {
	case FlowLight, FlowStandard, FlowFull:
	default:
		return TaskPlan{}, "", ErrPlan
	}
	if len(units) < MinUnits || len(units) > MaxUnits {
		return TaskPlan{}, "", ErrPlan
	}
	minCriteria := MinCriteria
	if flow == FlowLight {
		minCriteria = MinCriteriaLight
	}
	out := make([]Unit, len(units))
	for i, unit := range units {
		title := strings.TrimSpace(unit.Title)
		if title == "" || utf8.RuneCountInString(title) > UnitTitleRunes {
			return TaskPlan{}, "", ErrPlan
		}
		if len(unit.Criteria) < minCriteria || len(unit.Criteria) > MaxCriteria {
			return TaskPlan{}, "", ErrPlan
		}
		criteria := make([]Criterion, len(unit.Criteria))
		for j, crit := range unit.Criteria {
			next, err := normalizeCriterion(j+1, crit)
			if err != nil {
				return TaskPlan{}, "", err
			}
			criteria[j] = next
		}
		out[i] = Unit{Unit: i + 1, Title: title, Criteria: criteria}
	}
	if err := keepLocked(out, locked); err != nil {
		return TaskPlan{}, "", err
	}
	plan := TaskPlan{Flow: flow, Units: out}
	raw, err := canonicalPlan(plan)
	if err != nil {
		return TaskPlan{}, "", ErrPlan
	}
	sum := sha256.Sum256(raw)
	return plan, hex.EncodeToString(sum[:]), nil
}

func normalizeCriterion(n int, crit Criterion) (Criterion, error) {
	text := strings.TrimSpace(crit.Text)
	if text == "" || utf8.RuneCountInString(text) > CriterionTextRunes {
		return Criterion{}, ErrPlan
	}
	target := strings.TrimSpace(crit.Target)
	if utf8.RuneCountInString(target) > CheckTargetRunes {
		return Criterion{}, ErrPlan
	}
	switch crit.Kind {
	case CheckExists, CheckCommand:
		if target == "" {
			return Criterion{}, ErrPlan
		}
	case CheckCitation, CheckRule:
	default:
		return Criterion{}, ErrPlan
	}
	source := crit.Source
	if source == "" {
		source = SourceModel
	}
	switch source {
	case SourceTemplate, SourcePerson, SourceModel:
	default:
		return Criterion{}, ErrPlan
	}
	next := Criterion{
		Criterion: n,
		Text:      text,
		Kind:      crit.Kind,
		Source:    source,
		Target:    target,
	}
	next.Digest = CheckDigest(next, "")
	return next, nil
}

func keepLocked(units []Unit, locked []Criterion) error {
	used := map[[2]int]bool{}
	for _, want := range locked {
		if want.Source != SourceTemplate && want.Source != SourcePerson {
			continue
		}
		text := strings.TrimSpace(want.Text)
		target := strings.TrimSpace(want.Target)
		found := false
		for _, unit := range units {
			for _, got := range unit.Criteria {
				key := [2]int{unit.Unit, got.Criterion}
				if used[key] {
					continue
				}
				if got.Text != text || got.Kind != want.Kind || got.Target != target {
					continue
				}
				if sourceRank(got.Source) < sourceRank(want.Source) {
					continue
				}
				used[key] = true
				found = true
				break
			}
			if found {
				break
			}
		}
		if !found {
			return ErrPlan
		}
	}
	return nil
}

func sourceRank(source CriterionSource) int {
	switch source {
	case SourceTemplate:
		return 3
	case SourcePerson:
		return 2
	case SourceModel:
		return 1
	default:
		return 0
	}
}

func canonicalPlan(plan TaskPlan) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(plan); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// CheckDigest is the lowercase hex SHA-256 of kind, text, target, and the
// input digest, joined by newlines. AcceptPlan stores it with an empty input.
func CheckDigest(c Criterion, inputDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(c.Kind),
		strings.TrimSpace(c.Text),
		strings.TrimSpace(c.Target),
		strings.TrimSpace(inputDigest),
	}, "\n")))
	return hex.EncodeToString(sum[:])
}

type pairKey struct {
	unit      int
	criterion int
}

// Due returns the criteria that still need a check, cheapest kind first.
func Due(plan TaskPlan, checks []Check, findings []ReviewFinding, inputs map[int]string) []DueCheck {
	latest := latestChecks(checks)
	openBlock, accepted := findingIndex(findings)
	var due []DueCheck
	for _, unit := range plan.Units {
		for _, crit := range unit.Criteria {
			key := pairKey{unit.Unit, crit.Criterion}
			if accepted[key] && !openBlock[key] {
				continue
			}
			want := CheckDigest(crit, inputs[unit.Unit])
			check, found := latest[key]
			if found && check.Verdict == VerdictPass && check.Code == 0 && check.Digest != "" && check.Digest == want && !openBlock[key] {
				continue
			}
			due = append(due, DueCheck{
				Unit:        unit.Unit,
				Criterion:   crit.Criterion,
				Kind:        crit.Kind,
				Text:        crit.Text,
				Target:      crit.Target,
				Digest:      want,
				PriorPassed: found && check.Verdict == VerdictPass,
			})
		}
	}
	slices.SortFunc(due, func(a, b DueCheck) int {
		if rank := kindRank(a.Kind) - kindRank(b.Kind); rank != 0 {
			return rank
		}
		if a.Unit != b.Unit {
			return a.Unit - b.Unit
		}
		return a.Criterion - b.Criterion
	})
	return due
}

func kindRank(kind CheckKind) int {
	switch kind {
	case CheckExists:
		return 1
	case CheckCommand, CheckCitation:
		return 2
	case CheckRule:
		return 3
	default:
		return 4
	}
}

func latestChecks(checks []Check) map[pairKey]Check {
	out := make(map[pairKey]Check, len(checks))
	order := make(map[pairKey]int, len(checks))
	for i, check := range checks {
		key := pairKey{check.Unit, check.Criterion}
		prev, ok := out[key]
		if !ok || check.Batch > prev.Batch || (check.Batch == prev.Batch && check.Seq > prev.Seq) || (check.Batch == prev.Batch && check.Seq == prev.Seq && i >= order[key]) {
			out[key] = check
			order[key] = i
		}
	}
	return out
}

func findingIndex(findings []ReviewFinding) (openBlock, accepted map[pairKey]bool) {
	openBlock = map[pairKey]bool{}
	accepted = map[pairKey]bool{}
	for _, finding := range findings {
		key := pairKey{finding.Unit, finding.Criterion}
		if finding.Blocking && finding.State == FindingOpen {
			openBlock[key] = true
		}
		if finding.State == FindingAccepted {
			accepted[key] = true
		}
	}
	return openBlock, accepted
}

// Brake selects continue, one automatic replan, or a hold. An empty gap is not a miss.
func Brake(state BrakeState, gap string) BrakeDecision {
	if state.HasUnknown {
		return BrakeDecision{
			Action: BrakeHold, Reason: ReasonUnknown,
			Stalls: state.Stalls, Failures: state.Failures,
			Replanned: state.Replanned, LastGap: state.LastGap,
		}
	}
	gap = strings.TrimSpace(gap)
	if state.ReviewRound >= ReviewRounds {
		return BrakeDecision{
			Action: BrakeHold, Reason: ReasonReviewLimit,
			Stalls: state.Stalls, Failures: state.Failures,
			Replanned: state.Replanned, LastGap: gap,
		}
	}
	stalls := state.Stalls
	switch {
	case gap != "" && gap == strings.TrimSpace(state.LastGap):
		stalls = max(state.Stalls, 1) + 1
	case gap != "":
		stalls = 1
	}
	failures := state.Failures
	if gap != "" {
		failures = state.Failures + 1
	}
	if stalls >= StallRounds {
		if !state.Replanned {
			return BrakeDecision{Action: BrakeReplan, Replanned: true}
		}
		return BrakeDecision{
			Action: BrakeHold, Reason: ReasonStalled,
			Stalls: stalls, Failures: failures, Replanned: true, LastGap: gap,
		}
	}
	if failures >= FailureLimit {
		return BrakeDecision{
			Action: BrakeHold, Reason: ReasonFailing,
			Stalls: stalls, Failures: failures, Replanned: state.Replanned, LastGap: gap,
		}
	}
	return BrakeDecision{
		Action: BrakeContinue, Stalls: stalls, Failures: failures,
		Replanned: state.Replanned, LastGap: gap,
	}
}

// Tighten returns the stricter of base and override. Empty base is risky.
// Empty override keeps base.
func Tighten(base, override PlanApproval) (PlanApproval, error) {
	if base == "" {
		base = PlanApprovalRisky
	}
	if override == "" {
		override = base
	}
	if approvalRank(base) == 0 || approvalRank(override) == 0 {
		return "", ErrPlan
	}
	if approvalRank(override) > approvalRank(base) {
		return override, nil
	}
	return base, nil
}

func approvalRank(pref PlanApproval) int {
	switch pref {
	case PlanApprovalAlways:
		return 3
	case PlanApprovalRisky:
		return 2
	case PlanApprovalNever:
		return 1
	default:
		return 0
	}
}

// PlanNeedsPerson reports whether a person must accept the plan.
// Impact, taint, budget, and a changed digest are floors.
func PlanNeedsPerson(pref PlanApproval, facts Facts) bool {
	if facts.HasImpact || facts.Tainted || facts.OverBudget || facts.DigestChanged {
		return true
	}
	switch pref {
	case PlanApprovalAlways:
		return true
	case PlanApprovalRisky, "":
		return facts.FirstPlan || facts.DeciderAsk
	case PlanApprovalNever:
		return false
	default:
		return true
	}
}

// OpenStatus is the status a new task opens in.
func OpenStatus(needsSandbox bool, flow Flow) Status {
	if needsSandbox {
		return Preparing
	}
	if flow == FlowLight {
		return Working
	}
	return Planning
}

func hexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}
