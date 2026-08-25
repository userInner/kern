package vaulttest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/userInner/kern/internal/secret"
)

type memoryVault struct {
	mu     sync.Mutex
	values map[string]string
	next   int
}

func (v *memoryVault) Check(ctx context.Context) error {
	return ctx.Err()
}

func (*memoryVault) Owns(reference string) bool {
	return strings.HasPrefix(reference, "keyring:contract:")
}

func (v *memoryVault) Put(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.next++
	reference := fmt.Sprintf("keyring:contract:%d", v.next)
	v.values[reference] = value
	return reference, nil
}

func (v *memoryVault) Resolve(ctx context.Context, reference string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !v.Owns(reference) {
		return "", secret.ErrUnsupported
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.values[reference]
	if !ok {
		return "", secret.ErrNotFound
	}
	return value, nil
}

func (v *memoryVault) Delete(ctx context.Context, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !v.Owns(reference) {
		return secret.ErrUnsupported
	}
	v.mu.Lock()
	delete(v.values, reference)
	v.mu.Unlock()
	return nil
}

func TestContract(t *testing.T) {
	Run(t, func(*testing.T) secret.Vault {
		return &memoryVault{values: make(map[string]string)}
	})
}
