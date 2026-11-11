package app

import (
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// MarkUsable sets Usable on every route: whether one of its hops reaches an
// account the person can use — one of accounts, their own and the workspace's
// — that is Connected and lists the hop's model. The rules are domain's; this
// only carries the listed rows over to them.
func MarkUsable(routes []ListedRoute, accounts []domain.Provider) {
	for i := range routes {
		routes[i].Usable = domain.UsableRoute(usableView(routes[i]), accounts)
	}
}

// UsableAgent reports whether one agent may be offered to the person the
// accounts belong to: enabled, and its model, or its route — the named one,
// else defaultRoute — usable.
func UsableAgent(enabled bool, route, modelProvider, modelID string, routes []ListedRoute, accounts []domain.Provider, defaultRoute string) bool {
	views := make([]domain.UsableRouteView, 0, len(routes))
	for _, r := range routes {
		views = append(views, usableView(r))
	}
	return domain.UsableAgent(domain.AgentView{
		Enabled:       enabled,
		Route:         route,
		ModelProvider: modelProvider,
		ModelID:       modelID,
	}, views, accounts, defaultRoute)
}

func usableView(r ListedRoute) domain.UsableRouteView {
	view := domain.UsableRouteView{Name: r.Name, Capability: domain.Capability(r.Capability)}
	for _, hop := range r.Hops {
		view.Hops = append(view.Hops, domain.UsableHop{AccountID: hop.AccountID, Model: hop.Model})
	}
	return view
}
