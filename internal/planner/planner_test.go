package planner

import (
	"testing"

	"github.com/userInner/kern/internal/plan"
	"github.com/userInner/kern/internal/task"
)

func TestBuildSkipsSimpleSingleStepQuestion(t *testing.T) {
	decision := Build(task.Task{Goal: "What is a mutex?"})
	if decision.Explicit || decision.Reason == "" || len(decision.Draft.Steps) != 0 {
		t.Fatalf("Build(simple) = %#v", decision)
	}
}

func TestBuildCreatesValidatedTaskSpecificPlans(t *testing.T) {
	tests := []struct {
		name      string
		goal      string
		wantFirst string
	}{
		{
			name:      "code",
			goal:      "Inspect this repository, implement the API change, then run tests and verify the diff.",
			wantFirst: "Inspect the workspace and constraints",
		},
		{
			name:      "research",
			goal:      "Research recent papers, cross-check the sources, and verify every claim.",
			wantFirst: "Set scope and source criteria",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Build(task.Task{Goal: tt.goal})
			if !decision.Explicit || decision.Draft.Steps[0].Title != tt.wantFirst {
				t.Fatalf("Build() = %#v", decision)
			}
			if err := plan.ValidateDraft(decision.Draft); err != nil {
				t.Fatalf("ValidateDraft() error = %v", err)
			}
		})
	}
}

func TestBuildUsesTaskLanguageForVisiblePlan(t *testing.T) {
	decision := Build(task.Task{Goal: "检查代码仓库，实现接口修改，然后运行测试并验证差异。"})
	if !decision.Explicit || decision.Draft.Steps[0].Title != "检查工作区与约束" ||
		decision.Draft.Rationale != "该任务会修改或验证工作区，需要依次完成检查、执行和确定性验证。" {
		t.Fatalf("Build(Chinese) = %#v", decision)
	}
}
