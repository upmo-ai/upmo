package app

import (
	"context"
	"fmt"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Attempt is one provider call after secrets are resolved. It does not
// include the secret in logs; adapters receive it as an argument.
type Attempt struct {
	Adapter string
	BaseURL string
	Model   string
	Secret  string
}

// Decider is the decide adapter for one account.adapter value.
type Decider interface {
	Decide(ctx context.Context, attempt Attempt, req domain.DecideRequest) (domain.DecideResult, domain.FailureClass, error)
}

// Chatter is the chat adapter for one account.adapter value.
type Chatter interface {
	Chat(ctx context.Context, attempt Attempt, req domain.Request) (string, domain.FailureClass, error)
}

// Routes loads one named route from the gateway schema.
type Routes interface {
	ByName(ctx context.Context, name string) (domain.Route, error)
}

// Secrets resolves a credential_ref. Implementations must not log the value.
type Secrets interface {
	Get(ref string) (string, error)
}

// Dispatch walks a route's links using the failover table.
type Dispatch struct {
	Routes  Routes
	Secrets Secrets
	Decide  map[string]Decider
	Chat    map[string]Chatter
}

func (d Dispatch) DecideCall(ctx context.Context, routeName, credentialRef string, req domain.DecideRequest) (domain.DecideResult, error) {
	if err := req.Check(); err != nil {
		return domain.DecideResult{}, err
	}
	route, err := d.Routes.ByName(ctx, routeName)
	if err != nil {
		return domain.DecideResult{}, err
	}
	if route.Capability != domain.CapDecide || route.Decision != req.Kind {
		return domain.DecideResult{}, fmt.Errorf("gateway: route %q is %s %s", routeName, route.Capability, route.Decision)
	}
	links, err := route.LinksFor(credentialRef)
	if err != nil {
		return domain.DecideResult{}, err
	}
	var last error
	for _, link := range links {
		decider := d.Decide[link.Account.Adapter]
		if decider == nil {
			last = fmt.Errorf("gateway: no decide adapter %q", link.Account.Adapter)
			continue
		}
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			if domain.NextAction(domain.ClassCredential, 0) == domain.ActionNext {
				last = err
				continue
			}
			return domain.DecideResult{}, err
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret}
		retries := link.RetryBudget
		for {
			result, class, err := decider.Decide(ctx, attempt, req)
			if class == domain.ClassOK && err == nil {
				return result, nil
			}
			last = err
			if last == nil {
				last = fmt.Errorf("gateway: %s", class)
			}
			switch domain.NextAction(class, retries) {
			case domain.ActionRetry:
				retries--
				continue
			case domain.ActionNext:
			default:
				return domain.DecideResult{}, last
			}
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("gateway: route %q exhausted", routeName)
	}
	return domain.DecideResult{}, last
}

func (d Dispatch) ChatCall(ctx context.Context, routeName, credentialRef string, in domain.Input) (string, error) {
	built, err := domain.Build(in)
	if err != nil {
		return "", err
	}
	if len(built.Messages) == 0 {
		return "", fmt.Errorf("gateway: chat prompt is empty")
	}
	route, err := d.Routes.ByName(ctx, routeName)
	if err != nil {
		return "", err
	}
	if route.Capability != domain.CapChat {
		return "", fmt.Errorf("gateway: route %q is %s", routeName, route.Capability)
	}
	links, err := route.LinksFor(credentialRef)
	if err != nil {
		return "", err
	}
	var last error
	for _, link := range links {
		chatter := d.Chat[link.Account.Adapter]
		if chatter == nil {
			last = fmt.Errorf("gateway: no chat adapter %q", link.Account.Adapter)
			continue
		}
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			last = err
			continue
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret}
		retries := link.RetryBudget
		for {
			text, class, err := chatter.Chat(ctx, attempt, built)
			if class == domain.ClassOK && err == nil {
				return text, nil
			}
			last = err
			if last == nil {
				last = fmt.Errorf("gateway: %s", class)
			}
			switch domain.NextAction(class, retries) {
			case domain.ActionRetry:
				retries--
				continue
			case domain.ActionNext:
			default:
				return "", last
			}
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("gateway: route %q exhausted", routeName)
	}
	return "", last
}
