package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// Adapter talks OpenAI-compatible /v1/chat/completions. Production uses
// netguard; tests inject Client.
type Adapter struct {
	Client *http.Client
}

func (a Adapter) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return &http.Client{Transport: (&netguard.Guard{}).Transport()}
}

func (a Adapter) Decide(ctx context.Context, attempt app.Attempt, req domain.DecideRequest) (domain.DecideResult, domain.FailureClass, error) {
	prompt := req.Prompt
	if req.Kind == domain.DecisionYesNo {
		prompt += "\nAnswer with yes or no."
	} else {
		prompt += "\nAnswer with one of: " + strings.Join(req.Options, ", ")
	}
	text, class, err := a.complete(ctx, attempt, prompt)
	if err != nil {
		return domain.DecideResult{}, class, err
	}
	switch req.Kind {
	case domain.DecisionYesNo:
		yes, err := domain.ParseYesNo(strings.ToLower(strings.TrimSpace(text)))
		if err != nil {
			return domain.DecideResult{}, domain.ClassCaller, err
		}
		return domain.DecideResult{YesNo: yes}, domain.ClassOK, nil
	case domain.DecisionChoice:
		got, err := domain.ParseChoice(strings.TrimSpace(text), req.Options)
		if err != nil {
			return domain.DecideResult{}, domain.ClassCaller, err
		}
		return domain.DecideResult{Choice: got}, domain.ClassOK, nil
	default:
		return domain.DecideResult{}, domain.ClassCaller, fmt.Errorf("gateway: decide kind %q", req.Kind)
	}
}

func (a Adapter) Chat(ctx context.Context, attempt app.Attempt, prompt string) (string, domain.FailureClass, error) {
	return a.complete(ctx, attempt, prompt)
}

func (a Adapter) complete(ctx context.Context, attempt app.Attempt, prompt string) (string, domain.FailureClass, error) {
	base := strings.TrimRight(attempt.BaseURL, "/")
	if base == "" {
		return "", domain.ClassCaller, fmt.Errorf("gateway: adapter base URL is empty")
	}
	body, err := json.Marshal(map[string]any{
		"model":    attempt.Model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", domain.ClassCaller, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", domain.ClassCaller, err
	}
	req.Header.Set("Content-Type", "application/json")
	if attempt.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+attempt.Secret)
	}
	res, err := a.client().Do(req)
	if err != nil {
		return "", classForErr(err), err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", domain.ClassTransient, err
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return "", domain.ClassRateLimit, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", domain.ClassCredential, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode == http.StatusNotFound {
		return "", domain.ClassModelAccess, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode >= 500 {
		return "", domain.ClassTransient, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", domain.ClassCaller, fmt.Errorf("gateway: provider %d", res.StatusCode)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", domain.ClassCaller, fmt.Errorf("gateway: provider body")
	}
	return parsed.Choices[0].Message.Content, domain.ClassOK, nil
}

func classForErr(err error) domain.FailureClass {
	if err == nil {
		return domain.ClassOK
	}
	if strings.Contains(strings.ToLower(err.Error()), "timeout") {
		return domain.ClassTimeout
	}
	return domain.ClassTransient
}
