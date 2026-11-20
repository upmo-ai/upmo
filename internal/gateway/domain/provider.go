package domain

import (
	"fmt"
	"hash/fnv"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// The adapter keys the gateway dispatches to. A provider account names one.
const (
	AdapterFake      = "fake"
	AdapterOpenAI    = "openai_compat"
	AdapterAnthropic = "anthropic"
)

// ScriptedPrefix starts the model ids that only the fake adapter acts on.
const ScriptedPrefix = "fake-"

// Limits on what a person may save.
const (
	MaxProviderName  = 60
	MaxProviderLabel = 60
	MaxModels        = 100
	MaxSeenModels    = 2000
	MaxModelName     = 120
	MaxOwnAccounts   = 20
	MaxAPIKeyBytes   = 4096
	MaxBaseURLBytes  = 2048
)

// What an account last did, as the settings screen shows it.
const (
	StatusNotConnected = "Not connected"
	StatusConnected    = "Connected"
	StatusRateLimited  = "Rate limited"
	StatusKeyRejected  = "Key rejected"
	StatusOffline      = "Offline"
	StatusWorkspaceOff = "Turned off by your workspace"
)

// ModelModalities describes the input and output modalities a provider
// advertises for a discovered model.
type ModelModalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

// SeenModel is one model discovered from a provider's model-list endpoint.
// Extra fields are populated when the provider returns them.
type SeenModel struct {
	ID            string            `json:"id"`
	DisplayName   string            `json:"display_name,omitempty"`
	Created       string            `json:"created,omitempty"`
	ContextLength int               `json:"context_length,omitempty"`
	Pricing       map[string]string `json:"pricing,omitempty"`
	Modalities    *ModelModalities  `json:"modalities,omitempty"`
	SeenAt        *time.Time        `json:"seen_at,omitempty"`
}

// EnabledModel is one model enabled on an account, with whether it was present
// in the provider's discovered model list when saved and whether two missed
// daily checks have retired it.
type EnabledModel struct {
	ID      string `json:"id"`
	Listed  bool   `json:"listed"`
	Retired bool   `json:"retired,omitempty"`
}

// RetireMissThreshold is the number of consecutive daily checks a model must
// be absent from discovery before it is marked retired.
const RetireMissThreshold = 2

// NextModelMissState returns the updated consecutive-miss count and retired flag
// after a successful discovery probe. A model present in discovery resets to
// zero misses and is no longer retired; an absent model increments misses and
// retires once misses reaches RetireMissThreshold.
func NextModelMissState(misses int, present bool) (int, bool) {
	if present {
		return 0, false
	}
	next := misses + 1
	return next, next >= RetireMissThreshold
}

// AccountCheckOffset returns a deterministic delay in [0, 24h) for accountID so
// daily provider checks spread across the day without a thundering herd.
func AccountCheckOffset(accountID string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(accountID))
	return time.Duration(h.Sum64() % uint64(24*time.Hour))
}

// Provider is one account as a person sees it. CredentialRef names its sealed
// key and never leaves the backend.
type Provider struct {
	ID              string
	Provider        string
	Name            string
	Adapter         string
	PlanKind        string
	BaseURL         string
	PricingProvider string
	PricingModels   map[string]string
	Status          string
	Enabled         bool
	// Disabled preserves the zero-value compatibility of provider test views;
	// persisted accounts set it from enabled when they are scanned.
	Disabled      bool
	Models        []string
	EnabledModels []EnabledModel
	// ModelKinds holds the catalogue kind for Models when known. A model
	// absent from ModelKinds counts as KindChat with text input.
	ModelKinds map[string]ModelKind
	Own        bool
	// OwnerUserID is the account's owner. It is empty for a house account.
	OwnerUserID   string
	CredentialRef string
	HasKey        bool
	KeyHint       string
	KeyChangedAt  *time.Time
	KeyChangedBy  string
	Share         Share
}

// KeyHint returns the final four characters of a sufficiently long key. Short
// keys do not reveal a hint, even to the person who saved them.
func KeyHint(key string) string {
	runes := []rune(key)
	if len(runes) < 12 {
		return ""
	}
	return string(runes[len(runes)-4:])
}

// Invalid is a plain-language reason an input was refused.
type Invalid string

func (e Invalid) Error() string { return string(e) }

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

// CleanName trims name and checks it: 1 to 60 characters, no control characters.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	switch {
	case n == 0:
		return "", Invalid("Give the provider a name.")
	case n > MaxProviderName || hasControl(name) || !utf8.ValidString(name):
		return "", Invalid(fmt.Sprintf("The name must be 1 to %d characters.", MaxProviderName))
	}
	return name, nil
}

// CleanLabel trims the vendor label and checks it like a name.
func CleanLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	n := utf8.RuneCountInString(label)
	if n == 0 || n > MaxProviderLabel || hasControl(label) || !utf8.ValidString(label) {
		return "", Invalid(fmt.Sprintf("The provider must be 1 to %d characters.", MaxProviderLabel))
	}
	return label, nil
}

// CleanPlan checks the plan kind.
func CleanPlan(plan string) (string, error) {
	switch plan {
	case PlanMetered, PlanSubscription, PlanLocal:
		return plan, nil
	}
	return "", Invalid("Choose metered, subscription or local.")
}

// CleanModelID trims a model id and checks it: 1 to MaxModelName characters,
// valid UTF-8, and no control characters.
func CleanModelID(raw string) (string, error) {
	m := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(m)
	if n == 0 || n > MaxModelName || hasControl(m) || !utf8.ValidString(m) {
		return "", Invalid(fmt.Sprintf("Each model must be 1 to %d characters.", MaxModelName))
	}
	return m, nil
}

// CleanText reports whether s is valid UTF-8 without control characters.
func ValidModelText(s string) bool {
	return utf8.ValidString(s) && !hasControl(s)
}

// CleanModels trims each model and checks the list: up to 100 models, each 1
// to 120 characters, none twice.
func CleanModels(models []string) ([]string, error) {
	if len(models) > MaxModels {
		return nil, Invalid(fmt.Sprintf("Add up to %d models.", MaxModels))
	}
	out := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, raw := range models {
		m, err := CleanModelID(raw)
		if err != nil {
			return nil, err
		}
		if seen[m] {
			return nil, Invalid("A model is listed twice.")
		}
		seen[m] = true
		out = append(out, m)
	}
	return out, nil
}

// CheckScripted refuses a scripted model id (fake-...) on an account whose
// adapter is not the fake one, so no real provider is ever sent one.
func CheckScripted(adapter string, models []string) error {
	if adapter == AdapterFake {
		return nil
	}
	for _, m := range models {
		if strings.HasPrefix(m, ScriptedPrefix) {
			return Invalid("Model ids starting with fake- belong to the test adapter.")
		}
	}
	return nil
}

// CleanKey trims an API key and checks it. An empty key is returned as empty;
// NeedKey decides whether that is allowed.
func CleanKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if len(key) > MaxAPIKeyBytes || hasControl(key) || !utf8.ValidString(key) {
		return "", Invalid("The API key is not valid.")
	}
	return key, nil
}

// NeedKey reports a missing key: metered and subscription accounts need one,
// either now or already saved. A local account may go without.
func NeedKey(plan, key string, saved bool) error {
	if plan != PlanLocal && key == "" && !saved {
		return Invalid("An API key is required.")
	}
	return nil
}

// AddrReason is why a plan may not dial addr, or "" when it may. A local
// account may reach loopback and private addresses; no account may reach a
// link-local or metadata address.
func AddrReason(plan string, addr netip.Addr) string {
	if plan == PlanLocal {
		return netguard.BlockedAddrLocal(addr)
	}
	return netguard.BlockedAddr(addr)
}

// metadataHosts are names that only ever mean a cloud metadata service.
var metadataHosts = map[string]bool{
	"metadata":                     true,
	"metadata.google.internal":     true,
	"metadata.goog":                true,
	"instance-data":                true,
	"instance-data.ec2.internal":   true,
	"metadata.tencentyun.com":      true,
	"metadata.platformequinix.com": true,
}

// numericHost matches the forms a resolver may read as an IPv4 address
// without being a dotted quad: 2130706433, 0x7f.1, 0177.0.0.1.
var numericHost = regexp.MustCompile(`^(0x[0-9a-f]+|[0-9]+)(\.(0x[0-9a-f]+|[0-9]+))*$`)

var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9_-]*[a-z0-9])?)*$`)

// CleanBaseURL checks a provider base URL for plan and returns it without a
// trailing slash. Empty stays empty.
//
// https is required for metered and subscription accounts; a local account
// may use http. A user name or password in the URL is refused. A literal
// address is judged here by AddrReason. A name is judged when it is dialed,
// on every address it resolves to, so a name that is re-pointed after saving
// still cannot reach an address the plan forbids. This function does no I/O.
func CleanBaseURL(plan, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	bad := Invalid("The base URL is not valid.")
	if len(raw) > MaxBaseURLBytes || hasControl(raw) {
		return "", bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Host == "" {
		return "", bad
	}
	scheme := strings.ToLower(u.Scheme)
	switch {
	case scheme == "https":
	case scheme == "http" && plan == PlanLocal:
	case scheme == "http":
		return "", Invalid("The base URL must start with https://.")
	default:
		return "", bad
	}
	if u.User != nil {
		return "", Invalid("The base URL must not contain a user name or password.")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", Invalid("The base URL must not have a query or a fragment.")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", bad
		}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", bad
	}
	blocked := Invalid("The base URL points at a blocked address.")
	internal := Invalid("The base URL points at a private or internal address. Use the local plan for a provider on your own network.")
	if addr, err := netip.ParseAddr(host); err == nil {
		switch reason := AddrReason(plan, addr); reason {
		case "":
		case "private", "loopback", "shared address space", "unique local":
			return "", internal
		default:
			return "", blocked
		}
	} else {
		switch {
		case strings.Contains(host, ":") || strings.Contains(host, "%"):
			return "", bad
		case metadataHosts[host]:
			return "", blocked
		case numericHost.MatchString(host):
			return "", bad
		case !hostName.MatchString(host):
			return "", bad
		case plan != PlanLocal && (host == "localhost" || strings.HasSuffix(host, ".localhost")):
			return "", internal
		}
	}
	clean := url.URL{Scheme: scheme, Host: strings.ToLower(u.Host), Path: strings.TrimRight(u.Path, "/")}
	return clean.String(), nil
}
