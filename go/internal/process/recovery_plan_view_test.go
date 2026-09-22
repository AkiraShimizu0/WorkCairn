package process

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

const recoveryPlanViewDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func validCompleteRecoveryPlanViewTest() recovery.Plan {
	return recovery.Plan{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Action: recovery.ActionCompleteTask, ExpectedStatus: task.StatusInProgress, ExpectedVersion: 2,
		EvidenceRef: "プロジェクト/ToDoアプリ/Deliverables/TASK-001.md", EvidenceDigest: recoveryPlanViewDigest,
		SourceRevision: recoveryPlanViewDigest, Executable: true, BlockingReasons: []string{}, ApprovalRequired: true,
	}
}

func validFailRecoveryPlanViewTest(reason string) recovery.Plan {
	return recovery.Plan{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Action: recovery.ActionFailAndHold, ExpectedStatus: task.StatusInProgress, ExpectedVersion: 2,
		Reason: reason, SourceRevision: recoveryPlanViewDigest, Executable: true,
		BlockingReasons: []string{}, ApprovalRequired: true,
	}
}

func TestProjectRecoveryPlanViewAcceptsCanonicalPlans(t *testing.T) {
	blockedComplete := validCompleteRecoveryPlanViewTest()
	blockedComplete.ExpectedStatus = task.StatusCompleted
	blockedComplete.Executable = false
	blockedComplete.BlockingReasons = []string{"task_not_in_progress"}

	for name, plan := range map[string]recovery.Plan{
		"executable complete":                  validCompleteRecoveryPlanViewTest(),
		"blocked complete with valid evidence": blockedComplete,
		"executable fail and hold":             validFailRecoveryPlanViewTest("process interrupted"),
	} {
		t.Run(name, func(t *testing.T) {
			view, err := ProjectRecoveryPlanView(plan)
			if err != nil {
				t.Fatalf("ProjectRecoveryPlanView() error = %v", err)
			}
			if view.ProjectName != plan.ProjectName || view.TaskID != plan.TaskID || view.Action != plan.Action ||
				view.TaskStatus != plan.ExpectedStatus || view.TaskVersion != plan.ExpectedVersion || view.Executable != plan.Executable {
				t.Fatalf("ProjectRecoveryPlanView() = %#v", view)
			}
		})
	}
}

func TestProjectRecoveryPlanViewRejectsActionSpecificHiddenFieldViolations(t *testing.T) {
	cases := map[string]recovery.Plan{
		"complete reason": func() recovery.Plan {
			plan := validCompleteRecoveryPlanViewTest()
			plan.Reason = "must stay hidden"
			return plan
		}(),
		"fail evidence reference": func() recovery.Plan {
			plan := validFailRecoveryPlanViewTest("process interrupted")
			plan.EvidenceRef = "Deliverables/TASK-001.md"
			return plan
		}(),
		"fail evidence digest": func() recovery.Plan {
			plan := validFailRecoveryPlanViewTest("process interrupted")
			plan.EvidenceDigest = recoveryPlanViewDigest
			return plan
		}(),
		"fail reason whitespace": validFailRecoveryPlanViewTest(" process interrupted "),
		"fail reason too long":   validFailRecoveryPlanViewTest(strings.Repeat("x", MaxRecoveryPlanPreviewReasonBytes+1)),
	}
	for name, plan := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ProjectRecoveryPlanView(plan); !errors.Is(err, ErrUnsupportedRecoveryPlanPreview) {
				t.Fatalf("error = %v, want ErrUnsupportedRecoveryPlanPreview", err)
			}
		})
	}
}

func TestProjectRecoveryPlanViewEnforcesClosedOrderedBlockingReasons(t *testing.T) {
	tests := []struct {
		name    string
		action  recovery.Action
		reasons []string
		accept  bool
	}{
		{"complete empty", recovery.ActionCompleteTask, []string{}, true},
		{"complete one", recovery.ActionCompleteTask, []string{"task_not_in_progress"}, true},
		{"complete ordered", recovery.ActionCompleteTask, []string{"task_not_in_progress", "matching_deliverable_not_confirmed"}, true},
		{"complete reversed", recovery.ActionCompleteTask, []string{"matching_deliverable_not_confirmed", "task_not_in_progress"}, false},
		{"complete duplicate", recovery.ActionCompleteTask, []string{"task_not_in_progress", "task_not_in_progress"}, false},
		{"complete wrong action", recovery.ActionCompleteTask, []string{"deliverable_present_or_invalid"}, false},
		{"fail one", recovery.ActionFailAndHold, []string{"task_not_in_progress"}, true},
		{"fail ordered", recovery.ActionFailAndHold, []string{"task_not_in_progress", "deliverable_present_or_invalid"}, true},
		{"fail reversed", recovery.ActionFailAndHold, []string{"deliverable_present_or_invalid", "task_not_in_progress"}, false},
		{"fail unknown", recovery.ActionFailAndHold, []string{"unknown"}, false},
		{"failure reason internal only", recovery.ActionFailAndHold, []string{"failure_reason_required"}, false},
		{"nil", recovery.ActionCompleteTask, nil, false},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			var plan recovery.Plan
			if current.action == recovery.ActionCompleteTask {
				plan = validCompleteRecoveryPlanViewTest()
			} else {
				plan = validFailRecoveryPlanViewTest("process interrupted")
			}
			plan.BlockingReasons = current.reasons
			plan.Executable = len(current.reasons) == 0
			_, err := ProjectRecoveryPlanView(plan)
			if current.accept && err != nil {
				t.Fatalf("ProjectRecoveryPlanView() error = %v", err)
			}
			if !current.accept && !errors.Is(err, ErrUnsupportedRecoveryPlanPreview) {
				t.Fatalf("error = %v, want ErrUnsupportedRecoveryPlanPreview", err)
			}
		})
	}
}

func TestProjectRecoveryPlanViewJSONIsClosedAndUsesEmptyArray(t *testing.T) {
	view, err := ProjectRecoveryPlanView(validCompleteRecoveryPlanViewTest())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"schema_version", "project_name", "task_id", "action", "task_status", "task_version", "executable", "blocking_reasons"}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	sortStrings(got)
	sortStrings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v; JSON = %s", got, want, encoded)
	}
	if !strings.Contains(string(encoded), `"blocking_reasons":[]`) {
		t.Fatalf("JSON = %s, want non-null empty blocking_reasons", encoded)
	}
	for _, hidden := range []string{`"evidence_reference":`, `"evidence_digest":`, `"source_revision":`, `"reason":`, `"approval_required":`} {
		if strings.Contains(string(encoded), hidden) {
			t.Fatalf("JSON leaked hidden field %q: %s", hidden, encoded)
		}
	}
}

func TestPlanTaskRecoveryViewRejectsInvalidInputBeforePlanner(t *testing.T) {
	validInput := RecoveryInput{VaultRoot: "/not/read", ProjectName: "ToDoアプリ"}
	validRequest := recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask}
	tests := []struct {
		name    string
		input   RecoveryInput
		request recovery.PlanRequest
	}{
		{"blank project", RecoveryInput{VaultRoot: "/not/read", ProjectName: ""}, validRequest},
		{"trimmed project", RecoveryInput{VaultRoot: "/not/read", ProjectName: " ToDoアプリ "}, validRequest},
		{"project traversal", RecoveryInput{VaultRoot: "/not/read", ProjectName: "../outside"}, validRequest},
		{"project separator", RecoveryInput{VaultRoot: "/not/read", ProjectName: "inside/outside"}, validRequest},
		{"project backslash", RecoveryInput{VaultRoot: "/not/read", ProjectName: `inside\outside`}, validRequest},
		{"project NUL", RecoveryInput{VaultRoot: "/not/read", ProjectName: "inside\x00outside"}, validRequest},
		{"blank task", validInput, recovery.PlanRequest{Action: recovery.ActionCompleteTask}},
		{"leading task whitespace", validInput, recovery.PlanRequest{TaskID: " TASK-001", Action: recovery.ActionCompleteTask}},
		{"task CRLF", validInput, recovery.PlanRequest{TaskID: "TASK-\r\n001", Action: recovery.ActionCompleteTask}},
		{"invalid task", validInput, recovery.PlanRequest{TaskID: "task-1", Action: recovery.ActionCompleteTask}},
		{"none action", validInput, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionNone}},
		{"complete reason", validInput, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask, Reason: "reason"}},
		{"fail missing reason", validInput, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionFailAndHold}},
		{"fail whitespace reason", validInput, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionFailAndHold, Reason: " reason "}},
		{"fail reason too long", validInput, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionFailAndHold, Reason: strings.Repeat("x", MaxRecoveryPlanPreviewReasonBytes+1)}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			calls := 0
			planner := func(context.Context, RecoveryInput, recovery.PlanRequest) (recovery.Plan, error) {
				calls++
				return validCompleteRecoveryPlanViewTest(), nil
			}
			if _, err := planTaskRecoveryView(context.Background(), current.input, current.request, planner); !errors.Is(err, ErrInvalidRecoveryPlanPreviewInput) {
				t.Fatalf("error = %v, want ErrInvalidRecoveryPlanPreviewInput", err)
			}
			if calls != 0 {
				t.Fatalf("planner calls = %d, want 0", calls)
			}
		})
	}
}

func TestPlanTaskRecoveryViewCallsPlannerOnceAndBindsResult(t *testing.T) {
	reason := strings.Repeat("x", MaxRecoveryPlanPreviewReasonBytes)
	request := recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionFailAndHold, Reason: reason}
	calls := 0
	planner := func(_ context.Context, input RecoveryInput, got recovery.PlanRequest) (recovery.Plan, error) {
		calls++
		if input.ProjectName != "ToDoアプリ" || !reflect.DeepEqual(got, request) {
			t.Fatalf("planner input = %#v %#v", input, got)
		}
		return validFailRecoveryPlanViewTest(reason), nil
	}
	if _, err := planTaskRecoveryView(context.Background(), RecoveryInput{VaultRoot: "/not/read", ProjectName: "ToDoアプリ"}, request, planner); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("planner calls = %d, want 1", calls)
	}
}

func TestPlanTaskRecoveryViewRejectsPlannerResultThatDoesNotMatchValidatedInput(t *testing.T) {
	reason := "process interrupted"
	request := recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionFailAndHold, Reason: reason}
	tests := map[string]func(*recovery.Plan){
		"project": func(plan *recovery.Plan) { plan.ProjectName = "別Project" },
		"task":    func(plan *recovery.Plan) { plan.TaskID = "TASK-002" },
		"action":  func(plan *recovery.Plan) { plan.Action = recovery.ActionCompleteTask },
		"reason":  func(plan *recovery.Plan) { plan.Reason = "different reason" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			planner := func(context.Context, RecoveryInput, recovery.PlanRequest) (recovery.Plan, error) {
				plan := validFailRecoveryPlanViewTest(reason)
				mutate(&plan)
				return plan, nil
			}
			_, err := planTaskRecoveryView(context.Background(), RecoveryInput{VaultRoot: "/not/read", ProjectName: "ToDoアプリ"}, request, planner)
			if !errors.Is(err, ErrUnsupportedRecoveryPlanPreview) {
				t.Fatalf("error = %v, want ErrUnsupportedRecoveryPlanPreview", err)
			}
		})
	}
}

func TestProjectRecoveryPlanViewRejectsUnsafeProjectPathSegment(t *testing.T) {
	plan := validCompleteRecoveryPlanViewTest()
	plan.ProjectName = "../outside"
	if _, err := ProjectRecoveryPlanView(plan); !errors.Is(err, ErrUnsupportedRecoveryPlanPreview) {
		t.Fatalf("error = %v, want ErrUnsupportedRecoveryPlanPreview", err)
	}
}

func TestPlanTaskRecoveryViewProjectsRealVaultPlan(t *testing.T) {
	root := writePlanVault(t)
	startRecoveryTask(t, root)
	writeRecoveryDeliverable(t, root)
	view, err := PlanTaskRecoveryView(context.Background(), RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"}, recovery.PlanRequest{
		TaskID: "TASK-001", Action: recovery.ActionCompleteTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.ProjectName != "ToDoアプリ" || view.TaskID != "TASK-001" || view.Action != recovery.ActionCompleteTask || !view.Executable {
		t.Fatalf("PlanTaskRecoveryView() = %#v", view)
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
