package secret

import (
	"context"
	"errors"
	"testing"
)

type resolverFunc func(context.Context, string) (string, error)

func (f resolverFunc) Resolve(ctx context.Context, reference string) (string, error) {
	return f(ctx, reference)
}

func TestChainRoutesByUnsupportedResult(t *testing.T) {
	chain, err := NewChain(
		resolverFunc(func(context.Context, string) (string, error) { return "", ErrUnsupported }),
		resolverFunc(func(_ context.Context, reference string) (string, error) {
			if reference != "keyring:model/default" {
				t.Fatalf("reference = %q", reference)
			}
			return "private-value", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewChain() error = %v", err)
	}
	value, err := chain.Resolve(t.Context(), "keyring:model/default")
	if err != nil || value != "private-value" {
		t.Fatalf("Resolve() = %q, %v", value, err)
	}
}

func TestChainDoesNotFallThroughRecognizedMissingSecret(t *testing.T) {
	want := errors.New("recognized secret missing")
	called := false
	chain, err := NewChain(
		resolverFunc(func(context.Context, string) (string, error) { return "", want }),
		resolverFunc(func(context.Context, string) (string, error) {
			called = true
			return "unexpected", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewChain() error = %v", err)
	}
	if _, err := chain.Resolve(t.Context(), "keyring:model/default"); !errors.Is(err, want) {
		t.Fatalf("Resolve() error = %v, want %v", err, want)
	}
	if called {
		t.Fatal("fallback resolver was called after a recognized error")
	}
}

func TestNewChainRejectsMissingResolver(t *testing.T) {
	if _, err := NewChain(); err == nil {
		t.Fatal("NewChain() error = nil")
	}
	if _, err := NewChain(nil); err == nil {
		t.Fatal("NewChain(nil) error = nil")
	}
}
