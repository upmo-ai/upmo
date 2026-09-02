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
	Decide(ctx context.Context, attempt Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error)
}

// Chatter is the chat adapter for one account.adapter value.
type Chatter interface {
	Chat(ctx context.Context, attempt Attempt, req domain.Request) (string, domain.Usage, domain.FailureClass, error)
}

// Embedder is the embed adapter for one account.adapter value.
type Embedder interface {
	Embed(ctx context.Context, attempt Attempt, inputs []string) ([][]float32, domain.Usage, domain.FailureClass, error)
}

// Prices looks up a catalog row. Missing is not an error.
type Prices interface {
	Price(ctx context.Context, model string) (domain.Price, bool, error)
}

// Ledger stores one row per attempt.
type Ledger interface {
	Record(ctx context.Context, row domain.Entry) error
}

// Bundles loads a named tool surface, or the one assigned to an agent.
type Bundles interface {
	Bundle(ctx context.Context, name string) (domain.Bundle, error)
	AgentBundle(ctx context.Context, agentID string) (domain.Bundle, error)
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
	Prices  Prices
	Ledger  Ledger
	Bundles Bundles
	Decide  map[string]Decider
	Chat    map[string]Chatter
	Embed   map[string]Embedder
}

func (d Dispatch) write(ctx context.Context, route string, link domain.Link, who domain.Principal, class domain.FailureClass, usage domain.Usage) error {
	if d.Ledger == nil {
		return nil
	}
	var price domain.Price
	found := false
	if d.Prices != nil {
		p, ok, err := d.Prices.Price(ctx, link.Model)
		if err != nil {
			return err
		}
		price, found = p, ok
	}
	return d.Ledger.Record(ctx, domain.Entry{
		Route:        route,
		LinkPosition: link.Position,
		AccountID:    link.Account.ID,
		Model:        link.Model,
		PlanKind:     link.Account.PlanKind,
		Class:        class,
		Principal:    who,
		Usage:        usage,
		CostUSD:      domain.CostUSD(link.Account.PlanKind, usage, price, found),
		PriceVersion: price.Version,
	})
}

func (d Dispatch) DecideCall(ctx context.Context, routeName, credentialRef string, who domain.Principal, req domain.DecideRequest) (domain.DecideResult, error) {
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
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return domain.DecideResult{}, werr
			}
			if domain.NextAction(domain.ClassCredential, 0) == domain.ActionNext {
				last = err
				continue
			}
			return domain.DecideResult{}, err
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret}
		retries := link.RetryBudget
		for {
			result, usage, class, err := decider.Decide(ctx, attempt, req)
			if werr := d.write(ctx, routeName, link, who, class, usage); werr != nil {
				return domain.DecideResult{}, werr
			}
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

func (d Dispatch) applyBundle(ctx context.Context, who domain.Principal, in domain.Input) (domain.Input, error) {
	var (
		names     []string
		useBundle bool
	)
	switch {
	case in.Bundle != "":
		if d.Bundles == nil {
			return in, fmt.Errorf("gateway: no bundle %q", in.Bundle)
		}
		b, err := d.Bundles.Bundle(ctx, in.Bundle)
		if err != nil {
			return in, err
		}
		names, useBundle = b.Tools, true
	case who.Agent != "":
		if d.Bundles == nil {
			return in, fmt.Errorf("gateway: agent has no tool bundle")
		}
		b, err := d.Bundles.AgentBundle(ctx, who.Agent)
		if err != nil {
			return in, err
		}
		names, useBundle = b.Tools, true
	default:
		return in, nil
	}
	allow, err := domain.ResolveAllow(names, in.AllowTools, useBundle)
	if err != nil {
		return in, err
	}
	in.AllowTools = allow
	in.Mask = true
	return in, nil
}

func (d Dispatch) ChatCall(ctx context.Context, routeName, credentialRef string, who domain.Principal, in domain.Input) (string, error) {
	in, err := d.applyBundle(ctx, who, in)
	if err != nil {
		return "", err
	}
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
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return "", werr
			}
			last = err
			continue
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret}
		retries := link.RetryBudget
		for {
			text, usage, class, err := chatter.Chat(ctx, attempt, built)
			if werr := d.write(ctx, routeName, link, who, class, usage); werr != nil {
				return "", werr
			}
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

// EmbedCall walks an embed route and returns the link model with one vector
// per input. A vector of the wrong shape skips that link. Nothing is returned
// until every vector is EmbedDim, finite, and non-zero.
func (d Dispatch) EmbedCall(ctx context.Context, routeName, credentialRef string, who domain.Principal, inputs []string) (string, [][]float32, error) {
	if err := domain.CheckEmbedInputs(inputs); err != nil {
		return "", nil, err
	}
	route, err := d.Routes.ByName(ctx, routeName)
	if err != nil {
		return "", nil, err
	}
	if route.Capability != domain.CapEmbed {
		return "", nil, fmt.Errorf("gateway: route %q is %s", routeName, route.Capability)
	}
	links, err := route.LinksFor(credentialRef)
	if err != nil {
		return "", nil, err
	}
	var last error
	for _, link := range links {
		embedder := d.Embed[link.Account.Adapter]
		if embedder == nil {
			last = fmt.Errorf("gateway: no embed adapter %q", link.Account.Adapter)
			continue
		}
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return "", nil, werr
			}
			last = err
			continue
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret}
		retries := link.RetryBudget
		for {
			vectors, usage, class, err := embedder.Embed(ctx, attempt, inputs)
			if class == domain.ClassOK && err == nil {
				if verr := domain.CheckEmbeddings(len(inputs), vectors); verr != nil {
					class = domain.ClassModelAccess
					err = verr
					vectors = nil
				}
			}
			if werr := d.write(ctx, routeName, link, who, class, usage); werr != nil {
				return "", nil, werr
			}
			if class == domain.ClassOK && err == nil {
				return link.Model, vectors, nil
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
				return "", nil, last
			}
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("gateway: route %q exhausted", routeName)
	}
	return "", nil, last
}
