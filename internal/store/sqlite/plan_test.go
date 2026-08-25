package sqlite

import (
	"encoding/json"
	"testing"

	"github.com/userInner/kern/internal/plan"
)

func TestStorePersistsAndTransitionsPlanSteps(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	draft := testPlanDraft()
	created, err := store.CreatePlan(t.Context(), createdTask.ID, draft)
	if err != nil {
		t.Fatalf("CreatePlan() error = %v", err)
	}
	if created.Revision != 1 || created.Status != plan.StatusActive || len(created.Steps) != 3 {
		t.Fatalf("CreatePlan() = %#v", created)
	}
	loaded, err := store.CurrentPlan(t.Context(), createdTask.ID)
	if err != nil || loaded.ID != created.ID || len(loaded.Steps) != 3 {
		t.Fatalf("CurrentPlan() = %#v, %v", loaded, err)
	}

	for _, transition := range []struct {
		step    int
		status  plan.StepStatus
		failure string
	}{
		{step: 0, status: plan.StepStatusRunning},
		{step: 0, status: plan.StepStatusCompleted},
		{step: 1, status: plan.StepStatusRunning},
		{step: 1, status: plan.StepStatusFailed, failure: "test failed"},
		{step: 1, status: plan.StepStatusPending},
		{step: 1, status: plan.StepStatusRunning},
		{step: 1, status: plan.StepStatusCompleted},
		{step: 2, status: plan.StepStatusRunning},
		{step: 2, status: plan.StepStatusCompleted},
	} {
		loaded, err = store.TransitionPlanStep(
			t.Context(),
			created.Steps[transition.step].ID,
			transition.status,
			transition.failure,
		)
		if err != nil {
			t.Fatalf("TransitionPlanStep(%d, %s) error = %v", transition.step, transition.status, err)
		}
	}
	if loaded.Status != plan.StatusCompleted || loaded.Steps[1].RetryCount != 1 ||
		loaded.Steps[1].Failure != "" {
		t.Fatalf("completed plan = %#v", loaded)
	}
	for _, step := range loaded.Steps {
		if step.Status != plan.StepStatusCompleted || step.StartedAt == nil || step.CompletedAt == nil {
			t.Fatalf("completed step = %#v", step)
		}
	}
	events, err := store.EventsAfter(t.Context(), createdTask.ID, 0, 100)
	if err != nil {
		t.Fatalf("EventsAfter() error = %v", err)
	}
	var planEvents int
	var stepEvents int
	for _, event := range events {
		switch event.Type {
		case "task.plan_updated":
			planEvents++
			var payload plan.Plan
			if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.ID != created.ID {
				t.Fatalf("plan event payload = %s, %v", event.Payload, err)
			}
		case "task.step_updated":
			stepEvents++
		}
	}
	if planEvents != 1 || stepEvents != 9 {
		t.Fatalf("plan events = %d, step events = %d", planEvents, stepEvents)
	}
}

func TestCreatePlanSupersedesActiveRevision(t *testing.T) {
	store := openTestStore(t)
	createdTask := createTestTask(t, store)
	first, err := store.CreatePlan(t.Context(), createdTask.ID, testPlanDraft())
	if err != nil {
		t.Fatalf("CreatePlan(first) error = %v", err)
	}
	second, err := store.CreatePlan(t.Context(), createdTask.ID, testPlanDraft())
	if err != nil {
		t.Fatalf("CreatePlan(second) error = %v", err)
	}
	if second.Revision != 2 || second.ID == first.ID {
		t.Fatalf("second plan = %#v", second)
	}
	var firstStatus plan.Status
	if err := store.db.QueryRowContext(
		t.Context(),
		"SELECT status FROM plans WHERE id = ?",
		first.ID,
	).Scan(&firstStatus); err != nil {
		t.Fatalf("reading first plan status: %v", err)
	}
	if firstStatus != plan.StatusSuperseded {
		t.Fatalf("first plan status = %q", firstStatus)
	}
}

func testPlanDraft() plan.Draft {
	return plan.Draft{
		Rationale: "multi-phase task",
		Steps: []plan.StepDraft{
			{Title: "Prepare", Phase: plan.PhasePrepare, Required: true},
			{Title: "Execute", Phase: plan.PhaseExecute, Required: true},
			{Title: "Verify", Phase: plan.PhaseVerify, Required: true},
		},
	}
}
