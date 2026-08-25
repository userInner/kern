package secret

import (
	"context"
	"errors"
	"fmt"
)

// Resolver loads one opaque secret reference at its final use boundary.
type Resolver interface {
	Resolve(ctx context.Context, reference string) (string, error)
}

// Vault is a writable system credential backend. Put returns an opaque
// reference suitable for durable storage; neither callers nor public APIs may
// derive a secret value from that reference. Delete must be idempotent so a
// failed database mutation can safely compensate a preceding Put. Check must
// perform a bounded, non-mutating readiness probe; a failed check disables new
// writes while the Resolver remains installed so recognized references fail
// closed rather than falling through to another backend.
type Vault interface {
	Resolver
	Check(ctx context.Context) error
	Owns(reference string) bool
	Put(ctx context.Context, value string) (reference string, err error)
	Delete(ctx context.Context, reference string) error
}

// Chain routes a reference across ordered, scheme-specific resolvers. A
// resolver must return ErrUnsupported when the reference is not its scheme;
// every other result is final so a missing recognized secret cannot silently
// fall through to a different backend.
type Chain struct {
	resolvers []Resolver
}

// NewChain constructs a resolver chain without exposing its mutable slice.
func NewChain(resolvers ...Resolver) (*Chain, error) {
	if len(resolvers) == 0 {
		return nil, errors.New("secret: at least one resolver is required")
	}
	copyOfResolvers := append([]Resolver(nil), resolvers...)
	for _, resolver := range copyOfResolvers {
		if resolver == nil {
			return nil, errors.New("secret: resolver is nil")
		}
	}
	return &Chain{resolvers: copyOfResolvers}, nil
}

// Resolve stops at the first resolver that recognizes the reference scheme.
func (c *Chain) Resolve(ctx context.Context, reference string) (string, error) {
	if c == nil {
		return "", errors.New("secret: resolver chain is nil")
	}
	for _, resolver := range c.resolvers {
		value, err := resolver.Resolve(ctx, reference)
		if !errors.Is(err, ErrUnsupported) {
			return value, err
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnsupported, reference)
}

var _ Resolver = (*Chain)(nil)
