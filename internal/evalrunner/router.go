package evalrunner

import (
	"context"
	"errors"
	"fmt"

	"github.com/userInner/kern/internal/evaluation"
)

// Router selects the explicitly recorded agent adapter for each variant.
type Router struct {
	Kern  evaluation.Agent
	Codex evaluation.Agent
}

// Run delegates without changing the fixed evaluation request.
func (r Router) Run(ctx context.Context, request evaluation.AgentRequest) (evaluation.AgentResult, error) {
	switch request.Variant.Agent {
	case "", "kern":
		if r.Kern == nil {
			return evaluation.AgentResult{}, errors.New("evalrunner: Kern agent is unavailable")
		}
		return r.Kern.Run(ctx, request)
	case "codex":
		if r.Codex == nil {
			return evaluation.AgentResult{}, errors.New("evalrunner: Codex baseline is unavailable")
		}
		return r.Codex.Run(ctx, request)
	default:
		return evaluation.AgentResult{}, fmt.Errorf("evalrunner: unsupported agent %q", request.Variant.Agent)
	}
}

var _ evaluation.Agent = Router{}
