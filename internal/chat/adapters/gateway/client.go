// Package gateway is brain's client for the model gateway chat stream. It
// signs each call with the service HMAC and does not import the gateway
// process.
package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// pathChatStream mirrors the gateway's ChatStream path. The signature covers
// the path, so the two cannot drift apart silently.
const pathChatStream = "/upmo.gateway.v1.Gateway/ChatStream"

// Message is one turn of the history sent with the prompt.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Request is one chat turn: a prompt, or a message history when the turn
// builds on earlier lines. Route names the gateway chat route for this turn;
// CredentialRef, when set, spends a bring-your-own key instead of the house
// one. PinModel with PinAccount, or with PinProvider, names the model the
// gateway tries before the route: on that provider account, or on the
// person's own account of that provider and else a global one. AgentID is carried with the turn
// and not sent: the gateway reads an agent id as a tool bundle to look up.
type Request struct {
	Route           string
	CredentialRef   string
	UserID          string
	Prompt          string
	System          string
	Messages        []Message
	MaxOutputTokens int
	PinAccount      string
	PinProvider     string
	PinModel        string
	AgentID         string
	TeamID          string
}

// Attempt names the model that failed before any text and the next one.
type Attempt struct {
	FailedModel  string `json:"failed_model"`
	FailureClass string `json:"failure_class"`
	NextModel    string `json:"next_model"`
}

// Done is the successful end of a streamed reply.
type Done struct {
	Model        string   `json:"model"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
}

// Failure is the error frame of a stream: the class, the model that was
// writing, and a message meant for logs only.
type Failure struct {
	Class   string `json:"failure_class"`
	Model   string `json:"model"`
	Message string `json:"message"`
}

func (f *Failure) Error() string {
	return fmt.Sprintf("chat: gateway stream %s on %s", f.Class, f.Model)
}

// ErrCut is a stream that ended without a done or error frame.
var ErrCut = errors.New("chat: gateway stream was cut")

// Client streams chat replies from the model gateway.
type Client struct {
	base string
	http *http.Client
}

// Open builds a client. baseURL and serviceKey are the install settings.
func Open(baseURL, serviceKey string) (Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Client{}, fmt.Errorf("chat: gateway url")
	}
	master, err := svcsig.ParseKey(serviceKey)
	if err != nil {
		return Client{}, err
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		return Client{}, err
	}
	transport, err := svcsig.Transport(nil, derived, svcsig.AudienceGateway)
	if err != nil {
		return Client{}, err
	}
	return Client{
		base: strings.TrimRight(baseURL, "/"),
		http: &http.Client{Transport: transport},
	}, nil
}

// ChatStream streams one chat turn on the request's route. onDelta receives
// each text delta in order and onAttempt each failover notice; both may be
// nil. It returns the done frame, a *Failure for an error frame, and ErrCut
// when the stream ends without a final frame.
func (c Client) ChatStream(ctx context.Context, req Request, onDelta func(string), onAttempt func(Attempt)) (Done, error) {
	if req.Route == "" {
		return Done{}, fmt.Errorf("chat: gateway route")
	}
	if req.UserID == "" || (req.Prompt == "" && len(req.Messages) == 0) {
		return Done{}, fmt.Errorf("chat: gateway stream input")
	}
	raw, err := json.Marshal(chatTurn{
		Route:           req.Route,
		CredentialRef:   req.CredentialRef,
		Prompt:          req.Prompt,
		System:          req.System,
		Messages:        req.Messages,
		MaxOutputTokens: req.MaxOutputTokens,
		Pin:             pinOf(req),
		UserID:          req.UserID,
		TeamID:          req.TeamID,
	})
	if err != nil {
		return Done{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+pathChatStream, bytes.NewReader(raw))
	if err != nil {
		return Done{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(httpReq)
	if err != nil {
		return Done{}, fmt.Errorf("chat: gateway stream: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Done{}, fmt.Errorf("chat: gateway %d", res.StatusCode)
	}
	lines := bufio.NewReader(res.Body)
	for {
		line, rerr := lines.ReadString('\n')
		if text := strings.TrimSpace(line); text != "" {
			done, ferr := oneFrame(text, onDelta, onAttempt)
			if ferr != nil {
				return Done{}, ferr
			}
			if done != nil {
				return *done, nil
			}
		}
		if rerr != nil {
			return Done{}, ErrCut
		}
	}
}

type chatTurn struct {
	Route           string    `json:"route"`
	CredentialRef   string    `json:"credential_ref,omitempty"`
	Prompt          string    `json:"prompt,omitempty"`
	System          string    `json:"system,omitempty"`
	Messages        []Message `json:"messages,omitempty"`
	MaxOutputTokens int       `json:"max_output_tokens,omitempty"`
	Pin             *pinTurn  `json:"pin,omitempty"`
	UserID          string    `json:"user_id"`
	TeamID          string    `json:"team_id,omitempty"`
}

// pinTurn is the pin as the gateway reads it.
type pinTurn struct {
	AccountID string `json:"account_id,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model"`
}

func pinOf(req Request) *pinTurn {
	if req.PinAccount == "" && req.PinProvider == "" && req.PinModel == "" {
		return nil
	}
	return &pinTurn{AccountID: req.PinAccount, Provider: req.PinProvider, Model: req.PinModel}
}

// frame is one line of a chat stream. Exactly one field is set.
type frame struct {
	Delta   *string  `json:"delta"`
	Attempt *Attempt `json:"attempt"`
	Done    *Done    `json:"done"`
	Error   *Failure `json:"error"`
}

func oneFrame(text string, onDelta func(string), onAttempt func(Attempt)) (*Done, error) {
	var f frame
	if err := json.Unmarshal([]byte(text), &f); err != nil {
		return nil, fmt.Errorf("chat: gateway frame")
	}
	switch {
	case f.Delta != nil:
		if onDelta != nil {
			onDelta(*f.Delta)
		}
		return nil, nil
	case f.Attempt != nil:
		if onAttempt != nil {
			onAttempt(*f.Attempt)
		}
		return nil, nil
	case f.Done != nil:
		return f.Done, nil
	case f.Error != nil:
		return nil, f.Error
	default:
		return nil, fmt.Errorf("chat: gateway frame")
	}
}
