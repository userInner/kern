// Package executionphase defines the Core-owned stages of one model attempt.
package executionphase

// Phase is the current model execution stage. Plugins may attach advisory
// resources to a phase, but they cannot create phases or control transitions.
type Phase string

const (
	Unknown Phase = ""
	Prepare Phase = "prepare"
	Execute Phase = "execute"
	Verify  Phase = "verify"
)

// All returns the stable execution order.
func All() []Phase {
	return []Phase{Prepare, Execute, Verify}
}

// Valid reports whether phase is Core-owned and model-visible.
func (p Phase) Valid() bool {
	return p == Prepare || p == Execute || p == Verify
}
