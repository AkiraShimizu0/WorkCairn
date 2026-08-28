package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// recoveryScenarioMockServer answers Task execution and Review Provider
// calls exactly like parallelApproveAndExecuteMockServer, except it grows a
// watched Task-lineage set starting from watchTaskID: any Review request
// whose own Task ID is already watched, or whose "元タスクタイトル" embeds an
// already-watched ID, is deemed part of that lineage, gets Request Changes
// (a distinct finding each time, see distinctRequestChangesOutput), and
// adds its own Task ID to the watched set before returning -- because
// PlanRevision's own Title ("<SourceTaskID>のレビュー指摘を反映する") only
// ever embeds the *immediately preceding* Task ID in a chain, not the
// lineage's original root, so a single static substring match is not
// enough to follow a multi-Revision chain. Everything else Approves.
//
// Once setForceApprove(true) is called (used once the test moves from the
// initial dispatch into its explicit Recovery Command), every Review
// Approves unconditionally -- Recovery's own new Revision Task is a
// deliberately distinct lineage anchored at the stalled Task's own ID, so
// without this toggle it would itself join the watched set and the
// scenario could never demonstrate a successful recovery.
// Task execution responses for the watched lineage deliberately vary their
// content on every attempt (see the "default" case below) -- Progress
// Intelligence v1's Deliverable Progress signal (policy.ProgressPolicy /
// CompoundProgressPolicy, wired into production ExecuteReviewedWorkflow)
// would otherwise see the *same* Deliverable body every attempt and, once
// combined with the repeating structural Review finding this lineage also
// produces, escalate as NO_PROGRESS_DETECTED at the same attempt count a
// fixed/unchanging Deliverable body would reach -- one attempt before this
// test's own Revision Guard proof gets to run. This scenario is
// specifically exercising the Revision Guard's hard count
// (ErrRevisionLimitReached); No-Progress has its own dedicated coverage in
// internal/service.
// varyDeliverableContent controls whether the watched lineage's Task
// execution responses change on every attempt (true, the Revision Guard
// scenario -- see the doc comment above) or stay byte-for-byte identical
// (false, deliberately triggering Progress Intelligence v1's Deliverable
// Progress signal so a caller can exercise NO_PROGRESS_DETECTED instead).
func recoveryScenarioMockServer(t *testing.T, watchTaskID string, varyDeliverableContent bool) (server *httptest.Server, callCount func() int, setForceApprove func(bool)) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	requestChangesCount := 0
	executionCount := 0
	watched := map[string]bool{watchTaskID: true}
	forceApprove := false
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded struct {
			OutputConfig json.RawMessage `json:"output_config"`
			Messages     []struct {
				Content string `json:"content"`
			} `json:"messages"`
			System string `json:"system"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("decode Provider request: %v", err)
		}
		structured := len(decoded.OutputConfig) > 0
		combined := decoded.System
		for _, message := range decoded.Messages {
			combined += message.Content
		}
		var output string
		switch {
		case structured:
			ownTaskID, titleLine := taskIdentityFromPrompt(combined, "元タスクID: ", "元タスクタイトル: ")
			mu.Lock()
			partOfWatchedLineage := !forceApprove && (watched[ownTaskID] || watchedTitleReferencesLineage(titleLine, watched))
			if partOfWatchedLineage {
				watched[ownTaskID] = true
				requestChangesCount++
			}
			attempt := requestChangesCount
			mu.Unlock()
			if partOfWatchedLineage {
				output = distinctRequestChangesOutput(attempt)
			} else {
				output = reviewProviderOutput(review.VerdictApprove)
			}
		default:
			ownTaskID, titleLine := taskIdentityFromPrompt(combined, "タスクID: ", "タイトル: ")
			mu.Lock()
			approvalForced := forceApprove
			partOfWatchedLineage := !approvalForced && (watched[ownTaskID] || watchedTitleReferencesLineage(titleLine, watched))
			if partOfWatchedLineage {
				executionCount++
			}
			attempt := executionCount
			mu.Unlock()
			switch {
			case strings.Contains(combined, "タイトル: 販売戦略へ統合する"):
				if !strings.Contains(combined, "回復済みの競合B") {
					t.Errorf("Synthesis prompt did not use the terminal recovery Revision Deliverable")
				}
				if strings.Contains(combined, "本文（実行") || strings.Contains(combined, "本文（内容は変化しません）") {
					t.Errorf("Synthesis prompt retained stale pre-recovery branch evidence")
				}
				output = "# synthesis\n\n回復済みの競合Bを含む統合結果"
			case approvalForced && strings.Contains(combined, "## CEOからの追加指示"):
				output = "# deliverable\n\n回復済みの競合B"
			case partOfWatchedLineage && varyDeliverableContent:
				output = fmt.Sprintf("# deliverable\n\n本文（実行%d回目の内容です）", attempt)
			case partOfWatchedLineage:
				output = "# deliverable\n\n本文（内容は変化しません）"
			default:
				output = "# deliverable\n\n本文"
			}
		}
		mu.Lock()
		calls++
		mu.Unlock()
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
	callCount = func() int { mu.Lock(); defer mu.Unlock(); return calls }
	setForceApprove = func(value bool) { mu.Lock(); forceApprove = value; mu.Unlock() }
	return server, callCount, setForceApprove
}

func TestInteractionBudgetContinuationResumesCreatedRevisionThenSynthesisWithoutReExecutingCompletedBranches(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BUDGET-CONTINUATION-001", "Budget継続アプリ"
	_, digest := writeApprovalRequiredSessionWithPlan(t, root, sessionID, writeApproveAndExecuteParallelPlan(projectName), at)

	applied, err := ExecuteInteractionPlanApply(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-BUDGET-CONTINUATION-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-BUDGET-APPLY-001",
	}, true)
	if err != nil || !applied.SessionCommitted || applied.Session.State != interaction.StateReadyToExecute {
		t.Fatalf("ExecuteInteractionPlanApply() = %#v, %v", applied, err)
	}

	// Six shared Provider calls let all three initial branches execute and
	// Review. A/C Approve; B (TASK-003) commits its Revision Task, then the
	// shared Budget stops immediately before that Revision's first call.
	server, callCount, setForceApprove := recoveryScenarioMockServer(t, "TASK-003", true)
	defer server.Close()
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	basePlan, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: applied.Session.Version,
		CurrentTime: at.Add(3 * time.Minute), MaxTasks: defaultWorkflowMaxTasks,
	})
	if err != nil {
		t.Fatal(err)
	}
	budgetContract := basePlan.Autonomy.Clone()
	budgetContract.MaxProviderCalls = 6
	if err := budgetContract.Validate(); err != nil {
		t.Fatal(err)
	}
	budgetPlan, err := PlanInteractionWorkflow(context.Background(), InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: applied.Session.Version,
		CurrentTime: at.Add(3 * time.Minute), MaxTasks: defaultWorkflowMaxTasks, Autonomy: &budgetContract,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflowInput := ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: InteractionWorkflowPlanInput{
			VaultRoot: root, SessionID: sessionID, ExpectedVersion: applied.Session.Version,
			ReviewerID: budgetPlan.ReviewerID, CurrentTime: at.Add(3 * time.Minute), MaxTasks: defaultWorkflowMaxTasks,
			Autonomy: &budgetContract,
		},
		WorkflowPlanDigest: budgetPlan.WorkflowPlanDigest, ApprovalReference: "budget-test-approval",
		CommandID: "CMD-BUDGET-WORKFLOW-001",
	}
	stopped, stopErr := ExecuteInteractionWorkflow(context.Background(), workflowInput, provider, server.Client(), true)
	var recorded *RecordedCommandError
	if !errors.As(stopErr, &recorded) || recorded.Code != "BUDGET_EXCEEDED" || recorded.Envelope == nil || recorded.Envelope.Category != "provider_call" {
		t.Fatalf("ExecuteInteractionWorkflow() error = %#v", stopErr)
	}
	if !stopped.SessionCommitted || stopped.Session.State != interaction.StateWorkflowAttentionRequired || callCount() != 6 {
		t.Fatalf("stopped = %#v calls=%d", stopped, callCount())
	}
	byTask := make(map[string]review.Verdict)
	pendingRevisionTaskID := ""
	for _, current := range stopped.Workflow.Tasks {
		byTask[current.TaskID] = current.Verdict
		if current.TaskID == "TASK-003" && current.Revision != nil && current.Revision.Task != nil {
			pendingRevisionTaskID = current.Revision.Task.ID
		}
	}
	if byTask["TASK-001"] != review.VerdictApprove || byTask["TASK-002"] != review.VerdictApprove ||
		byTask["TASK-003"] != review.VerdictRequestChanges || pendingRevisionTaskID == "" {
		t.Fatalf("Budget stop tasks = %#v", stopped.Workflow.Tasks)
	}
	if _, synthesisRan := byTask["TASK-004"]; synthesisRan {
		t.Fatal("Synthesis ran before Budget recovery")
	}
	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		t.Fatal(err)
	}
	completedVersions := make(map[string]uint64, 3)
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003"} {
		stored, getErr := taskStore.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("Budget stop source Task %s = %#v, %v", taskID, stored, getErr)
		}
		completedVersions[taskID] = stored.Version
		if _, statErr := os.Stat(filepath.Join(root, "プロジェクト", projectName, "Deliverables", taskID+".md")); statErr != nil {
			t.Fatalf("Budget stop Deliverable %s: %v", taskID, statErr)
		}
	}
	pendingRevision, err := taskStore.Get(context.Background(), pendingRevisionTaskID)
	if err != nil || pendingRevision.Status != task.StatusUnstarted {
		t.Fatalf("pending Revision Task = %#v, %v", pendingRevision, err)
	}
	for _, relative := range []string{"Reviews/TASK-003.review.json", "Revisions/" + pendingRevisionTaskID + ".revision.md"} {
		if _, statErr := os.Stat(filepath.Join(root, "プロジェクト", projectName, filepath.FromSlash(relative))); statErr != nil {
			t.Fatalf("Budget stop canonical evidence %s: %v", relative, statErr)
		}
	}

	next, err := stopped.Session.Next()
	if err != nil || next.Operation != "interaction.workflow.recover_revision" ||
		!reflect.DeepEqual(next.EligibleTaskIDs, []string{pendingRevisionTaskID}) || next.EvidenceTaskID != "TASK-003" {
		t.Fatalf("Next() = %#v, %v", next, err)
	}
	// Replaying the exhausted original Workflow Command is evidence lookup,
	// not a new Budget scope or retry: it consumes no Provider calls and
	// returns the same durable stop.
	callsAtStop := callCount()
	_, originalReplayErr := ExecuteInteractionWorkflow(context.Background(), workflowInput, provider, server.Client(), true)
	if !errors.As(originalReplayErr, &recorded) || recorded.Code != "BUDGET_EXCEEDED" || callCount() != callsAtStop {
		t.Fatalf("original Workflow replay error = %#v calls=%d, want durable Budget stop and %d calls", originalReplayErr, callCount(), callsAtStop)
	}

	setForceApprove(true)
	callsBeforeRecovery := callCount()
	var taskEvents []event.Event
	var eventsMu sync.Mutex
	observer := event.Observer{
		Types: []event.Type{event.TaskStarted, event.TaskCompleted},
		Handler: func(_ context.Context, published event.Event) error {
			eventsMu.Lock()
			taskEvents = append(taskEvents, published)
			eventsMu.Unlock()
			return nil
		},
	}
	recoveryInput := InteractionRecoverRevisionInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: stopped.Session.Version, TaskID: pendingRevisionTaskID,
		AdditionalGuidance: "既存の成果を保ち、指摘箇所だけ直してください", CurrentTime: at.Add(10 * time.Minute),
		CommandID: "CMD-BUDGET-CONTINUATION-RECOVERY-001", EventObservers: []event.Observer{observer},
	}
	competingInput := recoveryInput
	competingInput.CommandID = "CMD-BUDGET-CONTINUATION-RECOVERY-002"
	start := make(chan struct{})
	type recoveryOutcome struct {
		input  InteractionRecoverRevisionInput
		result InteractionRecoverRevisionResult
		err    error
	}
	outcomes := make(chan recoveryOutcome, 2)
	for _, candidate := range []InteractionRecoverRevisionInput{recoveryInput, competingInput} {
		go func(candidate InteractionRecoverRevisionInput) {
			<-start
			result, runErr := ExecuteInteractionRecoverRevision(context.Background(), candidate, provider, server.Client(), true)
			outcomes <- recoveryOutcome{input: candidate, result: result, err: runErr}
		}(candidate)
	}
	close(start)
	first, second := <-outcomes, <-outcomes
	var winner recoveryOutcome
	successes := 0
	for _, outcome := range []recoveryOutcome{first, second} {
		if outcome.err == nil {
			successes++
			winner = outcome
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent Recovery outcomes = (%#v, %v), (%#v, %v); want exactly one success", first.result, first.err, second.result, second.err)
	}
	recovered, recoverErr := winner.result, winner.err
	if recoverErr != nil || recovered.Workflow.Status != "completed" || recovered.Session.State != interaction.StateCompleted ||
		recovered.ContinuationTaskID != pendingRevisionTaskID || recovered.Revision.Task != nil {
		t.Fatalf("ExecuteInteractionRecoverRevision() = %#v, %v", recovered, recoverErr)
	}
	gotOrder := make([]string, 0, len(recovered.Workflow.Tasks))
	for _, current := range recovered.Workflow.Tasks {
		gotOrder = append(gotOrder, current.TaskID)
		if current.TaskID == "TASK-001" || current.TaskID == "TASK-002" || current.TaskID == "TASK-003" {
			t.Fatalf("Recovery re-executed completed source Task %s", current.TaskID)
		}
	}
	if !reflect.DeepEqual(gotOrder, []string{pendingRevisionTaskID, "TASK-004"}) || !recovered.Workflow.Tasks[0].Targeted {
		t.Fatalf("Recovery order = %#v tasks=%#v", gotOrder, recovered.Workflow.Tasks)
	}
	if callCount()-callsBeforeRecovery != 4 {
		t.Fatalf("Recovery Provider calls = %d, want Revision execute/review + Synthesis execute/review", callCount()-callsBeforeRecovery)
	}
	for taskID, version := range completedVersions {
		stored, getErr := taskStore.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted || stored.Version != version {
			t.Fatalf("completed branch %s changed during Recovery: %#v, %v (version want %d)", taskID, stored, getErr, version)
		}
	}
	for _, taskID := range []string{pendingRevisionTaskID, "TASK-004"} {
		stored, getErr := taskStore.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("Recovery Task %s = %#v, %v", taskID, stored, getErr)
		}
	}
	eventsMu.Lock()
	capturedEvents := append([]event.Event(nil), taskEvents...)
	eventsMu.Unlock()
	if len(capturedEvents) != 4 {
		t.Fatalf("Recovery Task events = %#v, want Started/Completed for Revision and Synthesis", capturedEvents)
	}
	for _, current := range capturedEvents {
		if current.CorrelationID != workflowInput.CommandID || current.CausationID == "" ||
			current.AggregateID == "TASK-001" || current.AggregateID == "TASK-002" || current.AggregateID == "TASK-003" {
			t.Fatalf("Recovery event lineage = %#v", current)
		}
	}
	recoveryTurn := recovered.Session.Turns[len(recovered.Session.Turns)-2]
	if recoveryTurn.Kind != interaction.TurnRevisionRecoveryStarted || recoveryTurn.RecoveryTaskID != pendingRevisionTaskID ||
		recoveryTurn.RecoveryGuidance != recoveryInput.AdditionalGuidance {
		t.Fatalf("Recovery turn = %#v", recoveryTurn)
	}

	// Same Recovery Command is a durable replay: zero new Provider calls,
	// no second Revision, and no second Synthesis dispatch.
	callsAfterRecovery := callCount()
	replayed, replayErr := ExecuteInteractionRecoverRevision(context.Background(), winner.input, provider, server.Client(), true)
	if replayErr != nil || callCount() != callsAfterRecovery || !reflect.DeepEqual(replayed.Workflow.Tasks, recovered.Workflow.Tasks) {
		t.Fatalf("Recovery replay = %#v, %v calls=%d want=%d", replayed, replayErr, callCount(), callsAfterRecovery)
	}
	// A fresh Command from an obsolete page/version is also default-denied;
	// unlike a same-ID replay it has no stored success to return, but it must
	// still consume zero Provider calls.
	staleInput := recoveryInput
	staleInput.CommandID = "CMD-BUDGET-CONTINUATION-RECOVERY-STALE"
	if _, staleErr := ExecuteInteractionRecoverRevision(context.Background(), staleInput, provider, server.Client(), true); staleErr == nil || callCount() != callsAfterRecovery {
		t.Fatalf("stale Recovery = %v calls=%d want rejection and %d calls", staleErr, callCount(), callsAfterRecovery)
	}

	// The recovery Workflow's fresh Autonomy Contract restores the standard
	// safe Budget rather than inheriting the exhausted six-call tracker.
	latest, ok := recovered.Session.LatestWorkflow()
	if !ok || latest.Autonomy == nil || latest.Autonomy.EffectiveMaxProviderCalls() != autonomy.DefaultMaxProviderCalls {
		t.Fatalf("recovery Autonomy = %#v", latest.Autonomy)
	}
}

// taskIdentityFromPrompt extracts a Task ID/Title pair from a Provider
// request's combined prompt text, given the exact line prefixes the real
// Prompt package uses -- "元タスクID: "/"元タスクタイトル: " for Review
// requests (prompt.BuildReview), "タスクID: "/"タイトル: " for Task
// execution requests (prompt.Builder.build) -- so the mock above can
// identify which Task lineage a request belongs to without any dependency
// on the real Prompt package itself.
func taskIdentityFromPrompt(combined, idPrefix, titlePrefix string) (taskID, title string) {
	for _, line := range strings.Split(combined, "\n") {
		switch {
		case strings.HasPrefix(line, idPrefix):
			taskID = strings.TrimPrefix(line, idPrefix)
		case strings.HasPrefix(line, titlePrefix):
			title = strings.TrimPrefix(line, titlePrefix)
		}
	}
	return taskID, title
}

func watchedTitleReferencesLineage(title string, watched map[string]bool) bool {
	for id := range watched {
		if id != "" && strings.Contains(title, id) {
			return true
		}
	}
	return false
}

// distinctRequestChangesOutput builds a Request Changes verdict whose
// finding description differs on every attempt, so the No-Progress
// Foundation's repeated-feedback signal never fires for it.
func distinctRequestChangesOutput(attempt int) string {
	issues := `[{"category":"requirements","severity":"medium","description":"要件が不足しています（指摘` +
		strconv.Itoa(attempt) + `件目）。","suggested_action":"要件を追記してください（指摘` + strconv.Itoa(attempt) + `件目）。"}]`
	encoded, err := json.Marshal(map[string]any{
		"verdict": string(review.VerdictRequestChanges), "issues": json.RawMessage(issues),
		"summary": "要件不足のため修正を依頼します（指摘" + strconv.Itoa(attempt) + "件目）。",
	})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// stalledTaskFromWorkflow finds the same Task interaction.stalledRevisionTaskID
// would surface: the last Task in the result whose Review verdict is
// Request Changes with no follow-up Revision Command -- i.e. the Task a
// Recovery Command should target.
func stalledTaskFromWorkflow(t *testing.T, tasks []interaction.WorkflowTaskEvidence) string {
	t.Helper()
	for index := len(tasks) - 1; index >= 0; index-- {
		current := tasks[index]
		if current.Verdict == review.VerdictRequestChanges && current.RevisionCommandID == "" {
			return current.TaskID
		}
	}
	t.Fatal("no stalled Revision Task found in Workflow evidence")
	return ""
}

// TestInteractionRecoverRevisionParallelBranchLimitThenRecoverySucceedsWithoutReExecutingOtherBranches
// is the parallel-branch Recovery integration proof required by the
// Revision Limit Recovery Checkpoint: of three independent branches (A, B,
// C) plus a Synthesis Task depending on all three, only B is made to
// repeatedly fail Review so it alone hits the Revision Guard
// (autonomy.DefaultMaxRevisionCount = 2 -- three attempts total). This
// proves, through the real production chain (interaction.plan.approve_and_execute
// -> workflow.reviewed.execute -> RunParallel), that: A and C's results are
// never lost or hidden; Synthesis is correctly never dispatched while B is
// still stuck; a single explicit interaction.workflow.recover_revision
// Command revises B alone; and once B Approves, Synthesis becomes ready and
// completes -- all without re-executing A or C a second time (no new
// Execute/Review Provider call for either Task ID after the initial round).
func TestInteractionRecoverRevisionParallelBranchLimitThenRecoverySucceedsWithoutReExecutingOtherBranches(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 19, 9, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-RECOVER-PARALLEL-001", "並列復旧アプリ"
	_, digest := writeApprovalRequiredSessionWithPlan(t, root, sessionID, writeApproveAndExecuteParallelPlan(projectName), at)

	// PROPOSED-002 ("競合調査を実施する") is created as TASK-002 -- the same
	// deterministic Plan-apply Task ID ordering
	// TestInteractionPlanApproveAndExecuteAutomaticallyParallelizesIndependentTasksThenSynthesis
	// already relies on -- and is this test's designated stuck branch (B).
	const branchBTaskID = "TASK-002"
	server, callCount, setForceApprove := recoveryScenarioMockServer(t, branchBTaskID, true)
	defer server.Close()
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}

	outerCommandID := "CMD-RECOVER-PARALLEL-001"
	applyInput := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-RECOVER-PARALLEL-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), applyInput, provider, server.Client(), true)

	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "REVISION_LIMIT_REACHED" || recorded.Stage != "revision_limit" {
		t.Fatalf("ExecuteInteractionPlanApproveAndExecute() error = %v, want a REVISION_LIMIT_REACHED/revision_limit RecordedCommandError", err)
	}
	if result.Workflow.Status != "partial_failure" || !result.SessionCommitted ||
		result.Session.State != interaction.StateWorkflowAttentionRequired {
		t.Fatalf("result = %#v", result)
	}

	seenTaskIDs := map[string]review.Verdict{}
	for _, current := range result.Workflow.Tasks {
		seenTaskIDs[current.TaskID] = current.Verdict
	}
	if seenTaskIDs["TASK-001"] != review.VerdictApprove || seenTaskIDs["TASK-003"] != review.VerdictApprove {
		t.Fatalf("branches A/C = %#v, want both Approved and preserved despite B's failure", seenTaskIDs)
	}
	if _, synthesisDispatched := seenTaskIDs["TASK-004"]; synthesisDispatched {
		t.Fatal("Synthesis Task TASK-004 must never be dispatched while branch B is still stuck")
	}

	evidence := result.Session.Turns[len(result.Session.Turns)-1].Workflow
	if evidence == nil {
		t.Fatal("Session's latest Turn carries no Workflow evidence")
	}
	stalledTaskID := stalledTaskFromWorkflow(t, evidence.Tasks)

	// Next() must surface exactly this stalled Task as the sole eligible
	// Recovery target, and no other operation.
	next, err := result.Session.Next()
	if err != nil || next.Operation != "interaction.workflow.recover_revision" || !next.ApprovalRequired ||
		len(next.EligibleTaskIDs) != 1 || next.EligibleTaskIDs[0] != stalledTaskID {
		t.Fatalf("Next() = %#v, %v, want a Recovery operation targeting %s", next, err, stalledTaskID)
	}

	// The Recovery Command's own new Revision Task is a deliberately
	// distinct lineage (anchored at the stalled Task's own ID, not
	// watchTaskID's) -- this scenario proves recovery success, not a
	// second run of the same repeated-failure script.
	setForceApprove(true)

	callsBeforeRecovery := callCount()
	var taskEvents []event.Event
	var eventsMu sync.Mutex
	observer := event.Observer{
		Types: []event.Type{event.TaskStarted, event.TaskCompleted},
		Handler: func(_ context.Context, published event.Event) error {
			eventsMu.Lock()
			taskEvents = append(taskEvents, published)
			eventsMu.Unlock()
			return nil
		},
	}
	recoveryCommandID := "CMD-RECOVER-PARALLEL-RECOVERY-001"
	recoveryInput := InteractionRecoverRevisionInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: result.Session.Version, TaskID: stalledTaskID,
		AdditionalGuidance: "この指摘は無視して、読みやすさを優先してください", CurrentTime: at.Add(10 * time.Minute),
		CommandID: recoveryCommandID, EventObservers: []event.Observer{observer},
	}
	recovered, recoverErr := ExecuteInteractionRecoverRevision(context.Background(), recoveryInput, provider, server.Client(), true)
	if recoverErr != nil || recovered.Workflow.Status != "completed" || !recovered.SessionCommitted ||
		recovered.Session.State != interaction.StateCompleted {
		t.Fatalf("ExecuteInteractionRecoverRevision() = %#v, %v", recovered, recoverErr)
	}

	// A/C must never be re-executed: the recovery round's own Workflow
	// result must only ever contain the new Revision Task (B's lineage
	// continuing) and Synthesis -- never TASK-001 or TASK-003 again.
	for _, current := range recovered.Workflow.Tasks {
		if current.TaskID == "TASK-001" || current.TaskID == "TASK-003" {
			t.Fatalf("recovery round re-dispatched already-completed Task %s: %#v", current.TaskID, recovered.Workflow.Tasks)
		}
	}
	recoveredIDs := map[string]review.Verdict{}
	for _, current := range recovered.Workflow.Tasks {
		recoveredIDs[current.TaskID] = current.Verdict
	}
	if recoveredIDs["TASK-004"] != review.VerdictApprove {
		t.Fatalf("Synthesis TASK-004 = %#v, want Approved once B recovered", recovered.Workflow.Tasks)
	}

	// No Provider call for A or C's Task ID occurred during recovery --
	// confirmed structurally (their rows are absent above) and via
	// published Task Events: only the new Revision Task and Synthesis ever
	// publish Started/Completed during this second Command.
	eventsMu.Lock()
	capturedEvents := append([]event.Event(nil), taskEvents...)
	eventsMu.Unlock()
	for _, current := range capturedEvents {
		if current.AggregateID == "TASK-001" || current.AggregateID == "TASK-003" {
			t.Fatalf("Task Event %s published for already-completed Task %s during recovery", current.Type, current.AggregateID)
		}
	}

	// Lineage: the Session's own Turn durably records which stalled Task
	// this Recovery targeted and the CEO's fresh guidance -- never silently
	// overwriting or losing that link.
	recoveryTurn := recovered.Session.Turns[len(recovered.Session.Turns)-2]
	if recoveryTurn.Kind != interaction.TurnRevisionRecoveryStarted || recoveryTurn.RecoveryTaskID != stalledTaskID ||
		recoveryTurn.RecoveryGuidance != recoveryInput.AdditionalGuidance {
		t.Fatalf("recovery Turn = %#v, want RecoveryTaskID=%s RecoveryGuidance=%q", recoveryTurn, stalledTaskID, recoveryInput.AdditionalGuidance)
	}
	if recovered.Revision.Task == nil || recovered.Revision.Task.ID == stalledTaskID {
		t.Fatalf("recovery must create a genuinely new Revision Task distinct from the stalled one: %#v", recovered.Revision)
	}

	// Idempotency: replaying the same Recovery Command ID must not call the
	// Provider again or re-create a second Revision Task.
	callsAfterRecovery := callCount()
	replayed, replayErr := ExecuteInteractionRecoverRevision(context.Background(), recoveryInput, provider, server.Client(), true)
	if replayErr != nil || callCount() != callsAfterRecovery || replayed.Revision.Task == nil ||
		replayed.Revision.Task.ID != recovered.Revision.Task.ID {
		t.Fatalf("replay = %#v, %v calls=%d (want %d, unchanged)", replayed, replayErr, callCount(), callsAfterRecovery)
	}
	if callsAfterRecovery <= callsBeforeRecovery {
		t.Fatalf("recovery itself made no new Provider calls: before=%d after=%d", callsBeforeRecovery, callsAfterRecovery)
	}
}

// TestInteractionRecoverRevisionParallelBranchNoProgressThenRecoverySucceedsWithoutReExecutingOtherBranches
// is Progress Intelligence v1's parallel-branch proof (mirroring the
// Revision Guard proof above, but for the No-Progress stop instead): of
// three independent branches (A, B, C) plus a Synthesis Task depending on
// all three, only B is made to repeat the same structural QA finding
// while its own Deliverable body never actually changes between attempts
// -- CompoundProgressPolicy escalates before the Revision Guard's own hard
// count would have. This proves, through the same real production chain,
// that Progress Intelligence's stop is recoverable exactly like the
// Revision Guard's: A and C's results are never lost, Synthesis never
// dispatches early, a single Recovery Command revises B alone, and once B
// Approves, Synthesis becomes ready and completes -- without re-executing
// A or C.
func TestInteractionRecoverRevisionParallelBranchNoProgressThenRecoverySucceedsWithoutReExecutingOtherBranches(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 19, 9, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-RECOVER-NOPROGRESS-001", "並列復旧アプリ2"
	_, digest := writeApprovalRequiredSessionWithPlan(t, root, sessionID, writeApproveAndExecuteParallelPlan(projectName), at)

	const branchBTaskID = "TASK-002"
	server, _, setForceApprove := recoveryScenarioMockServer(t, branchBTaskID, false)
	defer server.Close()
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}

	outerCommandID := "CMD-RECOVER-NOPROGRESS-001"
	applyInput := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 2, ProjectID: "PROJECT-RECOVER-NOPROGRESS-001",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), applyInput, provider, server.Client(), true)

	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "NO_PROGRESS_DETECTED" || recorded.Stage != "no_progress" {
		t.Fatalf("ExecuteInteractionPlanApproveAndExecute() error = %v, want a NO_PROGRESS_DETECTED/no_progress RecordedCommandError", err)
	}
	if result.Workflow.Status != "partial_failure" || !result.SessionCommitted ||
		result.Session.State != interaction.StateWorkflowAttentionRequired {
		t.Fatalf("result = %#v", result)
	}

	seenTaskIDs := map[string]review.Verdict{}
	for _, current := range result.Workflow.Tasks {
		seenTaskIDs[current.TaskID] = current.Verdict
	}
	if seenTaskIDs["TASK-001"] != review.VerdictApprove || seenTaskIDs["TASK-003"] != review.VerdictApprove {
		t.Fatalf("branches A/C = %#v, want both Approved and preserved despite B's failure", seenTaskIDs)
	}
	if _, synthesisDispatched := seenTaskIDs["TASK-004"]; synthesisDispatched {
		t.Fatal("Synthesis Task TASK-004 must never be dispatched while branch B is still stuck")
	}

	evidence := result.Session.Turns[len(result.Session.Turns)-1].Workflow
	if evidence == nil {
		t.Fatal("Session's latest Turn carries no Workflow evidence")
	}
	stalledTaskID := stalledTaskFromWorkflow(t, evidence.Tasks)

	next, err := result.Session.Next()
	if err != nil || next.Operation != "interaction.workflow.recover_revision" || !next.ApprovalRequired ||
		len(next.EligibleTaskIDs) != 1 || next.EligibleTaskIDs[0] != stalledTaskID {
		t.Fatalf("Next() = %#v, %v, want a Recovery operation targeting %s", next, err, stalledTaskID)
	}

	setForceApprove(true)
	recoveryCommandID := "CMD-RECOVER-NOPROGRESS-RECOVERY-001"
	recoveryInput := InteractionRecoverRevisionInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: result.Session.Version, TaskID: stalledTaskID,
		AdditionalGuidance: "同じ指摘が続いていますが、この観点は無視して続けてください", CurrentTime: at.Add(10 * time.Minute),
		CommandID: recoveryCommandID,
	}
	recovered, recoverErr := ExecuteInteractionRecoverRevision(context.Background(), recoveryInput, provider, server.Client(), true)
	if recoverErr != nil || recovered.Workflow.Status != "completed" || !recovered.SessionCommitted ||
		recovered.Session.State != interaction.StateCompleted {
		t.Fatalf("ExecuteInteractionRecoverRevision() = %#v, %v", recovered, recoverErr)
	}

	for _, current := range recovered.Workflow.Tasks {
		if current.TaskID == "TASK-001" || current.TaskID == "TASK-003" {
			t.Fatalf("recovery round re-dispatched already-completed Task %s: %#v", current.TaskID, recovered.Workflow.Tasks)
		}
	}
	recoveredIDs := map[string]review.Verdict{}
	for _, current := range recovered.Workflow.Tasks {
		recoveredIDs[current.TaskID] = current.Verdict
	}
	if recoveredIDs["TASK-004"] != review.VerdictApprove {
		t.Fatalf("Synthesis TASK-004 = %#v, want Approved once B recovered", recovered.Workflow.Tasks)
	}
}
