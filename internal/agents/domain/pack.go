package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PackSchemaV1 is the schema identifier for upmo.pack/v1 manifests.
const PackSchemaV1 = "upmo.pack/v1"

// DefaultMarketplaceKeyID is the key id of the bundled Upmo marketplace key.
const DefaultMarketplaceKeyID = "upmo-marketplace-v1"

// bundledMarketplaceKeyB64 is the 32-byte Ed25519 public key for DefaultMarketplaceKeyID.
// TODO: Replace this placeholder public key with the production Upmo marketplace
// Ed25519 public key before shipping marketplace packs; UPMO_PACK_TRUST_KEYS
// overrides or extends DefaultPackKeys at runtime.
const bundledMarketplaceKeyB64 = "M8f2p4x7N5qR8vT1wZ3yB6dF9hJ2kL5mP8sV1xY4zA0="

var (
	// ErrMalformedPack reports a manifest that is not valid upmo.pack/v1 JSON or fails schema validation.
	ErrMalformedPack = errors.New("agents: malformed pack manifest")
	// ErrUntrustedKey reports a manifest signed by an unknown or untrusted key_id.
	ErrUntrustedKey = errors.New("agents: untrusted pack key")
	// ErrInvalidSignature reports a manifest whose Ed25519 signature does not verify.
	ErrInvalidSignature = errors.New("agents: invalid pack signature")
	// ErrPackDowngrade reports an attempt to install an older pack version over a newer one.
	ErrPackDowngrade = errors.New("agents: pack version cannot downgrade installed version")
)

var trustKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// TrustKeys maps trusted key_id values to 32-byte Ed25519 public keys.
type TrustKeys map[string]ed25519.PublicKey

// DefaultPackKeys returns the bundled marketplace public keys.
func DefaultPackKeys() TrustKeys {
	raw, err := base64.StdEncoding.DecodeString(bundledMarketplaceKeyB64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return TrustKeys{}
	}
	return TrustKeys{
		DefaultMarketplaceKeyID: ed25519.PublicKey(raw),
	}
}

// ParseTrustKeys parses UPMO_PACK_TRUST_KEYS ("key_id=base64_pub,...") and merges
// them on top of base (if non-nil).
func ParseTrustKeys(raw string, base TrustKeys) (TrustKeys, error) {
	out := make(TrustKeys, len(base))
	for k, v := range base {
		out[k] = append(ed25519.PublicKey(nil), v...)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		keyID, pubB64, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("agents: UPMO_PACK_TRUST_KEYS entry must be key_id=base64_pub")
		}
		keyID = strings.TrimSpace(keyID)
		pubB64 = strings.TrimSpace(pubB64)
		if !trustKeyIDPattern.MatchString(keyID) {
			return nil, fmt.Errorf("agents: invalid trust key id %q", keyID)
		}
		pub, err := decodeBase64Key(pubB64, ed25519.PublicKeySize)
		if err != nil {
			return nil, fmt.Errorf("agents: invalid public key for %q: %w", keyID, err)
		}
		out[keyID] = ed25519.PublicKey(pub)
	}
	return out, nil
}

func decodeBase64Key(s string, wantLen int) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) != wantLen {
		return nil, fmt.Errorf("length %d, want %d", len(raw), wantLen)
	}
	return raw, nil
}

// Pack is the signed upmo.pack/v1 manifest.
//
// Note: Templates, ConnectorRules, and Schedules are validated and covered by
// the Ed25519 signature, but are reserved and ignored during v1.0.0 pack
// installation (only Agent and Lessons are applied).
type Pack struct {
	Schema         string              `json:"schema"`
	ID             string              `json:"id"`
	Version        string              `json:"version"`
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	Agent          PackAgent           `json:"agent"`
	Lessons        []PackLesson        `json:"lessons,omitempty"`
	Templates      []PackTemplate      `json:"templates,omitempty"`
	ConnectorRules []PackConnectorRule `json:"connector_rules,omitempty"`
	Schedules      []PackSchedule      `json:"schedules,omitempty"`
	KeyID          string              `json:"key_id"`
	Ed25519Sig     string              `json:"ed25519_sig"`
}

// PackAgent describes the agent profile bundled in a pack.
type PackAgent struct {
	Name                 string            `json:"name"`
	Slug                 string            `json:"slug"`
	Role                 string            `json:"role,omitempty"`
	Instructions         string            `json:"instructions"`
	DefaultRoute         string            `json:"default_route,omitempty"`
	DefaultModelProvider string            `json:"default_model_provider,omitempty"`
	DefaultModelID       string            `json:"default_model_id,omitempty"`
	ToolClasses          []string          `json:"tool_classes,omitempty"`
	ConnectorIDs         []string          `json:"connector_ids,omitempty"`
	PlanApproval         string            `json:"plan_approval,omitempty"`
	CheckCriteria        []json.RawMessage `json:"check_criteria,omitempty"`
}

// PackLesson is one bundled house rule.
type PackLesson struct {
	Title        string `json:"title"`
	Cue          string `json:"cue"`
	Body         string `json:"body"`
	AlwaysInline bool   `json:"always_inline,omitempty"`
}

// PackTemplate is one bundled task template.
type PackTemplate struct {
	Name          string            `json:"name"`
	Goal          string            `json:"goal"`
	Flow          string            `json:"flow,omitempty"`
	CheckCriteria []json.RawMessage `json:"check_criteria,omitempty"`
}

// PackConnectorRule declares connector scope policy for the pack.
type PackConnectorRule struct {
	Connector     string `json:"connector"`
	ResourceScope string `json:"resource_scope,omitempty"`
	Changes       string `json:"changes"`
}

// PackSchedule is an optional schedule entry in a pack.
type PackSchedule struct {
	Name     string `json:"name,omitempty"`
	Cron     string `json:"cron"`
	Timezone string `json:"timezone,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

// Validate checks that p conforms to the upmo.pack/v1 schema rules.
func (p Pack) Validate() error {
	if p.Schema != PackSchemaV1 {
		return fmt.Errorf("%w: schema must be %q", ErrMalformedPack, PackSchemaV1)
	}
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Version) == "" || strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: id, version, and name are required", ErrMalformedPack)
	}
	if _, err := parseSemver(p.Version); err != nil {
		return fmt.Errorf("%w: version: %v", ErrMalformedPack, err)
	}
	if strings.TrimSpace(p.KeyID) == "" {
		return fmt.Errorf("%w: key_id is required", ErrMalformedPack)
	}
	if _, err := CleanName(p.Agent.Name); err != nil {
		return fmt.Errorf("%w: agent.name: %v", ErrMalformedPack, err)
	}
	if strings.TrimSpace(p.Agent.Slug) == "" {
		return fmt.Errorf("%w: agent.slug is required", ErrMalformedPack)
	}
	if _, err := CleanSlug(p.Agent.Slug, p.Agent.Name); err != nil {
		return fmt.Errorf("%w: agent.slug: %v", ErrMalformedPack, err)
	}
	if _, err := CleanRole(p.Agent.Role); err != nil {
		return fmt.Errorf("%w: agent.role: %v", ErrMalformedPack, err)
	}
	if _, err := CleanDescription(p.Description); err != nil {
		return fmt.Errorf("%w: description: %v", ErrMalformedPack, err)
	}
	if _, err := CleanInstructions(p.Agent.Instructions); err != nil {
		return fmt.Errorf("%w: agent.instructions: %v", ErrMalformedPack, err)
	}
	if _, err := CleanRoute(p.Agent.DefaultRoute); err != nil {
		return fmt.Errorf("%w: agent.default_route: %v", ErrMalformedPack, err)
	}
	if _, _, err := CleanModel(p.Agent.DefaultModelProvider, p.Agent.DefaultModelID); err != nil {
		return fmt.Errorf("%w: agent model: %v", ErrMalformedPack, err)
	}
	if _, err := CleanToolClasses(p.Agent.ToolClasses); err != nil {
		return fmt.Errorf("%w: agent.tool_classes: %v", ErrMalformedPack, err)
	}
	if _, err := CleanConnectorIDs(p.Agent.ConnectorIDs); err != nil {
		return fmt.Errorf("%w: agent.connector_ids: %v", ErrMalformedPack, err)
	}
	if _, err := CleanPlanApproval(p.Agent.PlanApproval); err != nil {
		return fmt.Errorf("%w: agent.plan_approval: %v", ErrMalformedPack, err)
	}
	if _, err := CleanCheckCriteria(p.Agent.CheckCriteria); err != nil {
		return fmt.Errorf("%w: agent.check_criteria: %v", ErrMalformedPack, err)
	}
	for i, l := range p.Lessons {
		if _, _, _, err := CleanLesson(l.Title, l.Cue, l.Body); err != nil {
			return fmt.Errorf("%w: lessons[%d]: %v", ErrMalformedPack, i, err)
		}
	}
	for i, tpl := range p.Templates {
		if strings.TrimSpace(tpl.Name) == "" || strings.TrimSpace(tpl.Goal) == "" {
			return fmt.Errorf("%w: templates[%d] requires name and goal", ErrMalformedPack, i)
		}
		if _, err := CleanCheckCriteria(tpl.CheckCriteria); err != nil {
			return fmt.Errorf("%w: templates[%d].check_criteria: %v", ErrMalformedPack, i, err)
		}
	}
	for i, rule := range p.ConnectorRules {
		if strings.TrimSpace(rule.Connector) == "" {
			return fmt.Errorf("%w: connector_rules[%d].connector is required", ErrMalformedPack, i)
		}
		switch rule.Changes {
		case "allow", "ask", "block":
		default:
			return fmt.Errorf("%w: connector_rules[%d].changes must be allow, ask, or block", ErrMalformedPack, i)
		}
	}
	for i, sched := range p.Schedules {
		if strings.TrimSpace(sched.Cron) == "" {
			return fmt.Errorf("%w: schedules[%d].cron is required", ErrMalformedPack, i)
		}
	}
	return nil
}

// VerifyPack parses an upmo.pack/v1 JSON manifest, verifies its schema and
// Ed25519 signature over the RFC 8785 canonical JSON payload (excluding
// ed25519_sig) against keys, and returns the decoded Pack.
func VerifyPack(raw []byte, keys TrustKeys) (Pack, error) {
	decoded, err := decodeStrictJSON(raw)
	if err != nil {
		return Pack{}, fmt.Errorf("%w: %v", ErrMalformedPack, err)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return Pack{}, fmt.Errorf("%w: manifest root must be a JSON object", ErrMalformedPack)
	}
	var pack Pack
	if err := json.Unmarshal(raw, &pack); err != nil {
		return Pack{}, fmt.Errorf("%w: %v", ErrMalformedPack, err)
	}
	if err := pack.Validate(); err != nil {
		return Pack{}, err
	}
	if strings.TrimSpace(pack.Ed25519Sig) == "" {
		return Pack{}, fmt.Errorf("%w: ed25519_sig is required", ErrMalformedPack)
	}
	sig, err := decodeBase64Key(strings.TrimSpace(pack.Ed25519Sig), ed25519.SignatureSize)
	if err != nil {
		return Pack{}, fmt.Errorf("%w: ed25519_sig: %v", ErrMalformedPack, err)
	}
	pub, ok := keys[pack.KeyID]
	if !ok || len(pub) != ed25519.PublicKeySize {
		return Pack{}, fmt.Errorf("%w: %q", ErrUntrustedKey, pack.KeyID)
	}
	delete(obj, "ed25519_sig")
	canon, err := canonicalizeValue(obj)
	if err != nil {
		return Pack{}, fmt.Errorf("%w: %v", ErrMalformedPack, err)
	}
	if !ed25519.Verify(pub, canon, sig) {
		return Pack{}, ErrInvalidSignature
	}
	return pack, nil
}

// SignPack sets KeyID and Ed25519Sig on p using RFC 8785 canonical JSON over
// the manifest excluding ed25519_sig, and returns the serialized manifest JSON.
func SignPack(p Pack, keyID string, priv ed25519.PrivateKey) ([]byte, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("agents: invalid Ed25519 private key length")
	}
	p.KeyID = keyID
	p.Ed25519Sig = ""
	if err := p.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeStrictJSON(raw)
	if err != nil {
		return nil, err
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("agents: manifest root must be a JSON object")
	}
	delete(obj, "ed25519_sig")
	canon, err := canonicalizeValue(obj)
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(priv, canon)
	p.Ed25519Sig = base64.StdEncoding.EncodeToString(sig)
	return json.Marshal(p)
}

type semverParts struct {
	major int
	minor int
	patch int
	pre   []string
}

// ComparePackVersion compares two SemVer 2.0.0 pack version strings (with an
// optional leading "v"), returning -1, 0, or 1.
func ComparePackVersion(a, b string) (int, error) {
	va, err := parseSemver(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseSemver(b)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]int{{va.major, vb.major}, {va.minor, vb.minor}, {va.patch, vb.patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, nil
	case len(va.pre) == 0:
		return 1, nil
	case len(vb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := comparePreIdent(va.pre[i], vb.pre[i]); c != 0 {
			return c, nil
		}
	}
	if len(va.pre) < len(vb.pre) {
		return -1, nil
	}
	if len(va.pre) > len(vb.pre) {
		return 1, nil
	}
	return 0, nil
}

func comparePreIdent(a, b string) int {
	na, errA := strconv.Atoi(a)
	nb, errB := strconv.Atoi(b)
	switch {
	case errA == nil && errB == nil:
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
		return 0
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func parseSemver(raw string) (semverParts, error) {
	s := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if core, _, ok := strings.Cut(s, "+"); ok {
		s = core
	}
	var pre []string
	if core, preRaw, ok := strings.Cut(s, "-"); ok {
		s = core
		if preRaw == "" {
			return semverParts{}, fmt.Errorf("invalid semver %q", raw)
		}
		pre = strings.Split(preRaw, ".")
		for _, id := range pre {
			if id == "" {
				return semverParts{}, fmt.Errorf("invalid semver %q", raw)
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semverParts{}, fmt.Errorf("invalid semver %q", raw)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return semverParts{}, fmt.Errorf("invalid semver %q", raw)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semverParts{}, fmt.Errorf("invalid semver %q", raw)
		}
		nums[i] = n
	}
	return semverParts{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, nil
}
