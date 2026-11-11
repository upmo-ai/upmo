package app

import (
	"context"
	"fmt"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Attempt is one provider call after secrets are resolved. It does not
// include the secret in logs; adapters receive it as an argument.
type Attempt struct {
	Adapter string
	BaseURL string
	Model   string
	Secret  string
	// PlanKind decides which network addresses the adapter may dial: only a
	// local account may reach a private or loopback address.
	PlanKind string
}

// Decider is the decide adapter for one account.adapter value.
type Decider interface {
	Decide(ctx context.Context, attempt Attempt, req domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error)
}

// Chatter is the chat adapter for one account.adapter value.
type Chatter interface {
	Chat(ctx context.Context, attempt Attempt, req domain.Request) (string, domain.Usage, domain.FailureClass, error)
}

// StreamChatter is the streaming chat adapter for one account.adapter value,
// beside Chatter. emit receives each text delta in order; when it returns an
// error the caller is gone and the adapter stops reading.
type StreamChatter interface {
	ChatStream(ctx context.Context, attempt Attempt, req domain.Request, emit func(text string) error) (domain.Usage, domain.FailureClass, error)
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

// PinAccounts looks up the provider account a pin names. ok is false when
// there is no such account or it does not list the model.
type PinAccounts interface {
	PinAccount(ctx context.Context, id, model string) (account domain.Account, ok bool, err error)
	// PinProvider resolves a provider name and a model for one person: their
	// own account of that provider that lists the model, else a global
	// account of it that does. ok is false when there is none.
	PinProvider(ctx context.Context, user, provider, model string) (account domain.Account, ok bool, err error)
}

// Secrets resolves a credential_ref. Implementations must not log the value.
type Secrets interface {
	Get(ref string) (string, error)
}

// Caps reports the capabilities one model offers. ok is false when the model
// is absent from the catalog.
type Caps func(model string) (have []string, ok bool)

// Dispatch walks a route's links using the failover table.
type Dispatch struct {
	Routes  Routes
	Pins    PinAccounts
	Secrets Secrets
	Prices  Prices
	Ledger  Ledger
	Bundles Bundles
	Decide  map[string]Decider
	Chat    map[string]Chatter
	Stream  map[string]StreamChatter
	Embed   map[string]Embedder
	Gates   *Gates
	Caps    Caps
	// Status hears the outcome of every attempt, by credential ref. It must
	// not block. Nil hears nothing.
	Status Statuses
}

func (d Dispatch) write(ctx context.Context, route string, link domain.Link, who domain.Principal, class domain.FailureClass, usage domain.Usage) error {
	if d.Ledger == nil {
		return nil
	}
	_, err := d.writeCost(ctx, route, link, who, class, usage)
	return err
}

// writeCost prices one attempt, records its ledger row and returns the cost
// it priced, so a caller can report the same number the row holds.
func (d Dispatch) writeCost(ctx context.Context, route string, link domain.Link, who domain.Principal, class domain.FailureClass, usage domain.Usage) (*float64, error) {
	cost, version, err := d.priceCost(ctx, link, usage)
	if err != nil {
		return nil, err
	}
	if d.Ledger == nil {
		return cost, nil
	}
	return cost, d.Ledger.Record(ctx, domain.Entry{
		Route:        route,
		LinkPosition: link.Position,
		AccountID:    link.Account.ID,
		Model:        link.Model,
		PlanKind:     link.Account.PlanKind,
		Class:        class,
		Principal:    who,
		Usage:        usage,
		CostUSD:      cost,
		PriceVersion: version,
	})
}

// priceCost prices one attempt exactly as its ledger row does: the cost and
// the catalog version it came from.
func (d Dispatch) priceCost(ctx context.Context, link domain.Link, usage domain.Usage) (*float64, string, error) {
	var price domain.Price
	found := false
	if d.Prices != nil {
		p, ok, err := d.Prices.Price(ctx, link.Model)
		if err != nil {
			return nil, "", err
		}
		price, found = p, ok
	}
	return domain.CostUSD(link.Account.PlanKind, usage, price, found), price.Version, nil
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
		if skip, err := d.skip(link); skip {
			last = err
			continue
		}
		started := time.Now()
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return domain.DecideResult{}, werr
			}
			d.seen(link.Account.CredentialRef, domain.ClassCredential, started)
			if domain.NextAction(domain.ClassCredential, 0) == domain.ActionNext {
				last = err
				continue
			}
			return domain.DecideResult{}, err
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret, PlanKind: link.Account.PlanKind}
		retries := link.RetryBudget
		for {
			started := time.Now()
			result, usage, class, err := decider.Decide(ctx, attempt, req)
			if werr := d.write(ctx, routeName, link, who, class, usage); werr != nil {
				return domain.DecideResult{}, werr
			}
			d.seen(link.Account.CredentialRef, class, started)
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
		if skip, err := d.skip(link); skip {
			last = err
			continue
		}
		started := time.Now()
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return "", werr
			}
			d.seen(link.Account.CredentialRef, domain.ClassCredential, started)
			last = err
			continue
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret, PlanKind: link.Account.PlanKind}
		retries := link.RetryBudget
		for {
			started := time.Now()
			text, usage, class, err := chatter.Chat(ctx, attempt, built)
			if werr := d.write(ctx, routeName, link, who, class, usage); werr != nil {
				return "", werr
			}
			d.seen(link.Account.CredentialRef, class, started)
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

// StreamNotice names the link that failed before its first text and the link
// the walk tries next, which is empty when the walk is over.
type StreamNotice struct {
	FailedModel string
	Class       domain.FailureClass
	NextModel   string
}

// StreamDone is the successful end of a streamed reply.
type StreamDone struct {
	Model   string
	Usage   domain.Usage
	CostUSD *float64
}

// StreamFail is the failed end of a streamed reply. Model is the model that
// was writing when the call failed.
type StreamFail struct {
	Model string
	Class domain.FailureClass
	Err   error
}

// StreamSink hears one streamed chat call: text deltas in order, a notice
// each time a link fails before its first text and the walk moves on, and
// exactly one Done or Fail. Delta and Notice return an error when the
// caller is gone; the walk then stops at once.
type StreamSink interface {
	Delta(text string) error
	Notice(notice StreamNotice) error
	Done(done StreamDone)
	Fail(fail StreamFail)
}

// ChatStreamCall walks a chat route streaming the reply to sink. Failover
// and retries happen only while no text has been emitted; once text has
// arrived a failure ends the call with no further link. A credential that
// cannot be resolved fails its link like any pre-text fault. One ledger row
// is written per attempt, as in ChatCall.
func (d Dispatch) ChatStreamCall(ctx context.Context, routeName, credentialRef string, who domain.Principal, in domain.Input, maxOutputTokens int, sink StreamSink) error {
	return d.ChatStreamPinned(ctx, routeName, credentialRef, who, in, domain.Pin{}, maxOutputTokens, sink)
}

// ChatStreamPinned is ChatStreamCall with an optional pin. A pin is tried
// before the route's chain, on the credential of the account it names, and
// the chain follows as the fallback with the pinned account and model taken
// out of it. A pin names an account, or a provider that is resolved to the
// person's own account of it that lists the model, else a global one. A pin
// to another person's account is refused before any frame.
// A pin that cannot be used (no such account, or no stream adapter for it)
// is skipped, and the sink hears a notice that names the pinned model before
// the chain starts. The zero pin leaves the call as ChatStreamCall made it.
func (d Dispatch) ChatStreamPinned(ctx context.Context, routeName, credentialRef string, who domain.Principal, in domain.Input, pin domain.Pin, maxOutputTokens int, sink StreamSink) error {
	if err := pin.Check(); err != nil {
		return err
	}
	if !pin.IsZero() && (who.User == "" || credentialRef != "") {
		return domain.ErrPinCaller
	}
	// Whose account the pin names is settled first, so another person's
	// account is refused whatever else is wrong with the call.
	account, found, own, err := d.pinAccount(ctx, who, pin)
	if err != nil {
		return err
	}
	in, err = d.applyBundle(ctx, who, in)
	if err != nil {
		return err
	}
	built, err := domain.Build(in)
	if err != nil {
		return err
	}
	if len(built.Messages) == 0 {
		return fmt.Errorf("gateway: chat prompt is empty")
	}
	built.MaxOutputTokens = maxOutputTokens
	// ByName checks the route, and a route with no hops is a config error that
	// a pin does not bypass.
	route, err := d.Routes.ByName(ctx, routeName)
	if err != nil {
		return err
	}
	if route.Capability != domain.CapChat {
		return fmt.Errorf("gateway: route %q is %s", routeName, route.Capability)
	}
	var links []domain.Link
	if credentialRef == "" && who.User != "" {
		// A streamed reply is paid by the person it is for when the route has
		// links of theirs, and by the house otherwise.
		links, err = route.LinksForUser(who.User)
	} else {
		links, err = route.LinksFor(credentialRef)
	}
	var (
		last      error
		lastModel string
		lastClass domain.FailureClass
		pinFirst  bool
	)
	if !pin.IsZero() {
		pinned := d.pinLink(route, account, found, pin)
		if own {
			// A pin to the person's own account is paid by them alone: the
			// fallback is their own links of the route, which may be none.
			links, err = route.OwnLinks(who.User), nil
		}
		if pinned != nil {
			// The pin does not need the route to have a link for the person.
			err = nil
			links = domain.Pinned(*pinned, links)
			pinFirst = true
		} else {
			if err != nil {
				return err
			}
			next := ""
			if len(links) > 0 {
				next = links[0].Model
			}
			if nerr := sink.Notice(StreamNotice{FailedModel: pin.Model, Class: domain.ClassModelAccess, NextModel: next}); nerr != nil {
				return nerr
			}
			last, lastModel, lastClass = fmt.Errorf("gateway: the pinned model cannot be used"), pin.Model, domain.ClassModelAccess
		}
	}
	if err != nil {
		return err
	}
	// skipped hears of a pinned link that is passed over before any call: the
	// person is told, like for a pin that cannot be used at all.
	skipped := func(i int, link domain.Link, cause error) error {
		last = cause
		if !pinFirst || i != 0 {
			return nil
		}
		lastModel, lastClass = link.Model, domain.ClassModelAccess
		next := ""
		if len(links) > 1 {
			next = links[1].Model
		}
		return sink.Notice(StreamNotice{FailedModel: link.Model, Class: domain.ClassModelAccess, NextModel: next})
	}
	for i, link := range links {
		streamer := d.Stream[link.Account.Adapter]
		if streamer == nil {
			if nerr := skipped(i, link, fmt.Errorf("gateway: no stream adapter %q", link.Account.Adapter)); nerr != nil {
				return nerr
			}
			continue
		}
		if skip, err := d.skip(link); skip {
			if nerr := skipped(i, link, err); nerr != nil {
				return nerr
			}
			continue
		}
		started := time.Now()
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return werr
			}
			d.seen(link.Account.CredentialRef, domain.ClassCredential, started)
			last, lastModel, lastClass = err, link.Model, domain.ClassCredential
			next := ""
			if i+1 < len(links) {
				next = links[i+1].Model
			}
			if nerr := sink.Notice(StreamNotice{FailedModel: link.Model, Class: domain.ClassCredential, NextModel: next}); nerr != nil {
				return nerr
			}
			continue
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret, PlanKind: link.Account.PlanKind}
		retries := link.RetryBudget
		for {
			started := time.Now()
			text := false
			usage, class, err := chatStream(ctx, streamer, attempt, built, func(delta string) error {
				if err := sink.Delta(delta); err != nil {
					return err
				}
				text = true
				return nil
			})
			cost, werr := d.writeCost(ctx, routeName, link, who, class, usage)
			if werr != nil {
				return werr
			}
			d.seen(link.Account.CredentialRef, class, started)
			if class == domain.ClassOK && err == nil {
				sink.Done(StreamDone{Model: link.Model, Usage: usage, CostUSD: cost})
				return nil
			}
			if err == nil {
				err = fmt.Errorf("gateway: %s", class)
			}
			last, lastModel, lastClass = err, link.Model, class
			if text {
				// The caller has text; a failure now ends the call.
				sink.Fail(StreamFail{Model: link.Model, Class: class, Err: err})
				return nil
			}
			switch domain.NextAction(class, retries) {
			case domain.ActionRetry:
				if nerr := sink.Notice(StreamNotice{FailedModel: link.Model, Class: class, NextModel: link.Model}); nerr != nil {
					return nerr
				}
				retries--
				continue
			case domain.ActionNext:
				next := ""
				if i+1 < len(links) {
					next = links[i+1].Model
				}
				if nerr := sink.Notice(StreamNotice{FailedModel: link.Model, Class: class, NextModel: next}); nerr != nil {
					return nerr
				}
			default:
				sink.Fail(StreamFail{Model: link.Model, Class: class, Err: err})
				return nil
			}
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("gateway: route %q exhausted", routeName)
	}
	sink.Fail(StreamFail{Model: lastModel, Class: lastClass, Err: last})
	return nil
}

// chatStream runs one streaming attempt. A panic in the adapter fails that
// attempt as internal so the ledger row, the status note and the end of the
// walk still happen. The panic value stays in the returned error; the wire
// carries a fixed phrase instead. A panic in emit is the gateway's own and
// is raised again, not booked against the provider.
func chatStream(ctx context.Context, streamer StreamChatter, attempt Attempt, req domain.Request, emit func(string) error) (usage domain.Usage, class domain.FailureClass, err error) {
	inEmit := false
	defer func() {
		if rec := recover(); rec != nil {
			if inEmit {
				panic(rec)
			}
			usage, class, err = domain.Usage{}, domain.ClassInternal, fmt.Errorf("gateway: stream adapter panic: %v", rec)
		}
	}()
	return streamer.ChatStream(ctx, attempt, req, func(text string) error {
		inEmit = true
		err := emit(text)
		inEmit = false
		return err
	})
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
		if skip, err := d.skip(link); skip {
			last = err
			continue
		}
		started := time.Now()
		secret, err := d.Secrets.Get(link.Account.CredentialRef)
		if err != nil {
			if werr := d.write(ctx, routeName, link, who, domain.ClassCredential, domain.Usage{}); werr != nil {
				return "", nil, werr
			}
			d.seen(link.Account.CredentialRef, domain.ClassCredential, started)
			last = err
			continue
		}
		attempt := Attempt{Adapter: link.Account.Adapter, BaseURL: link.Account.BaseURL, Model: link.Model, Secret: secret, PlanKind: link.Account.PlanKind}
		retries := link.RetryBudget
		for {
			started := time.Now()
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
			d.seen(link.Account.CredentialRef, class, started)
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

// pinAccount looks up the account a pin names and settles whose it is. It
// returns an error for an account that belongs to another person, which is
// refused. found is false when there is no such account, or no lookup; own
// says the account is the person's own.
func (d Dispatch) pinAccount(ctx context.Context, who domain.Principal, pin domain.Pin) (account domain.Account, found, own bool, err error) {
	if pin.IsZero() || d.Pins == nil {
		return domain.Account{}, false, false, nil
	}
	if pin.Provider != "" {
		account, found, err = d.Pins.PinProvider(ctx, who.User, pin.Provider, pin.Model)
	} else {
		account, found, err = d.Pins.PinAccount(ctx, pin.AccountID, pin.Model)
	}
	if err != nil || !found {
		return domain.Account{}, false, false, err
	}
	if !domain.PinUsable(account, who.User) {
		return domain.Account{}, false, false, domain.ErrPinAccount
	}
	return account, true, account.OwnerUserID != "", nil
}

// pinLink makes the link that goes first from a found account, with the
// credential ref of that account. It is nil for a pin that cannot be used: no
// such account, or no stream adapter for it.
func (d Dispatch) pinLink(route domain.Route, account domain.Account, found bool, pin domain.Pin) *domain.Link {
	if !found || d.Stream[account.Adapter] == nil {
		return nil
	}
	link := route.PinLink(account, pin.Model)
	return &link
}

func (d Dispatch) skip(link domain.Link) (bool, error) {
	if d.Gates != nil && !d.Gates.Closed(link.Account.CredentialRef) {
		return true, domain.ErrCooling
	}
	if len(link.Need) == 0 {
		return false, nil
	}
	if d.Caps == nil {
		return true, domain.ErrCapability
	}
	have, ok := d.Caps(link.Model)
	if !ok || domain.Fit(link.Need, have) != nil {
		return true, domain.ErrCapability
	}
	return false, nil
}

func (d Dispatch) seen(ref string, class domain.FailureClass, started time.Time) {
	if d.Gates != nil {
		d.Gates.Observe(ref, class, 0)
	}
	if d.Status != nil {
		d.Status.Note(ref, class, started)
	}
}
