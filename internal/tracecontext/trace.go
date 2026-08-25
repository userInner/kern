// Package tracecontext provides Kern's dependency-free local trace identity.
package tracecontext

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

type contextKey struct{}

// Identity is the W3C-compatible identity attached to one local span.
type Identity struct {
	TraceID string
	SpanID  string
}

// EventSpan maps durable event families to bounded local spans. Related model,
// operation, plugin, and verification events share a stable span under the
// Attempt root; other events receive a deterministic child span.
func EventSpan(attemptID, eventType string, payload json.RawMessage) (spanID, parentSpanID string) {
	root := AttemptSpanID(attemptID)
	if strings.HasPrefix(eventType, "task.") || strings.HasPrefix(eventType, "agent.phase") {
		return root, ""
	}
	identity := eventType
	scope := "event"
	switch {
	case strings.HasPrefix(eventType, "model."):
		scope = "model-call"
		identity = payloadString(payload, "call_id")
	case strings.HasPrefix(eventType, "operation."):
		scope = "operation"
		identity = payloadString(payload, "operation_id")
	case strings.HasPrefix(eventType, "plugin."):
		scope = "plugin"
		identity = "activation"
	case strings.HasPrefix(eventType, "verification."):
		scope = "verification"
		identity = "suite"
	}
	if identity == "" {
		identity = eventType
	}
	return StableSpanID(scope, attemptID+":"+identity), root
}

// AttemptTraceID deterministically identifies one durable Attempt. It does not
// reveal the Attempt ID and remains stable across process restarts.
func AttemptTraceID(attemptID string) string {
	return StableTraceID("attempt", attemptID)
}

// StableTraceID derives an opaque restart-stable trace ID for durable local
// identities such as an Eval Case. Scope must be a Core-owned constant.
func StableTraceID(scope, identity string) string {
	digest := sha256.Sum256([]byte("kern:" + strings.TrimSpace(scope) + ":" + strings.TrimSpace(identity)))
	return hex.EncodeToString(digest[:16])
}

// StableSpanID derives a stable local span ID under the same constraints.
func StableSpanID(scope, identity string) string {
	digest := sha256.Sum256([]byte("kern:span:" + strings.TrimSpace(scope) + ":" + strings.TrimSpace(identity)))
	return hex.EncodeToString(digest[:8])
}

// AttemptSpanID identifies the root span of one durable Attempt.
func AttemptSpanID(attemptID string) string {
	digest := sha256.Sum256([]byte("kern:attempt-span:" + strings.TrimSpace(attemptID)))
	return hex.EncodeToString(digest[:8])
}

// NewRequest creates a child HTTP span. A valid W3C traceparent keeps its
// trace ID; malformed or absent input starts a new trace.
func NewRequest(traceparent string) Identity {
	traceID := traceIDFromParent(traceparent)
	if traceID == "" {
		traceID = randomHex(16)
	}
	return Identity{TraceID: traceID, SpanID: randomHex(8)}
}

// Traceparent returns the W3C header value for an identity.
func (i Identity) Traceparent() string {
	if !validHex(i.TraceID, 32) || !validHex(i.SpanID, 16) {
		return ""
	}
	return "00-" + i.TraceID + "-" + i.SpanID + "-01"
}

// With stores an identity in ctx.
func With(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, identity)
}

// From returns the current trace identity when present.
func From(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok && validHex(identity.TraceID, 32) && validHex(identity.SpanID, 16)
}

func traceIDFromParent(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) != 4 || parts[0] != "00" || !validHex(parts[1], 32) ||
		!validHex(parts[2], 16) || len(parts[3]) != 2 {
		return ""
	}
	return strings.ToLower(parts[1])
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	digest := sha256.Sum256(buffer)
	return hex.EncodeToString(digest[:bytes])
}

func validHex(value string, size int) bool {
	if len(value) != size || strings.Trim(value, "0") == "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func payloadString(payload json.RawMessage, key string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return strings.TrimSpace(value)
}
