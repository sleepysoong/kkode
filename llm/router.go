package llm

import (
	"context"
	"fmt"
	"strings"
)

type Router struct {
	providers map[string]Provider
	aliases   map[string]string
}

func NewRouter() *Router {
	return &Router{providers: map[string]Provider{}, aliases: map[string]string{}}
}

func (r *Router) Register(name string, provider Provider) {
	if r.providers == nil {
		r.providers = map[string]Provider{}
	}
	r.providers[name] = provider
}

func (r *Router) Alias(prefix, provider string) {
	if r.aliases == nil {
		r.aliases = map[string]string{}
	}
	r.aliases[prefix] = provider
}

func (r *Router) ProviderFor(model string) (Provider, string, error) {
	if provider, rest, ok := strings.Cut(model, "/"); ok {
		if p := r.providers[provider]; p != nil {
			return p, rest, nil
		}
	}
	for prefix, provider := range r.aliases {
		if strings.HasPrefix(model, prefix) {
			if p := r.providers[provider]; p != nil {
				return p, strings.TrimPrefix(model, prefix), nil
			}
		}
	}
	if p := r.providers["default"]; p != nil {
		return p, model, nil
	}
	return nil, "", fmt.Errorf("no provider registered for model %q", model)
}

func (r *Router) Generate(ctx context.Context, req Request) (*Response, error) {
	p, model, err := r.ProviderFor(req.Model)
	if err != nil {
		return nil, err
	}
	req.Model = model
	return p.Generate(ctx, req)
}
