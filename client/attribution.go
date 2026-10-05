package client

import (
	"context"
	"errors"

	"github.com/draftplane/draftplane/domain"
)

// ErrNoAttribution means a write reached a PlanService implementation
// through a context that was never decorated with WithAttribution.
var ErrNoAttribution = errors.New("no attribution on context")

// attributionKey is the context key for the attribution a write must stamp.
type attributionKey struct{}

// WithAttribution returns a context carrying who is making a write.
func WithAttribution(ctx context.Context, a domain.Attribution) context.Context {
	return context.WithValue(ctx, attributionKey{}, a)
}

// AttributionFrom returns the attribution a write must stamp, or
// ErrNoAttribution. It fails closed: an undecorated context errors rather
// than stamping an empty value.
//
// Every door decorates the context it originates, and nothing decorates it
// on a caller's behalf further down. A service-layer wrapper that stamped
// attribution would defeat this fail-closed check.
func AttributionFrom(ctx context.Context) (domain.Attribution, error) {
	a, ok := ctx.Value(attributionKey{}).(domain.Attribution)
	if !ok {
		return domain.Attribution{}, ErrNoAttribution
	}
	return a, nil
}
