package domain

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// UsageRange is the reporting window of a usage read: today, or the last 7,
// 30 or 90 local days including today.
type UsageRange string

// The usage ranges a caller may ask for.
const (
	UsageToday  UsageRange = "today"
	UsageWeek   UsageRange = "7"
	UsageMonth  UsageRange = "30"
	UsageXMonth UsageRange = "90"
)

// ParseUsageRange reads the range query value. Empty is the last 7 days.
func ParseUsageRange(raw string) (UsageRange, error) {
	switch r := UsageRange(raw); r {
	case "":
		return UsageWeek, nil
	case UsageToday, UsageWeek, UsageMonth, UsageXMonth:
		return r, nil
	default:
		return "", Invalid("range must be today, 7, 30 or 90.")
	}
}

// MarshalJSON writes today as a string and a day count as a number, the
// shape the usage page reads.
func (r UsageRange) MarshalJSON() ([]byte, error) {
	if r == UsageToday {
		return json.Marshal(string(r))
	}
	n, err := strconv.Atoi(string(r))
	if err != nil {
		return nil, err
	}
	return json.Marshal(n)
}

// UnmarshalJSON reads either form MarshalJSON writes.
func (r *UsageRange) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	text := ""
	switch v := raw.(type) {
	case string:
		text = v
	case float64:
		text = strconv.FormatFloat(v, 'f', -1, 64)
	}
	got, err := ParseUsageRange(text)
	if err != nil || text == "" {
		return Invalid("range must be today, 7, 30 or 90.")
	}
	*r = got
	return nil
}

// UsageGroup is the key the workspace usage rows are grouped by.
type UsageGroup string

// The workspace usage groupings.
const (
	GroupPerson   UsageGroup = "person"
	GroupTeam     UsageGroup = "team"
	GroupAgent    UsageGroup = "agent"
	GroupModel    UsageGroup = "model"
	GroupProvider UsageGroup = "provider"
	GroupSource   UsageGroup = "source"
)

// ParseUsageGroup reads the group query value. Empty is person.
func ParseUsageGroup(raw string) (UsageGroup, error) {
	switch g := UsageGroup(raw); g {
	case "":
		return GroupPerson, nil
	case GroupPerson, GroupTeam, GroupAgent, GroupModel, GroupProvider, GroupSource:
		return g, nil
	default:
		return "", Invalid("group must be person, team, agent, model, provider or source.")
	}
}

// Bucket units of a usage window.
const (
	BucketHour = "hour"
	BucketDay  = "day"
)

// UsageWindow is the half-open [Start, End) span a usage read covers, with
// the bucket unit and the zone the buckets are cut in.
type UsageWindow struct {
	Range UsageRange
	Start time.Time
	End   time.Time
	Unit  string
	Zone  *time.Location
}

// UsageWindowFor returns the window of r around now in zone: today runs from
// local midnight to the next local midnight in hours, and a day count ends
// at the next local midnight and starts that many local days before it.
func UsageWindowFor(r UsageRange, now time.Time, zone *time.Location) UsageWindow {
	if zone == nil {
		zone = time.UTC
	}
	y, m, d := now.In(zone).Date()
	end := time.Date(y, m, d+1, 0, 0, 0, 0, zone)
	w := UsageWindow{Range: r, End: end.UTC(), Unit: BucketDay, Zone: zone}
	switch r {
	case UsageToday:
		w.Start = time.Date(y, m, d, 0, 0, 0, 0, zone).UTC()
		w.Unit = BucketHour
	default:
		days, _ := strconv.Atoi(string(r))
		w.Start = time.Date(y, m, d+1-days, 0, 0, 0, 0, zone).UTC()
	}
	return w
}

// Label writes a bucket start: the local date of a day, or the local hour
// with its zone offset in RFC 3339.
func (w UsageWindow) Label(at time.Time) string {
	local := at.In(w.Zone)
	if w.Unit == BucketDay {
		return local.Format(time.DateOnly)
	}
	return local.Format(time.RFC3339)
}

// Names shown for usage keys that have no visible name.
const (
	SystemName          = "System"
	UnknownPersonName   = "Unknown person"
	NoTeamName          = "No team"
	UnknownTeamName     = "Unknown team"
	NoAgentName         = "No agent"
	PrivateAgentName    = "Private agent"
	NoProviderName      = "No provider"
	PrivateProviderName = "Private provider"
)

// UsageCell is one grouped ledger total. A bucket cell has Bucket set; a keyed
// cell has Key set instead. Spend is the summed metered cost, nil when no
// metered call in the cell had a price. ListPrice is the summed catalog value
// of the subscription and local calls whose every used rate is known, nil
// when there is none.
type UsageCell struct {
	Bucket        time.Time
	Key           string
	Account       string
	Model         string
	PlanKind      string
	Requests      int64
	Errors        int64
	Input         int64
	Output        int64
	CacheRead     int64
	CacheWrite    int64
	Reasoning     int64
	SpendMicroUSD *int64
	Estimated     bool
	Unpriced      int64
	ListMicroUSD  *int64
}

// Tokens is input, output, cache read and cache write. Reasoning is part of
// output, so it is not added again.
func (c UsageCell) Tokens() int64 {
	return c.Input + c.Output + c.CacheRead + c.CacheWrite
}

// UsageAccount is a provider account as the reader sees it under row
// security. Own says it is the reader's own account and not a house key.
type UsageAccount struct {
	ID   string
	Name string
	Own  bool
}

// UsageLatency is end to end call latency percentiles in milliseconds.
type UsageLatency struct {
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
}

// LatencyMS rounds a percentile to whole milliseconds.
func LatencyMS(v float64) int64 {
	return int64(math.Round(v))
}

// UsageTaskCell is one task's totals on one model.
type UsageTaskCell struct {
	Task          string
	Model         string
	Input         int64
	Output        int64
	CacheRead     int64
	CacheWrite    int64
	SpendMicroUSD *int64
}

// UsageScan is what the store read for one usage request.
type UsageScan struct {
	Buckets  []UsageCell
	Keyed    []UsageCell
	Accounts map[string]UsageAccount
	Latency  map[string]UsageLatency
	Tasks    []UsageTaskCell
}

// addMoney adds b to a, keeping nil only while both are unknown.
func addMoney(a, b *int64) *int64 {
	if b == nil {
		return a
	}
	sum := *b
	if a != nil {
		sum += *a
	}
	return &sum
}

// UsageTotals are the figures every usage level carries. Spend is billed
// metered spend, nil when unknown and never 0 for unknown. ListPrice is the
// catalog value of subscription and local calls, which are not billed.
type UsageTotals struct {
	Requests          int64  `json:"requests"`
	Errors            int64  `json:"errors"`
	Input             int64  `json:"input"`
	Output            int64  `json:"output"`
	CacheRead         int64  `json:"cache_read"`
	CacheWrite        int64  `json:"cache_write"`
	Reasoning         int64  `json:"reasoning"`
	Tokens            int64  `json:"tokens"`
	SpendMicroUSD     *int64 `json:"spend_micro_usd"`
	Estimated         bool   `json:"estimated"`
	UnpricedCalls     int64  `json:"unpriced_calls"`
	ListPriceMicroUSD *int64 `json:"list_price_micro_usd"`
}

func (t *UsageTotals) add(c UsageCell) {
	t.Requests += c.Requests
	t.Errors += c.Errors
	t.Input += c.Input
	t.Output += c.Output
	t.CacheRead += c.CacheRead
	t.CacheWrite += c.CacheWrite
	t.Reasoning += c.Reasoning
	t.Tokens += c.Tokens()
	t.SpendMicroUSD = addMoney(t.SpendMicroUSD, c.SpendMicroUSD)
	t.Estimated = t.Estimated || c.Estimated
	t.UnpricedCalls += c.Unpriced
	t.ListPriceMicroUSD = addMoney(t.ListPriceMicroUSD, c.ListMicroUSD)
}

// UsageProviderShare is one provider's part of a bucket.
type UsageProviderShare struct {
	SpendMicroUSD *int64 `json:"spend_micro_usd"`
	Requests      int64  `json:"requests"`
	Errors        int64  `json:"errors"`
	Tokens        int64  `json:"tokens"`
}

// UsageModelShare is one model's part of a bucket.
type UsageModelShare struct {
	SpendMicroUSD *int64 `json:"spend_micro_usd"`
	Tokens        int64  `json:"tokens"`
}

// UsageBucket is one hour or day.
type UsageBucket struct {
	At                string                        `json:"at"`
	Requests          int64                         `json:"requests"`
	Errors            int64                         `json:"errors"`
	Input             int64                         `json:"input"`
	Output            int64                         `json:"output"`
	CacheRead         int64                         `json:"cache_read"`
	CacheWrite        int64                         `json:"cache_write"`
	SpendMicroUSD     *int64                        `json:"spend_micro_usd"`
	Estimated         bool                          `json:"estimated"`
	ListPriceMicroUSD *int64                        `json:"list_price_micro_usd"`
	ByProvider        map[string]UsageProviderShare `json:"by_provider"`
	ByModel           map[string]UsageModelShare    `json:"by_model"`
}

// UsageProvider is one provider account's totals.
type UsageProvider struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Scope         string       `json:"scope"`
	PlanKind      string       `json:"plan_kind"`
	SpendMicroUSD *int64       `json:"spend_micro_usd"`
	Requests      int64        `json:"requests"`
	Errors        int64        `json:"errors"`
	LatencyMS     UsageLatency `json:"latency_ms"`
}

// UsageModel is one model's totals.
type UsageModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ProviderID    string `json:"provider_id"`
	SpendMicroUSD *int64 `json:"spend_micro_usd"`
	Tokens        int64  `json:"tokens"`
}

// UsageSource is one attribution source's totals.
type UsageSource struct {
	Source        string `json:"source"`
	Requests      int64  `json:"requests"`
	Tokens        int64  `json:"tokens"`
	SpendMicroUSD *int64 `json:"spend_micro_usd"`
}

// UsageTaskModel is one task's part on one model.
type UsageTaskModel struct {
	ModelID      string `json:"model_id"`
	CostMicroUSD *int64 `json:"cost_micro_usd"`
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheRead    int64  `json:"cache_read"`
	CacheWrite   int64  `json:"cache_write"`
}

// UsageTask is one task with its child runs rolled up.
type UsageTask struct {
	TaskID       string           `json:"task_id"`
	Goal         string           `json:"goal"`
	Input        int64            `json:"input"`
	Output       int64            `json:"output"`
	CacheRead    int64            `json:"cache_read"`
	CacheWrite   int64            `json:"cache_write"`
	CostMicroUSD *int64           `json:"cost_micro_usd"`
	ByModel      []UsageTaskModel `json:"by_model"`
}

// UsageReport is the response of GET /api/usage: the reader's own usage.
type UsageReport struct {
	Range             UsageRange      `json:"range"`
	Bucket            string          `json:"bucket"`
	TimeZone          string          `json:"time_zone"`
	SpendMicroUSD     *int64          `json:"spend_micro_usd"`
	Estimated         bool            `json:"estimated"`
	UnpricedCalls     int64           `json:"unpriced_calls"`
	ListPriceMicroUSD *int64          `json:"list_price_micro_usd"`
	Buckets           []UsageBucket   `json:"buckets"`
	Providers         []UsageProvider `json:"providers"`
	Models            []UsageModel    `json:"models"`
	Sources           []UsageSource   `json:"sources"`
	Tasks             []UsageTask     `json:"tasks"`
}

// UsageRow is one workspace usage row for the chosen group.
type UsageRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	UsageTotals
}

// WorkspaceUsageReport is the response of GET /api/usage/workspace.
type WorkspaceUsageReport struct {
	Range             UsageRange    `json:"range"`
	Bucket            string        `json:"bucket"`
	TimeZone          string        `json:"time_zone"`
	Group             UsageGroup    `json:"group"`
	SpendMicroUSD     *int64        `json:"spend_micro_usd"`
	Estimated         bool          `json:"estimated"`
	UnpricedCalls     int64         `json:"unpriced_calls"`
	ListPriceMicroUSD *int64        `json:"list_price_micro_usd"`
	Buckets           []UsageBucket `json:"buckets"`
	Rows              []UsageRow    `json:"rows"`
}

// buckets folds bucket cells into ordered buckets and the grand totals.
func (s UsageScan) buckets(w UsageWindow) ([]UsageBucket, UsageTotals) {
	var total UsageTotals
	byTime := map[time.Time]*UsageBucket{}
	var order []time.Time
	for _, c := range s.Buckets {
		total.add(c)
		at := c.Bucket.UTC()
		b, ok := byTime[at]
		if !ok {
			b = &UsageBucket{
				At:         w.Label(at),
				ByProvider: map[string]UsageProviderShare{},
				ByModel:    map[string]UsageModelShare{},
			}
			byTime[at] = b
			order = append(order, at)
		}
		b.Requests += c.Requests
		b.Errors += c.Errors
		b.Input += c.Input
		b.Output += c.Output
		b.CacheRead += c.CacheRead
		b.CacheWrite += c.CacheWrite
		b.SpendMicroUSD = addMoney(b.SpendMicroUSD, c.SpendMicroUSD)
		b.Estimated = b.Estimated || c.Estimated
		b.ListPriceMicroUSD = addMoney(b.ListPriceMicroUSD, c.ListMicroUSD)
		p := b.ByProvider[c.Account]
		p.SpendMicroUSD = addMoney(p.SpendMicroUSD, c.SpendMicroUSD)
		p.Requests += c.Requests
		p.Errors += c.Errors
		p.Tokens += c.Tokens()
		b.ByProvider[c.Account] = p
		m := b.ByModel[c.Model]
		m.SpendMicroUSD = addMoney(m.SpendMicroUSD, c.SpendMicroUSD)
		m.Tokens += c.Tokens()
		b.ByModel[c.Model] = m
	}
	slices.SortFunc(order, func(a, b time.Time) int { return a.Compare(b) })
	out := make([]UsageBucket, 0, len(order))
	for _, at := range order {
		out = append(out, *byTime[at])
	}
	return out, total
}

func (s UsageScan) providerName(id string) string {
	if id == "" {
		return NoProviderName
	}
	if a, ok := s.Accounts[id]; ok {
		return a.Name
	}
	return PrivateProviderName
}

// PersonalUsage builds the reader's own report. goals names tasks by id.
func PersonalUsage(s UsageScan, w UsageWindow, goals map[string]string) UsageReport {
	buckets, total := s.buckets(w)
	report := UsageReport{
		Range:             w.Range,
		Bucket:            w.Unit,
		TimeZone:          w.Zone.String(),
		SpendMicroUSD:     total.SpendMicroUSD,
		Estimated:         total.Estimated,
		UnpricedCalls:     total.UnpricedCalls,
		ListPriceMicroUSD: total.ListPriceMicroUSD,
		Buckets:           buckets,
		Providers:         []UsageProvider{},
		Models:            []UsageModel{},
		Sources:           []UsageSource{},
		Tasks:             []UsageTask{},
	}

	providers := map[string]*UsageProvider{}
	plans := map[string]map[string]int64{}
	models := map[string]*UsageModel{}
	modelAccounts := map[string]map[string]int64{}
	for _, c := range s.Buckets {
		p, ok := providers[c.Account]
		if !ok {
			scope := "global"
			if s.Accounts[c.Account].Own {
				scope = "own"
			}
			p = &UsageProvider{
				ID:        c.Account,
				Name:      s.providerName(c.Account),
				Scope:     scope,
				LatencyMS: s.Latency[c.Account],
			}
			providers[c.Account] = p
			plans[c.Account] = map[string]int64{}
		}
		p.SpendMicroUSD = addMoney(p.SpendMicroUSD, c.SpendMicroUSD)
		p.Requests += c.Requests
		p.Errors += c.Errors
		plans[c.Account][c.PlanKind] += c.Requests

		m, ok := models[c.Model]
		if !ok {
			m = &UsageModel{ID: c.Model, Name: c.Model}
			models[c.Model] = m
			modelAccounts[c.Model] = map[string]int64{}
		}
		m.SpendMicroUSD = addMoney(m.SpendMicroUSD, c.SpendMicroUSD)
		m.Tokens += c.Tokens()
		modelAccounts[c.Model][c.Account] += c.Requests
	}
	for id, p := range providers {
		p.PlanKind = busiest(plans[id])
		report.Providers = append(report.Providers, *p)
	}
	slices.SortFunc(report.Providers, func(a, b UsageProvider) int {
		return largerFirst(a.Requests, b.Requests, a.ID, b.ID)
	})
	for id, m := range models {
		m.ProviderID = busiest(modelAccounts[id])
		report.Models = append(report.Models, *m)
	}
	slices.SortFunc(report.Models, func(a, b UsageModel) int {
		return largerFirst(a.Tokens, b.Tokens, a.ID, b.ID)
	})

	sources := map[string]*UsageSource{}
	for _, c := range s.Keyed {
		src, ok := sources[c.Key]
		if !ok {
			src = &UsageSource{Source: c.Key}
			sources[c.Key] = src
		}
		src.Requests += c.Requests
		src.Tokens += c.Tokens()
		src.SpendMicroUSD = addMoney(src.SpendMicroUSD, c.SpendMicroUSD)
	}
	for _, src := range sources {
		report.Sources = append(report.Sources, *src)
	}
	slices.SortFunc(report.Sources, func(a, b UsageSource) int {
		return largerFirst(a.Requests, b.Requests, a.Source, b.Source)
	})

	tasks := map[string]*UsageTask{}
	for _, c := range s.Tasks {
		t, ok := tasks[c.Task]
		if !ok {
			t = &UsageTask{TaskID: c.Task, Goal: goals[c.Task], ByModel: []UsageTaskModel{}}
			tasks[c.Task] = t
		}
		t.Input += c.Input
		t.Output += c.Output
		t.CacheRead += c.CacheRead
		t.CacheWrite += c.CacheWrite
		t.CostMicroUSD = addMoney(t.CostMicroUSD, c.SpendMicroUSD)
		t.ByModel = append(t.ByModel, UsageTaskModel{
			ModelID:      c.Model,
			CostMicroUSD: c.SpendMicroUSD,
			Input:        c.Input,
			Output:       c.Output,
			CacheRead:    c.CacheRead,
			CacheWrite:   c.CacheWrite,
		})
	}
	for _, t := range tasks {
		slices.SortFunc(t.ByModel, func(a, b UsageTaskModel) int { return strings.Compare(a.ModelID, b.ModelID) })
		report.Tasks = append(report.Tasks, *t)
	}
	slices.SortFunc(report.Tasks, func(a, b UsageTask) int {
		return byMoneyThen(a.CostMicroUSD, b.CostMicroUSD, a.TaskID, b.TaskID)
	})
	return report
}

// WorkspaceUsage builds the workspace report for group. names holds the
// visible names of people, teams or agents by id; a key without one gets the
// group's placeholder, never an owner-side lookup.
func WorkspaceUsage(s UsageScan, w UsageWindow, group UsageGroup, names map[string]string) WorkspaceUsageReport {
	buckets, total := s.buckets(w)
	report := WorkspaceUsageReport{
		Range:             w.Range,
		Bucket:            w.Unit,
		TimeZone:          w.Zone.String(),
		Group:             group,
		SpendMicroUSD:     total.SpendMicroUSD,
		Estimated:         total.Estimated,
		UnpricedCalls:     total.UnpricedCalls,
		ListPriceMicroUSD: total.ListPriceMicroUSD,
		Buckets:           buckets,
		Rows:              []UsageRow{},
	}
	rows := map[string]*UsageRow{}
	for _, c := range s.Keyed {
		r, ok := rows[c.Key]
		if !ok {
			r = &UsageRow{ID: c.Key, Name: s.rowName(group, c.Key, names)}
			rows[c.Key] = r
		}
		r.add(c)
	}
	for _, r := range rows {
		report.Rows = append(report.Rows, *r)
	}
	slices.SortFunc(report.Rows, func(a, b UsageRow) int {
		return byMoneyThen(a.SpendMicroUSD, b.SpendMicroUSD, a.ID, b.ID)
	})
	return report
}

func (s UsageScan) rowName(group UsageGroup, id string, names map[string]string) string {
	switch group {
	case GroupPerson:
		if id == "" {
			return SystemName
		}
		if n := names[id]; n != "" {
			return n
		}
		return UnknownPersonName
	case GroupTeam:
		if id == "" {
			return NoTeamName
		}
		if n := names[id]; n != "" {
			return n
		}
		return UnknownTeamName
	case GroupAgent:
		if id == "" {
			return NoAgentName
		}
		if n := names[id]; n != "" {
			return n
		}
		return PrivateAgentName
	case GroupProvider:
		return s.providerName(id)
	default:
		return id
	}
}

// busiest returns the key with the most requests, the lowest key on a tie.
func busiest(counts map[string]int64) string {
	best, most := "", int64(-1)
	for k, n := range counts {
		if n > most || (n == most && k < best) {
			best, most = k, n
		}
	}
	return best
}

// largerFirst orders larger counts first, then by key.
func largerFirst(a, b int64, aKey, bKey string) int {
	if a != b {
		if a > b {
			return -1
		}
		return 1
	}
	return strings.Compare(aKey, bKey)
}

// byMoneyThen orders larger amounts first and unknown amounts last.
func byMoneyThen(a, b *int64, aKey, bKey string) int {
	switch {
	case a == nil && b == nil:
		return strings.Compare(aKey, bKey)
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return largerFirst(*a, *b, aKey, bKey)
}

// UsageCSVHeader is the column row of a workspace usage export.
var UsageCSVHeader = []string{
	"range", "time_zone", "group", "group_id", "group_name",
	"requests", "errors",
	"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens", "total_tokens",
	"spend_micro_usd", "estimated", "unpriced_calls", "list_price_micro_usd",
}

// UsageCSV returns the export rows, header first. Unknown money is an empty
// cell, and a name a spreadsheet would run as a formula is quoted.
func UsageCSV(r WorkspaceUsageReport) [][]string {
	out := [][]string{UsageCSVHeader}
	money := func(v *int64) string {
		if v == nil {
			return ""
		}
		return strconv.FormatInt(*v, 10)
	}
	n := func(v int64) string { return strconv.FormatInt(v, 10) }
	for _, row := range r.Rows {
		out = append(out, []string{
			string(r.Range), r.TimeZone, string(r.Group), NeutralizeCell(row.ID), NeutralizeCell(row.Name),
			n(row.Requests), n(row.Errors),
			n(row.Input), n(row.Output), n(row.CacheRead), n(row.CacheWrite), n(row.Reasoning), n(row.Tokens),
			money(row.SpendMicroUSD), strconv.FormatBool(row.Estimated), n(row.UnpricedCalls), money(row.ListPriceMicroUSD),
		})
	}
	return out
}

// NeutralizeCell prefixes a value a spreadsheet would read as a formula
// (one starting with =, +, -, @, a tab or a carriage return) with a quote.
func NeutralizeCell(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}
