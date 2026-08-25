// Package baseline provides a deterministic offline processor for development.
package baseline

import (
	"context"
	"fmt"
	"strings"

	"github.com/userInner/kern/internal/task"
)

// Processor proves the complete runtime path without external credentials.
// It is intentionally labelled as a baseline rather than pretending to be an LLM.
type Processor struct{}

// New constructs an offline baseline processor.
func New() *Processor {
	return &Processor{}
}

// Process returns a deterministic execution receipt for the requested goal.
func (p *Processor) Process(ctx context.Context, item task.Task) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	goal := strings.TrimSpace(item.Goal)
	return fmt.Sprintf(
		"Kern 已完成本地运行时验证。\n\n任务目标：%s\n\n"+
			"本次由 offline-baseline 执行器处理，用于验证任务持久化、事件流与完成校验。"+
			"配置 KERN_MODEL_BASE_URL、KERN_MODEL 和 KERN_MODEL_API_KEY 后，同一任务链路将使用真实模型。",
		goal,
	), nil
}
