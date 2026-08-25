// Package planner creates a deterministic initial plan when a task is complex
// enough to benefit from explicit, user-visible execution steps.
package planner

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/userInner/kern/internal/plan"
	"github.com/userInner/kern/internal/task"
)

// Decision either skips an unnecessary plan or supplies a validated draft.
type Decision struct {
	Explicit bool
	Reason   string
	Draft    plan.Draft
}

// Build classifies a task without consulting a model, so planning itself is
// bounded, reproducible, and available in offline mode.
func Build(item task.Task) Decision {
	goal := strings.TrimSpace(item.Goal)
	if complexity(goal) < 2 {
		return Decision{Reason: "single-step task without an execution or verification chain"}
	}
	kind := classify(goal)
	localizedChinese := containsHan(goal)
	draft := plan.Draft{
		Rationale: rationale(kind, localizedChinese),
		Steps:     steps(kind, localizedChinese),
	}
	return Decision{Explicit: true, Reason: draft.Rationale, Draft: draft}
}

type taskKind string

const (
	kindGeneral  taskKind = "general"
	kindCode     taskKind = "code"
	kindResearch taskKind = "research"
)

func complexity(goal string) int {
	lower := strings.ToLower(goal)
	score := 0
	if utf8.RuneCountInString(goal) >= 120 {
		score++
	}
	if strings.ContainsAny(goal, "\n；;") || containsAny(lower,
		"然后", "并且", "以及", "同时", " first ", " then ", " and ",
	) {
		score++
	}
	if containsAny(lower,
		"修改", "创建", "实现", "开发", "修复", "运行", "部署", "下载", "发布",
		"edit", "create", "implement", "build", "fix", "run", "deploy", "write",
	) {
		score += 2
	}
	if containsAny(lower,
		"验证", "测试", "检查", "对比", "证据", "verify", "test", "check", "compare",
	) {
		score++
	}
	if containsAny(lower, "支付", "转账", "删除", "权限", "publish", "payment", "delete") {
		score += 2
	}
	return score
}

func classify(goal string) taskKind {
	lower := strings.ToLower(goal)
	if containsAny(lower,
		"代码", "仓库", "文件", "编译", "测试", "修复", "开发", "接口",
		"code", "repository", "file", "compile", "test", "bug", "api", "sdk",
	) {
		return kindCode
	}
	if containsAny(lower,
		"研究", "调研", "资料", "来源", "论文", "新闻", "搜索",
		"research", "source", "paper", "news", "search",
	) {
		return kindResearch
	}
	return kindGeneral
}

func rationale(kind taskKind, chinese bool) string {
	if chinese {
		switch kind {
		case kindCode:
			return "该任务会修改或验证工作区，需要依次完成检查、执行和确定性验证。"
		case kindResearch:
			return "该任务需要明确来源边界、收集证据并在交付前完成交叉验证。"
		default:
			return "该任务包含多个动作或风险边界，适合使用可见、可追踪的执行阶段。"
		}
	}
	switch kind {
	case kindCode:
		return "The task changes or validates a workspace and needs inspect, execution, and deterministic verification phases."
	case kindResearch:
		return "The task needs an explicit source boundary, evidence collection, and cross-checking before delivery."
	default:
		return "The task contains multiple actions or risk boundaries and benefits from visible execution phases."
	}
}

func steps(kind taskKind, chinese bool) []plan.StepDraft {
	if chinese {
		switch kind {
		case kindCode:
			return []plan.StepDraft{
				{Title: "检查工作区与约束", Description: "定位相关文件、现有行为和用户授权的修改边界。", Phase: plan.PhasePrepare, Required: true},
				{Title: "实现请求的修改", Description: "完成范围最小但功能完整的修改，并保留可审查证据。", Phase: plan.PhaseExecute, Required: true},
				{Title: "运行确定性检查并交付证据", Description: "使用适用的测试、检查、产物和差异验证结果后再完成任务。", Phase: plan.PhaseVerify, Required: true},
			}
		case kindResearch:
			return []plan.StepDraft{
				{Title: "确定范围与来源标准", Description: "明确时效、权威性以及必须提供证据的结论。", Phase: plan.PhasePrepare, Required: true},
				{Title: "收集并交叉核对证据", Description: "保留来源信息，并将检索内容作为不可信输入处理。", Phase: plan.PhaseExecute, Required: true},
				{Title: "验证结论并形成交付", Description: "处理来源冲突、标明不确定性并生成可引用的结果。", Phase: plan.PhaseVerify, Required: true},
			}
		default:
			return []plan.StepDraft{
				{Title: "确认约束与目标产物", Description: "保留原始目标，并识别执行范围和授权边界。", Phase: plan.PhasePrepare, Required: true},
				{Title: "执行所需工作", Description: "使用受限的 Core 能力生成用户要求的结果。", Phase: plan.PhaseExecute, Required: true},
				{Title: "验证证据并交付", Description: "优先进行确定性验证，并披露仍然存在的不确定性。", Phase: plan.PhaseVerify, Required: true},
			}
		}
	}
	switch kind {
	case kindCode:
		return []plan.StepDraft{
			{Title: "Inspect the workspace and constraints", Description: "Identify relevant files, existing behavior, and the authorized change boundary.", Phase: plan.PhasePrepare, Required: true},
			{Title: "Implement the requested change", Description: "Make the smallest complete workspace changes and retain reviewable evidence.", Phase: plan.PhaseExecute, Required: true},
			{Title: "Run deterministic checks and deliver evidence", Description: "Verify behavior with applicable tests, checks, artifacts, and diffs before completion.", Phase: plan.PhaseVerify, Required: true},
		}
	case kindResearch:
		return []plan.StepDraft{
			{Title: "Set scope and source criteria", Description: "Define recency, authority, and claims that need evidence.", Phase: plan.PhasePrepare, Required: true},
			{Title: "Collect and cross-check evidence", Description: "Gather relevant sources while retaining provenance and treating retrieved text as untrusted.", Phase: plan.PhaseExecute, Required: true},
			{Title: "Verify claims and synthesize the result", Description: "Resolve conflicts, identify uncertainty, and produce a cited delivery.", Phase: plan.PhaseVerify, Required: true},
		}
	default:
		return []plan.StepDraft{
			{Title: "Establish constraints and desired output", Description: "Preserve the original goal and identify execution and authorization boundaries.", Phase: plan.PhasePrepare, Required: true},
			{Title: "Execute the required work", Description: "Use the bounded Core capabilities needed to produce the requested outcome.", Phase: plan.PhaseExecute, Required: true},
			{Title: "Verify evidence and deliver", Description: "Check completion deterministically where possible and disclose remaining uncertainty.", Phase: plan.PhaseVerify, Required: true},
		}
	}
}

func containsHan(text string) bool {
	for _, value := range text {
		if unicode.Is(unicode.Han, value) {
			return true
		}
	}
	return false
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
