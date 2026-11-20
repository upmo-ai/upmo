package agentloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
)

// ActionState is the lifecycle state of an at-most-once mutating action.
type ActionState string

const (
	ActionStateDialing   ActionState = "dialing"
	ActionStateDone      ActionState = "done"
	ActionStateFailed    ActionState = "failed"
	ActionStateSimulated ActionState = "simulated"
)

// ActionRecord represents one row in the durable action log.
type ActionRecord struct {
	CallID          string
	RunID           string
	ObjectID        string
	UserID          string
	ToolName        string
	Action          string
	Preview         string
	State           ActionState
	UntrustedOrigin bool
	DryRun          bool
	ResponseDigest  string
	ErrorText       string
	ExecutedAt      time.Time
	CompletedAt     *time.Time
}

// ActionLog is the pure port for 3-transaction at-most-once execution.
//
// Implementations must guarantee:
//   - tx1 (BeginDial): atomically check whether (scope_id, call_id) already
//     exists in dialing/done/simulated; if so return ErrDuplicateAction,
//     otherwise insert state="dialing", executed_at=now() and commit.
//   - tx2 (FinishDial): immediately update state="done" (or "failed") with
//     response digest and commit after the external call returns.
//   - RecordSimulated: record a dry_run simulated write (state="simulated") and commit.
type ActionLog interface {
	BeginDial(ctx context.Context, rec ActionRecord) error
	FinishDial(ctx context.Context, scopeID, callID string, state ActionState, responseDigest, errText string, completedAt time.Time) error
	RecordSimulated(ctx context.Context, rec ActionRecord) error
}

// ActionScopeID returns the scope identifier for rec (RunID if non-empty, else ObjectID).
func ActionScopeID(rec ActionRecord) string {
	if s := strings.TrimSpace(rec.RunID); s != "" {
		return s
	}
	return strings.TrimSpace(rec.ObjectID)
}

// ScopedCallID namespaces a provider tool call ID by the run's turn sequence
// so distinct turns reusing the same call ID (e.g. "call_0") do not collide
// in action_logs or idempotency keys, hashing long IDs to fit the 200-char DB check.
func ScopedCallID(seq int, callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	scoped := "t" + strconv.Itoa(seq) + ":" + callID
	if len(scoped) <= 180 {
		return scoped
	}
	sum := sha256.Sum256([]byte(scoped))
	return "t" + strconv.Itoa(seq) + ":sha256:" + hex.EncodeToString(sum[:])
}

// DeriveIdempotencyKey deterministically derives an external Idempotency-Key
// from (scopeID, callID) as a UUIDv5 so providers reusing call IDs across
// scopes never collide or leak internal identifiers.
func DeriveIdempotencyKey(scopeID, callID string) string {
	keyInput := strings.TrimSpace(scopeID) + "\x00" + strings.TrimSpace(callID)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(keyInput)).String()
}

// TurnCommit carries everything persisted atomically in tx3 at the end of a turn.
// Messages contains only the messages appended by this turn.
type TurnCommit struct {
	RunID           string
	ObjectID        string
	UserID          string
	SeqStart        int
	Messages        []Message
	Model           string
	InputTokens     int
	OutputTokens    int
	CostMicroUSD    int64
	SpentMicroUSD   int64
	Paused          bool
	Exposure        ExposureState
	Hold            *BatchHold
	BlockedEvents   []TimelineEvent
	SimulatedEvents []TimelineEvent
	EnqueueNext     bool
}

// TurnStore commits tx3 (turn transcript row + state + batch hold + next River job).
type TurnStore interface {
	CommitTurnAndEnqueue(ctx context.Context, commit TurnCommit) error
}

// ExecuteAtMostOnce runs the 2-phase action-log protocol around fn:
//  1. tx1: BeginDial commits state="dialing", executed_at=now() (or fails with
//     ErrDuplicateAction on replay/crash after tx1).
//  2. fn runs outside any DB transaction with Idempotency-Key derived from (scope_id, call_id).
//  3. tx2: FinishDial commits state="done" or "failed" with response digest.
func ExecuteAtMostOnce(
	ctx context.Context,
	log ActionLog,
	rec ActionRecord,
	fn func(ctx context.Context, idempotencyKey string) (responseDigest string, err error),
) error {
	if log == nil {
		return fmt.Errorf("%w: nil ActionLog", ErrInvalidConfig)
	}
	if strings.TrimSpace(rec.CallID) == "" {
		return fmt.Errorf("%w: empty call_id", ErrInvalidConfig)
	}
	if rec.ExecutedAt.IsZero() {
		rec.ExecutedAt = time.Now().UTC()
	}
	scopeID := ActionScopeID(rec)
	idempotencyKey := DeriveIdempotencyKey(scopeID, rec.CallID)
	rec.State = ActionStateDialing
	if err := log.BeginDial(ctx, rec); err != nil {
		return err
	}

	digest, callErr := fn(ctx, idempotencyKey)
	completedAt := time.Now().UTC()
	if callErr != nil {
		finishErr := log.FinishDial(context.WithoutCancel(ctx), scopeID, rec.CallID, ActionStateFailed, digest, callErr.Error(), completedAt)
		return errors.Join(callErr, finishErr)
	}
	return log.FinishDial(context.WithoutCancel(ctx), scopeID, rec.CallID, ActionStateDone, digest, "", completedAt)
}

// HTTPConnectorClient executes connector HTTP requests guarded by netguard
// and secretchain, attaching Idempotency-Key for mutating calls.
type HTTPConnectorClient struct {
	Client  *http.Client
	Secrets secretchain.Backend
}

// NewHTTPConnectorClient builds an SSRF-guarded HTTP client using netguard.
func NewHTTPConnectorClient(guard *netguard.Guard, secrets secretchain.Backend, timeout time.Duration) *HTTPConnectorClient {
	if guard == nil {
		guard = &netguard.Guard{}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &HTTPConnectorClient{
		Client: &http.Client{
			Transport: guard.Transport(),
			Timeout:   timeout,
		},
		Secrets: secrets,
	}
}

// Do performs an HTTP request with optional credential ref (Bearer token) and
// Idempotency-Key header, returning status code, body, and SHA-256 digest.
// When credentialRef or allowedHost is non-empty, the request URL host must
// match allowedHost before any credential is fetched or attached.
func (c *HTTPConnectorClient) Do(
	ctx context.Context,
	method string,
	targetURL string,
	allowedHost string,
	body io.Reader,
	credentialRef string,
	idempotencyKey string,
	headers map[string]string,
) (status int, respBody []byte, digest string, err error) {
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return 0, nil, "", err
	}
	expectedHost := strings.TrimSpace(allowedHost)
	if credentialRef != "" && expectedHost == "" {
		return 0, nil, "", fmt.Errorf("agentloop: allowedHost required with credentialRef")
	}
	if expectedHost != "" && !hostMatches(req.URL, expectedHost) {
		return 0, nil, "", fmt.Errorf("agentloop: target host %q does not match allowed host %q", req.URL.Host, expectedHost)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if credentialRef != "" {
		if c.Secrets == nil {
			return 0, nil, "", fmt.Errorf("agentloop: secrets backend not configured")
		}
		secret, err := c.Secrets.Get(credentialRef)
		if err != nil {
			return 0, nil, "", err
		}
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Transport: (&netguard.Guard{}).Transport(), Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = res.Body.Close() }()
	const maxRespBytes = 1 << 20
	respBody, err = io.ReadAll(io.LimitReader(res.Body, maxRespBytes))
	if err != nil {
		return res.StatusCode, nil, "", err
	}
	sum := sha256.Sum256(respBody)
	digest = hex.EncodeToString(sum[:])
	if res.StatusCode >= 400 {
		return res.StatusCode, respBody, digest, fmt.Errorf("agentloop: connector http status %d", res.StatusCode)
	}
	return res.StatusCode, respBody, digest, nil
}

func hostMatches(u *url.URL, allowedHost string) bool {
	if u == nil {
		return false
	}
	expected := strings.ToLower(strings.TrimSpace(allowedHost))
	if expected == "" || u.Host == "" {
		return false
	}
	if strings.Contains(expected, ":") {
		return strings.EqualFold(u.Host, expected)
	}
	return strings.EqualFold(u.Hostname(), expected)
}
