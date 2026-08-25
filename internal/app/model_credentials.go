package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/userInner/kern/internal/modelconfig"
)

const (
	// MaxModelCredentialBytes is the portable raw-key ceiling shared by
	// Keychain, Credential Manager and Secret Service backends.
	MaxModelCredentialBytes     = 2 << 10
	modelCredentialCleanupLimit = 5 * time.Second
)

// ErrCredentialStoreUnavailable indicates that this Core instance has no
// writable system credential backend. Environment references remain usable.
var ErrCredentialStoreUnavailable = errors.New("app: system credential store unavailable")

// CredentialStoreAvailable reports whether this runtime can accept write-only
// raw credentials in addition to environment references.
func (r *Runtime) CredentialStoreAvailable() bool {
	return r != nil && r.vaultReady
}

// GetModelConfig returns one public-safe model configuration. SecretRef is
// available only to trusted in-process code and remains excluded from JSON.
func (r *Runtime) GetModelConfig(ctx context.Context, configID string) (modelconfig.Config, error) {
	return r.Store.GetModelConfig(ctx, configID)
}

// ListModelConfigs returns all public-safe model configurations.
func (r *Runtime) ListModelConfigs(ctx context.Context) ([]modelconfig.Config, error) {
	return r.Store.ListModelConfigs(ctx)
}

// CreateModelConfig persists a configuration that already contains only an
// opaque credential reference, such as env:NAME.
func (r *Runtime) CreateModelConfig(
	ctx context.Context,
	draft modelconfig.Draft,
) (modelconfig.Config, error) {
	return r.Store.CreateModelConfig(ctx, draft)
}

// CreateModelConfigWithAPIKey writes a raw key to the system credential store
// before persisting only its opaque reference. A failed database write is
// compensated by deleting the newly created credential.
func (r *Runtime) CreateModelConfigWithAPIKey(
	ctx context.Context,
	draft modelconfig.Draft,
	apiKey string,
) (modelconfig.Config, error) {
	reference, err := r.putModelCredential(ctx, apiKey)
	if err != nil {
		return modelconfig.Config{}, err
	}
	draft.SecretRef = reference
	created, err := r.Store.CreateModelConfig(ctx, draft)
	if err == nil {
		return created, nil
	}
	if cleanupErr := r.deleteModelCredential(ctx, reference); cleanupErr != nil {
		err = errors.Join(err, fmt.Errorf("removing unreferenced model credential: %w", cleanupErr))
	}
	return modelconfig.Config{}, err
}

// UpdateModelConfig replaces mutable settings while retaining the existing
// credential reference unless the draft explicitly changes or clears it.
func (r *Runtime) UpdateModelConfig(
	ctx context.Context,
	configID string,
	draft modelconfig.Draft,
) (modelconfig.Config, error) {
	current, err := r.Store.GetModelConfig(ctx, configID)
	if err != nil {
		return modelconfig.Config{}, err
	}
	updated, err := r.Store.UpdateModelConfig(ctx, configID, draft)
	if err != nil {
		return modelconfig.Config{}, err
	}
	r.cleanupReplacedCredential(ctx, current.SecretRef, updated.SecretRef)
	return updated, nil
}

// UpdateModelConfigWithAPIKey stores a replacement raw key and atomically
// switches SQLite to the new opaque reference. On database failure, the new
// credential is removed and the prior reference remains authoritative.
func (r *Runtime) UpdateModelConfigWithAPIKey(
	ctx context.Context,
	configID string,
	draft modelconfig.Draft,
	apiKey string,
) (modelconfig.Config, error) {
	current, err := r.Store.GetModelConfig(ctx, configID)
	if err != nil {
		return modelconfig.Config{}, err
	}
	reference, err := r.putModelCredential(ctx, apiKey)
	if err != nil {
		return modelconfig.Config{}, err
	}
	draft.SecretRef = reference
	updated, err := r.Store.UpdateModelConfig(ctx, configID, draft)
	if err != nil {
		if cleanupErr := r.deleteModelCredential(ctx, reference); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("removing unreferenced model credential: %w", cleanupErr))
		}
		return modelconfig.Config{}, err
	}
	r.cleanupReplacedCredential(ctx, current.SecretRef, reference)
	return updated, nil
}

// DeleteModelConfig removes the durable configuration, then best-effort
// removes a credential owned by this Core's system vault. An unavailable OS
// credential service cannot resurrect the deleted SQLite row.
func (r *Runtime) DeleteModelConfig(ctx context.Context, configID string) error {
	current, err := r.Store.GetModelConfig(ctx, configID)
	if err != nil {
		return err
	}
	if err := r.Store.DeleteModelConfig(ctx, configID); err != nil {
		return err
	}
	r.cleanupReplacedCredential(ctx, current.SecretRef, "")
	return nil
}

func (r *Runtime) putModelCredential(ctx context.Context, apiKey string) (string, error) {
	if r.vault == nil || !r.vaultReady {
		return "", ErrCredentialStoreUnavailable
	}
	if apiKey == "" || len(apiKey) > MaxModelCredentialBytes || strings.IndexByte(apiKey, 0) >= 0 {
		return "", errors.New("app: API key must contain 1 to 2048 bytes without NUL")
	}
	reference, err := r.vault.Put(ctx, apiKey)
	apiKey = ""
	if err != nil {
		return "", fmt.Errorf("storing model credential: %w", err)
	}
	reference = strings.TrimSpace(reference)
	if reference == "" || len(reference) > 512 || !r.vault.Owns(reference) {
		if reference != "" && r.vault.Owns(reference) {
			if cleanupErr := r.deleteModelCredential(ctx, reference); cleanupErr != nil {
				return "", errors.Join(
					errors.New("app: credential vault returned an invalid reference"),
					fmt.Errorf("removing invalid credential reference: %w", cleanupErr),
				)
			}
		}
		return "", errors.New("app: credential vault returned an invalid reference")
	}
	return reference, nil
}

func (r *Runtime) cleanupReplacedCredential(ctx context.Context, previous, current string) {
	if r.vault == nil || previous == "" || previous == current || !r.vault.Owns(previous) {
		return
	}
	if err := r.deleteModelCredential(ctx, previous); err != nil && r.logger != nil {
		r.logger.WarnContext(context.WithoutCancel(ctx), "system credential cleanup failed", "error", err)
	}
}

func (r *Runtime) deleteModelCredential(ctx context.Context, reference string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), modelCredentialCleanupLimit)
	defer cancel()
	return r.vault.Delete(cleanupCtx, reference)
}
