// Package systemvault stores opaque Kern credentials in the operating
// system's native credential service.
package systemvault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	keyring "github.com/zalando/go-keyring"

	"github.com/userInner/kern/internal/secret"
)

const (
	serviceName     = "dev.kern.core"
	referencePrefix = "keyring:kern:v1:"
	accountHexBytes = 16
	maxSecretBytes  = 2 << 10
	probeTimeout    = 2 * time.Second
	resolveTimeout  = 15 * time.Second
	probeAccount    = "__kern_readiness_probe__"
)

type backend interface {
	Set(service, account, value string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
}

type nativeBackend struct{}

func (nativeBackend) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

func (nativeBackend) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}

func (nativeBackend) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

// Vault is a native system credential adapter. The underlying OS APIs do not
// expose cancellation for mutating calls, so Put and Delete check cancellation
// before starting and then report their definitive OS result. This ensures the
// app always receives a reference for a successful Put and can compensate it.
type Vault struct {
	backend backend
}

// New constructs the production system credential Vault.
func New() *Vault {
	return &Vault{backend: nativeBackend{}}
}

func newWithBackend(backend backend) *Vault {
	return &Vault{backend: backend}
}

// Check performs a bounded, non-mutating lookup of a reserved probe account.
func (v *Vault) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := v.getBounded(ctx, probeAccount, probeTimeout)
	if err == nil || errors.Is(err, secret.ErrNotFound) {
		return nil
	}
	return err
}

// Owns reports whether reference is a canonical Kern keyring reference.
func (*Vault) Owns(reference string) bool {
	_, ok := accountFromReference(reference)
	return ok
}

// Put creates an independent native credential and returns only its opaque
// reference. The value is never embedded in the reference or a command line.
func (v *Vault) Put(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if value == "" {
		return "", errors.New("systemvault: credential is empty")
	}
	if len(value) > maxSecretBytes {
		return "", fmt.Errorf("%w: maximum is %d bytes", secret.ErrTooLarge, maxSecretBytes)
	}
	account, err := randomAccount()
	if err != nil {
		return "", err
	}
	if err := v.backend.Set(serviceName, account, value); err != nil {
		return "", mapBackendError("set", err)
	}
	return referencePrefix + account, nil
}

// Resolve reads a credential through a bounded, cancellation-aware lookup.
func (v *Vault) Resolve(ctx context.Context, reference string) (string, error) {
	account, ok := accountFromReference(reference)
	if !ok {
		return "", fmt.Errorf("%w: %q", secret.ErrUnsupported, reference)
	}
	return v.getBounded(ctx, account, resolveTimeout)
}

// Delete removes a credential. Missing owned references are already deleted
// and therefore succeed, making compensation idempotent. Deletion may finish
// in the OS after the caller's deadline, which is still a safe final state.
func (v *Vault) Delete(ctx context.Context, reference string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	account, ok := accountFromReference(reference)
	if !ok {
		return fmt.Errorf("%w: %q", secret.ErrUnsupported, reference)
	}
	results := make(chan error, 1)
	go func() { results <- v.backend.Delete(serviceName, account) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-results:
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return mapBackendError("delete", err)
	}
}

type getResult struct {
	value string
	err   error
}

func (v *Vault) getBounded(ctx context.Context, account string, limit time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	results := make(chan getResult, 1)
	go func() {
		value, err := v.backend.Get(serviceName, account)
		results <- getResult{value: value, err: err}
	}()
	select {
	case <-callCtx.Done():
		return "", callCtx.Err()
	case result := <-results:
		if result.err != nil {
			return "", mapBackendError("get", result.err)
		}
		return result.value, nil
	}
}

func randomAccount() (string, error) {
	var data [accountHexBytes]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("systemvault: generating credential account: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

func accountFromReference(reference string) (string, bool) {
	account, ok := strings.CutPrefix(reference, referencePrefix)
	if !ok || len(account) != hex.EncodedLen(accountHexBytes) {
		return "", false
	}
	decoded, err := hex.DecodeString(account)
	return account, err == nil && len(decoded) == accountHexBytes && account == strings.ToLower(account)
}

func mapBackendError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("%w: native credential", secret.ErrNotFound)
	}
	if errors.Is(err, keyring.ErrSetDataTooBig) {
		return fmt.Errorf("%w: native credential", secret.ErrTooLarge)
	}
	return fmt.Errorf("%w: %s: %v", secret.ErrUnavailable, operation, err)
}

var _ secret.Vault = (*Vault)(nil)
