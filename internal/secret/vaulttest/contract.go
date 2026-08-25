// Package vaulttest defines the behavioral contract for a Kern system
// credential Vault adapter.
package vaulttest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/userInner/kern/internal/secret"
)

// Factory returns a fresh, ready Vault. Implementations should register any
// backend-specific cleanup with t.Cleanup and skip the test when the platform
// service is intentionally unavailable.
type Factory func(t *testing.T) secret.Vault

// Run executes the mandatory Vault adapter contract.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	if factory == nil {
		t.Fatal("vaulttest: factory is nil")
	}
	t.Run("readiness", func(t *testing.T) {
		vault := open(t, factory)
		if err := vault.Check(t.Context()); err != nil {
			t.Fatalf("Check() error = %v", err)
		}
	})
	t.Run("opaque-lifecycle", func(t *testing.T) {
		vault := open(t, factory)
		value := "Kern contract secret · line 1\nline 2"
		reference, err := vault.Put(t.Context(), value)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		t.Cleanup(func() { _ = vault.Delete(context.Background(), reference) })
		if reference == "" || reference != strings.TrimSpace(reference) {
			t.Fatalf("Put() reference = %q", reference)
		}
		if strings.Contains(reference, value) || !vault.Owns(reference) {
			t.Fatalf("reference is not opaque or owned: %q", reference)
		}
		resolved, err := vault.Resolve(t.Context(), reference)
		if err != nil || resolved != value {
			t.Fatalf("Resolve() = %q, %v", resolved, err)
		}
		if err := vault.Check(t.Context()); err != nil {
			t.Fatalf("Check(after Put) error = %v", err)
		}
		resolved, err = vault.Resolve(t.Context(), reference)
		if err != nil || resolved != value {
			t.Fatalf("Resolve(after Check) = %q, %v", resolved, err)
		}
		if err := vault.Delete(t.Context(), reference); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if err := vault.Delete(t.Context(), reference); err != nil {
			t.Fatalf("Delete(idempotent) error = %v", err)
		}
		if _, err := vault.Resolve(t.Context(), reference); !errors.Is(err, secret.ErrNotFound) {
			t.Fatalf("Resolve(deleted) error = %v, want ErrNotFound", err)
		}
	})
	t.Run("independent-references", func(t *testing.T) {
		vault := open(t, factory)
		first, err := vault.Put(t.Context(), "same-value")
		if err != nil {
			t.Fatalf("Put(first) error = %v", err)
		}
		t.Cleanup(func() { _ = vault.Delete(context.Background(), first) })
		second, err := vault.Put(t.Context(), "same-value")
		if err != nil {
			t.Fatalf("Put(second) error = %v", err)
		}
		t.Cleanup(func() { _ = vault.Delete(context.Background(), second) })
		if first == second {
			t.Fatalf("two entries reused reference %q", first)
		}
		if err := vault.Delete(t.Context(), first); err != nil {
			t.Fatalf("Delete(first) error = %v", err)
		}
		resolved, err := vault.Resolve(t.Context(), second)
		if err != nil || resolved != "same-value" {
			t.Fatalf("Resolve(second after first delete) = %q, %v", resolved, err)
		}
	})
	t.Run("unsupported-reference", func(t *testing.T) {
		vault := open(t, factory)
		const reference = "env:KERN_VAULT_CONTRACT_UNSUPPORTED"
		if vault.Owns(reference) {
			t.Fatalf("Owns(%q) = true", reference)
		}
		if _, err := vault.Resolve(t.Context(), reference); !errors.Is(err, secret.ErrUnsupported) {
			t.Fatalf("Resolve(unsupported) error = %v", err)
		}
		if err := vault.Delete(t.Context(), reference); !errors.Is(err, secret.ErrUnsupported) {
			t.Fatalf("Delete(unsupported) error = %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		vault := open(t, factory)
		reference, err := vault.Put(t.Context(), "cancellation-value")
		if err != nil {
			t.Fatalf("Put(setup) error = %v", err)
		}
		t.Cleanup(func() { _ = vault.Delete(context.Background(), reference) })
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := vault.Check(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatalf("Check(cancelled) error = %v", err)
		}
		if _, err := vault.Put(cancelled, "must-not-write"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Put(cancelled) error = %v", err)
		}
		if _, err := vault.Resolve(cancelled, reference); !errors.Is(err, context.Canceled) {
			t.Fatalf("Resolve(cancelled) error = %v", err)
		}
		if err := vault.Delete(cancelled, reference); !errors.Is(err, context.Canceled) {
			t.Fatalf("Delete(cancelled) error = %v", err)
		}
		resolved, err := vault.Resolve(t.Context(), reference)
		if err != nil || resolved != "cancellation-value" {
			t.Fatalf("cancelled operation changed entry: %q, %v", resolved, err)
		}
	})
}

func open(t *testing.T, factory Factory) secret.Vault {
	t.Helper()
	vault := factory(t)
	if vault == nil {
		t.Fatal("vaulttest: factory returned nil")
	}
	return vault
}
