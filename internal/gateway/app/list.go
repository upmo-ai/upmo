package app

import "context"

// ListedHop is one link on a route without credential material.
type ListedHop struct {
	Position int    `json:"position"`
	Adapter  string `json:"adapter"`
	Model    string `json:"model"`
	Weight   int    `json:"weight"`
	Retry    int    `json:"retry"`
}

// ListedRoute is one route and its hops for a settings screen.
type ListedRoute struct {
	Name       string      `json:"name"`
	Capability string      `json:"capability"`
	Decision   string      `json:"decision"`
	Hops       []ListedHop `json:"hops"`
}

// RouteBook lists routes without credential material.
type RouteBook interface {
	List(ctx context.Context) ([]ListedRoute, error)
}

// ListRoutes returns every route. A nil book is an empty list.
func ListRoutes(ctx context.Context, book RouteBook) ([]ListedRoute, error) {
	if book == nil {
		return []ListedRoute{}, nil
	}
	out, err := book.List(ctx)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []ListedRoute{}
	}
	return out, nil
}
