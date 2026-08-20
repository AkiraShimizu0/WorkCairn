package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
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
func recoveryScenarioMockServer(t *testing.T, watchTaskID string) (server *httptest.Server, callCount func() int, setForceApprove func(bool)) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	requestChangesCount := 0
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
			ownTaskID, titleLine := reviewedTaskIdentity(combined)
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
			output = "# deliverable\n\n本文"
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

// reviewedTaskIdentity extracts the "元タスクID"/"元タスクタイトル" lines a
// Review Provider request always carries (prompt.BuildReview) so the mock
// above can identify which Task lineage a request belongs to without any
// dependency on the real Prompt package.
func reviewedTaskIdentity(combined string) (taskID, title string) {
	for _, line := range strings.Split(combined, "\n") {
		switch {
		case strings.HasPrefix(line, "元タスクID: "):
			taskID = strings.TrimPrefix(line, "元タスクID: ")
		case strings.HasPrefix(line, "元タスクタイトル: "):
			title = strings.TrimPrefix(line, "元タスクタイトル: ")
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
	server, callCount, setForceApprove := recoveryScenarioMockServer(t, branchBTaskID)
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
