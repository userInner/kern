// Package secret resolves opaque Secret Store references at the final adapter boundary.
package secret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

var (
	ErrNotFound    = errors.New("secret: not found")
	ErrUnsupported = errors.New("secret: unsupported reference")
	ErrUnavailable = errors.New("secret: credential backend unavailable")
	ErrTooLarge    = errors.New("secret: value is too large")
)

// Environment resolves env:NAME references without persisting their values.
type Environment struct{}

// Ref returns a validated environment Secret Store reference.
func Ref(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !environmentName.MatchString(name) {
		return "", errors.New("secret: environment name is invalid")
	}
	return "env:" + name, nil
}

// Resolve loads a secret at the final use boundary.
func (Environment) Resolve(ctx context.Context, reference string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name, ok := strings.CutPrefix(strings.TrimSpace(reference), "env:")
	if !ok || !environmentName.MatchString(name) {
		return "", fmt.Errorf("%w: %q", ErrUnsupported, reference)
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return value, nil
}

var _ Resolver = Environment{}
