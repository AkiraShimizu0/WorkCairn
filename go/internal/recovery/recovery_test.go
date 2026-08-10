package recovery

import (
	"errors"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

func TestRecoveryPlanValidationRequiresApprovalAndExactEvidence(t *testing.T) {
	valid := Plan{
		SchemaVersion: SchemaVersion, ProjectName: "P", TaskID: "TASK-001",
		Action: ActionCompleteTask, ExpectedStatus: task.StatusInProgress, ExpectedVersion: 2,
		EvidenceRef: "Deliverables/TASK-001.md", EvidenceDigest: digest("a"), SourceRevision: digest("b"),
		Executable: true, BlockingReasons: []string{}, ApprovalRequired: true,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Plan){
		func(plan *Plan) { plan.ApprovalRequired = false },
		func(plan *Plan) { plan.EvidenceDigest = "sha256:short" },
		func(plan *Plan) { plan.SourceRevision = "sha256:" + strings.Repeat("z", 64) },
		func(plan *Plan) { plan.BlockingReasons = []string{"changed"} },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("Validate() error = %v for %#v", err, candidate)
		}
	}
}

func TestBlockedRecoveryPlanCanDescribeMissingEvidence(t *testing.T) {
	plan := Plan{
		SchemaVersion: SchemaVersion, ProjectName: "P", TaskID: "TASK-001",
		Action: ActionCompleteTask, ExpectedStatus: task.StatusInProgress, ExpectedVersion: 2,
		SourceRevision: digest("b"), Executable: false,
		BlockingReasons: []string{"matching_deliverable_not_confirmed"}, ApprovalRequired: true,
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
