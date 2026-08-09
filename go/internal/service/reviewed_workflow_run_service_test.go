package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/revision"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

type reviewedWorkflowFixture struct {
	SchemaVersion   int              `json:"schema_version"`
	ParentCommandID string           `json:"parent_command_id"`
	ReviewerID      string           `json:"reviewer_id"`
	Verdicts        []review.Verdict `json:"verdicts"`
	Expected        struct {
		Status   string   `json:"status"`
		Tasks    []string `json:"task_order"`
		Targeted []bool   `json:"targeted_revision"`
	} `json:"expected"`
}

type reviewedTaskExecutorFake struct {
	tasks    []string
	commands []string
	targeted []bool
	failAt   int
}

func (fake *reviewedTaskExecutorFake) Execute(_ context.Context, taskID, commandID string, targeted bool) (execution.Result, error) {
	fake.tasks = append(fake.tasks, taskID)
	fake.commands = append(fake.commands, commandID)
	fake.targeted = append(fake.targeted, targeted)
	result := execution.Result{TaskID: taskID, Status: execution.StatusCompleted}
	if fake.failAt > 0 && len(fake.tasks) == fake.failAt {
		return result, errors.New("task failed")
	}
	return result, nil
}

type reviewedReviewerFake struct {
	verdicts []review.Verdict
	calls    int
	commands []string
	failAt   int
}

func (fake *reviewedReviewerFake) Execute(_ context.Context, taskID, commandID string) (review.OrchestrationResult, error) {
	fake.commands = append(fake.commands, commandID)
	verdict := fake.verdicts[fake.calls]
	fake.calls++
	issues := []review.Issue{}
	if verdict == review.VerdictRequestChanges {
		issues = []review.Issue{{Category: "requirements", Severity: "medium", Description: "不足", SuggestedAction: "追記"}}
	}
	result := review.OrchestrationResult{
		Status:         "completed",
		Execution:      &review.ExecutionResult{TaskID: taskID, Decision: review.Decision{Verdict: verdict, Issues: issues}},
		Artifact:       &review.Record{TaskID: taskID, CanonicalPath: "Reviews/" + taskID + ".json", ProjectionPath: "Reviews/" + taskID + ".md", CanonicalCommitted: true, ProjectionCommitted: true},
		EventPublished: true,
	}
	if fake.failAt > 0 && fake.calls == fake.failAt {
		return result, errors.New("review partial failure")
	}
	return result, nil
}

type reviewedReviserFake struct {
	tasks    []string
	commands []string
	fail     bool
}

func (fake *reviewedReviserFake) Execute(_ context.Context, sourceTaskID, commandID string) (revision.Result, error) {
	fake.commands = append(fake.commands, commandID)
	revisionID := "TASK-002"
	if sourceTaskID == "TASK-002" {
		revisionID = "TASK-003"
	}
	created := task.Task{ID: revisionID, Status: task.StatusUnstarted, Version: 1}
	result := revision.Result{Intent: &revision.Record{RevisionTaskID: revisionID, Committed: true}, Task: &created, EventPublished: true}
	if fake.fail {
		return result, errors.New("revision partial failure")
	}
	return result, nil
}

func TestReviewedWorkflowRunsRequestChangesRevisionReviewThenContinues(t *testing.T) {
	fixture := loadReviewedWorkflowFixture(t)
	planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{
		{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}},
		{TaskID: "TASK-004", Ready: true, BlockingReasons: []string{}},
		{Completed: true, BlockingReasons: []string{}},
	}}
	executor := &reviewedTaskExecutorFake{}
	reviewer := &reviewedReviewerFake{verdicts: fixture.Verdicts}
	reviser := &reviewedReviserFake{}
	service, err := NewReviewedWorkflowRunService(planner, executor, reviewer, reviser)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), fixture.ParentCommandID, 10)
	if err != nil || result.Status != fixture.Expected.Status || len(result.Tasks) != 3 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	if !reflect.DeepEqual(executor.tasks, fixture.Expected.Tasks) ||
		!reflect.DeepEqual(executor.targeted, fixture.Expected.Targeted) {
		t.Fatalf("execution order = %#v targeted=%#v", executor.tasks, executor.targeted)
	}
	if result.Tasks[0].Verdict != review.VerdictRequestChanges || result.Tasks[0].Revision == nil ||
		result.Tasks[1].Verdict != review.VerdictApprove || result.Tasks[2].Verdict != review.VerdictApprove {
		t.Fatalf("branch results = %#v", result.Tasks)
	}
	allCommands := append(append([]string{}, executor.commands...), reviewer.commands...)
	allCommands = append(allCommands, reviser.commands...)
	seen := map[string]bool{}
	for _, commandID := range allCommands {
		if seen[commandID] {
			t.Fatalf("duplicate child Command ID %s", commandID)
		}
		seen[commandID] = true
	}

	replayExecutor := &reviewedTaskExecutorFake{}
	replayReviewer := &reviewedReviewerFake{verdicts: fixture.Verdicts}
	replayReviser := &reviewedReviserFake{}
	replayService, _ := NewReviewedWorkflowRunService(&workflowRunPlannerFake{steps: planner.steps}, replayExecutor, replayReviewer, replayReviser)
	_, _ = replayService.Run(context.Background(), fixture.ParentCommandID, 10)
	if !reflect.DeepEqual(executor.commands, replayExecutor.commands) || !reflect.DeepEqual(reviewer.commands, replayReviewer.commands) || !reflect.DeepEqual(reviser.commands, replayReviser.commands) {
		t.Fatal("reviewed child Command IDs are not deterministic")
	}
}

func loadReviewedWorkflowFixture(t *testing.T) reviewedWorkflowFixture {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "workflow", "reviewed_branch_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture reviewedWorkflowFixture
	if err := json.Unmarshal(content, &fixture); err != nil || fixture.SchemaVersion != 1 || fixture.ParentCommandID == "" || fixture.ReviewerID == "" {
		t.Fatalf("invalid reviewed Workflow fixture: %#v, %v", fixture, err)
	}
	return fixture
}

func TestReviewedWorkflowStopsOnReviewPartialFailureAndPreservesResult(t *testing.T) {
	planner := &workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}}
	service, _ := NewReviewedWorkflowRunService(
		planner,
		&reviewedTaskExecutorFake{},
		&reviewedReviewerFake{verdicts: []review.Verdict{review.VerdictApprove}, failAt: 1},
		&reviewedReviserFake{},
	)
	result, err := service.Run(context.Background(), "CMD-REVIEWED-WORKFLOW-001", 10)
	var typed *ReviewedWorkflowRunError
	if !errors.As(err, &typed) || typed.Stage != "review" || result.Status != "partial_failure" ||
		len(result.Tasks) != 1 || result.Tasks[0].Review == nil || result.Tasks[0].Review.Artifact == nil ||
		!result.Tasks[0].Review.Artifact.CanonicalCommitted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestReviewedWorkflowLimitKeepsCommittedRevisionAsExplicitNext(t *testing.T) {
	service, _ := NewReviewedWorkflowRunService(
		&workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}},
		&reviewedTaskExecutorFake{},
		&reviewedReviewerFake{verdicts: []review.Verdict{review.VerdictRequestChanges}},
		&reviewedReviserFake{},
	)
	result, err := service.Run(context.Background(), "CMD-REVIEWED-WORKFLOW-001", 1)
	if err != nil || result.Status != "limit_reached" || result.Next == nil || result.Next.Action != "execute_revision_task" ||
		result.Next.TaskID != "TASK-002" || result.Tasks[0].Revision == nil || result.Tasks[0].Revision.Task == nil {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}
