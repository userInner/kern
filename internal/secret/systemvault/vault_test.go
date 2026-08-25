package systemvault

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	keyring "github.com/zalando/go-keyring"

	"github.com/userInner/kern/internal/secret"
	"github.com/userInner/kern/internal/secret/vaulttest"
)

type memoryBackend struct {
	mu             sync.Mutex
	values         map[string]string
	setErr         error
	getErr         error
	deleteErr      error
	getStarted     chan struct{}
	getReleased    chan struct{}
	deleteStarted  chan struct{}
	deleteReleased chan struct{}
	cancelSet      context.CancelFunc
}

func (b *memoryBackend) Set(service, account, value string) error {
	if b.cancelSet != nil {
		b.cancelSet()
	}
	if b.setErr != nil {
		return b.setErr
	}
	b.mu.Lock()
	b.values[service+":"+account] = value
	b.mu.Unlock()
	return nil
}

func (b *memoryBackend) Get(service, account string) (string, error) {
	if b.getStarted != nil {
		select {
		case b.getStarted <- struct{}{}:
		default:
		}
	}
	if b.getReleased != nil {
		<-b.getReleased
	}
	if b.getErr != nil {
		return "", b.getErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.values[service+":"+account]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (b *memoryBackend) Delete(service, account string) error {
	if b.deleteStarted != nil {
		select {
		case b.deleteStarted <- struct{}{}:
		default:
		}
	}
	if b.deleteReleased != nil {
		<-b.deleteReleased
	}
	if b.deleteErr != nil {
		return b.deleteErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := service + ":" + account
	if _, ok := b.values[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(b.values, key)
	return nil
}

func TestDeleteIsBoundedWhenBackendHangs(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	backend := &memoryBackend{
		values: make(map[string]string), deleteStarted: started, deleteReleased: release,
	}
	vault := newWithBackend(backend)
	reference, err := vault.Put(t.Context(), "delete-timeout")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- vault.Delete(ctx, reference) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backend deletion did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Delete() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Delete() did not honor context deadline")
	}
	close(release)
}

func TestVaultContract(t *testing.T) {
	vaulttest.Run(t, func(*testing.T) secret.Vault {
		return newWithBackend(&memoryBackend{values: make(map[string]string)})
	})
}

func TestNativeVaultContract(t *testing.T) {
	if os.Getenv("KERN_TEST_SYSTEM_VAULT") != "1" {
		t.Skip("set KERN_TEST_SYSTEM_VAULT=1 to exercise the native credential service")
	}
	vaulttest.Run(t, func(*testing.T) secret.Vault { return New() })
}

func TestCheckIsBoundedWhenBackendHangs(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	vault := newWithBackend(&memoryBackend{
		values: make(map[string]string), getStarted: started, getReleased: release,
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- vault.Check(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backend lookup did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Check() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Check() did not honor context deadline")
	}
	close(release)
}

func TestPutReturnsReferenceAfterMidCallCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	backend := &memoryBackend{values: make(map[string]string), cancelSet: cancel}
	vault := newWithBackend(backend)
	reference, err := vault.Put(ctx, "temporary-secret")
	if err != nil || reference == "" {
		t.Fatalf("Put() = %q, %v", reference, err)
	}
	if err := vault.Delete(context.Background(), reference); err != nil {
		t.Fatalf("compensating Delete() error = %v", err)
	}
}

func TestBackendErrorsAreClassified(t *testing.T) {
	tests := []struct {
		name    string
		backend *memoryBackend
		call    func(*Vault) error
		want    error
	}{
		{
			name: "unavailable get", backend: &memoryBackend{values: make(map[string]string), getErr: errors.New("dbus unavailable")},
			call: func(v *Vault) error {
				_, err := v.Resolve(t.Context(), referencePrefix+"0123456789abcdef0123456789abcdef")
				return err
			},
			want: secret.ErrUnavailable,
		},
		{
			name: "oversized backend", backend: &memoryBackend{values: make(map[string]string), setErr: keyring.ErrSetDataTooBig},
			call: func(v *Vault) error { _, err := v.Put(t.Context(), "value"); return err },
			want: secret.ErrTooLarge,
		},
		{
			name: "unavailable delete", backend: &memoryBackend{values: make(map[string]string), deleteErr: errors.New("keychain locked")},
			call: func(v *Vault) error { return v.Delete(t.Context(), referencePrefix+"0123456789abcdef0123456789abcdef") },
			want: secret.ErrUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(newWithBackend(test.backend)); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOwnsRejectsNonCanonicalReference(t *testing.T) {
	vault := New()
	for _, reference := range []string{
		"keyring:kern:v1:0123456789ABCDEF0123456789ABCDEF",
		" keyring:kern:v1:0123456789abcdef0123456789abcdef",
		"keyring:kern:v2:0123456789abcdef0123456789abcdef",
		"keyring:kern:v1:short",
	} {
		if vault.Owns(reference) {
			t.Fatalf("Owns(%q) = true", reference)
		}
	}
}
