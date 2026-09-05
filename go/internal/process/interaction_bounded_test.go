package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
)

// writeBoundedApprovalRequiredSession is writeApprovalRequiredSession's
// bounded_acceptance counterpart (ADR-0072): a Session created via
// NewWithProfile, already at plan_approval_required with a canonical Plan
// committed, ready for ExecuteInteractionPlanApproveAndExecute. It follows
// the mandatory v1 (created) -> v2 (Plan generation reservation) -> v3
// (Plan) Version sequence -- RecordPlan itself now requires a preceding
// reservation for a bounded_acceptance Session, so this fixture must too.
func writeBoundedApprovalRequiredSession(t *testing.T, root, sessionID string, plan ceoplan.Plan, at time.Time) (interaction.Record, string) {
	t.Helper()
	record, err := interaction.NewWithProfile(sessionID, "アプリを完成させる", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := record.RecordPlanGenerationReservation("CHILD-fffffffffffffffffffffffffffffffe", at.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := reserved.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := withPlan.CurrentPlan()
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), reserved, record.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), withPlan, reserved.Version); err != nil {
		t.Fatal(err)
	}
	return withPlan, digest
}

func writeTwoTaskPlan(projectName string) ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: projectName, Objective: "完成させる", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{
			{ProposalID: "PROPOSED-001", Title: "最初の機能を実装する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"},
			{ProposalID: "PROPOSED-002", Title: "次の機能を実装する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"},
		},
		Risks: []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
}

// sequentialProviderServer answers a fixed sequence of outputs in call
// order (not safe for concurrent use -- exactly what a bounded, MaxTasks=1
// execution guarantees no dispatch ever needs to be).
func sequentialProviderServer(t *testing.T, outputs []string) (*httptest.Server, func() int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if calls >= len(outputs) {
			t.Fatalf("unexpected Provider call #%d beyond the scripted %d outputs", calls+1, len(outputs))
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": outputs[calls]}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		calls++
		_, _ = response.Write(encoded)
	}))
	return server, func() int { return calls }
}

func TestBoundedApproveAndExecuteRejectsNonSingleTaskPlan(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BOUNDED-2TASK", "二件アプリ"
	_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeTwoTaskPlan(projectName), at)

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("Provider must never be called for a bounded Session whose Plan is not exactly 1 Task")
	}))
	defer server.Close()

	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-2TASK",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-BOUNDED-2TASK",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	if err == nil || result.Apply.Status == "applied" {
		t.Fatalf("2-task bounded approve_and_execute = %#v, %v, want rejected before any effect", result, err)
	}
	if _, err := vault.NewLoader(root); err != nil {
		t.Fatal(err)
	}
	// Project directory must never have been created.
	if _, statErr := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName}); statErr == nil {
		if store, storeErr := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName}); storeErr == nil {
			if tasks, inspectErr := store.InspectAll(context.Background()); inspectErr == nil && len(tasks) > 0 {
				t.Fatalf("Task Store unexpectedly has %d Tasks after a rejected 2-task bounded Plan", len(tasks))
			}
		}
	}
}

func TestBoundedApproveAndExecuteCompletesWithExactlyTwoProviderCalls(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BOUNDED-HAPPY", "限定確認アプリ"
	_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeApproveAndExecutePlan(projectName), at)

	server, calls := sequentialProviderServer(t, []string{"# 成果物\n\n完成した機能です。", reviewProviderOutput(review.VerdictApprove)})
	defer server.Close()

	outerCommandID := "CMD-BOUNDED-HAPPY-001"
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-HAPPY",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	if err != nil || result.Apply.Status != "applied" || result.Workflow.Status != "completed" ||
		result.Session.State != interaction.StateCompleted {
		t.Fatalf("bounded approve_and_execute = %#v, %v", result, err)
	}
	if got := calls(); got != 2 {
		t.Fatalf("Provider calls = %d, want exactly 2 (Task execution + Review)", got)
	}
}

func TestBoundedApproveAndExecuteRequestChangesStopsBeforeRevision(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BOUNDED-REQCHANGES", "限定確認要修正アプリ"
	_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeApproveAndExecutePlan(projectName), at)

	server, calls := sequentialProviderServer(t, []string{"# 成果物\n\n下書きです。", reviewProviderOutput(review.VerdictRequestChanges)})
	defer server.Close()

	var eventMu sync.Mutex
	var reviewEvents []event.Event
	observer := event.Observer{
		Types: []event.Type{event.ReviewCompleted},
		Handler: func(_ context.Context, published event.Event) error {
			eventMu.Lock()
			reviewEvents = append(reviewEvents, published)
			eventMu.Unlock()
			return nil
		},
	}

	outerCommandID := "CMD-BOUNDED-REQCHANGES-001"
	result, err := ExecuteInteractionPlanApproveAndExecute(context.Background(), InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-REQCHANGES",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: outerCommandID,
		EventObservers: []event.Observer{observer},
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}, server.Client(), true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "REVIEWED_WORKFLOW_BOUNDED_STOP" {
		t.Fatalf("bounded Request Changes error = %#v, %v, want Code=REVIEWED_WORKFLOW_BOUNDED_STOP", recorded, err)
	}
	if recorded.Envelope == nil || recorded.Envelope.RecoveryRequired {
		t.Fatalf("bounded Request Changes RecoveryRequired = %#v, want false", recorded.Envelope)
	}
	if got := calls(); got != 2 {
		t.Fatalf("Provider calls = %d, want exactly 2 (no 3rd call for a forbidden Revision)", got)
	}
	evidence := result.Session.Turns[len(result.Session.Turns)-1].Workflow
	if evidence == nil || evidence.Tasks[0].Verdict != review.VerdictRequestChanges || evidence.Tasks[0].RevisionCommandID != "" {
		t.Fatalf("Workflow evidence = %#v, want a saved Request Changes verdict and no Revision Command", evidence)
	}
	workspaceLedger, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	outerRecord, err := workspaceLedger.Get(context.Background(), outerCommandID)
	if err != nil || outerRecord.Failure == nil || outerRecord.Failure.Code != "REVIEWED_WORKFLOW_BOUNDED_STOP" ||
		outerRecord.Failure.Details == nil || outerRecord.Failure.Details.RecoveryRequired {
		t.Fatalf("outer Ledger record = %#v, %v, want RecoveryRequired=false", outerRecord, err)
	}
	nextAction, nextErr := result.Session.Next()
	if nextErr != nil || nextAction.Operation == "interaction.workflow.recover_revision" || nextAction.ApprovalRequired {
		t.Fatalf("Next() after bounded Request Changes = %#v, %v, want inspect-only, no recover_revision offer", nextAction, nextErr)
	}

	// Direct evidence assertions (PB-3an.2a item 6): the Review artifact
	// actually committed to disk, an Audit Log entry recording it, and the
	// review.completed Event actually published -- not just the Session's
	// own summarized Workflow evidence.
	reviewArtifactPath := filepath.Join(root, filepath.FromSlash("プロジェクト/"+projectName+"/Reviews/TASK-001.review.json"))
	reviewArtifact, err := os.ReadFile(reviewArtifactPath)
	if err != nil {
		t.Fatalf("missing bounded Review artifact at %s: %v", reviewArtifactPath, err)
	}
	if !strings.Contains(string(reviewArtifact), `"Request Changes"`) {
		t.Fatalf("Review artifact = %s, want a committed Request Changes verdict", reviewArtifact)
	}
	auditLogPath := filepath.Join(root, filepath.FromSlash("プロジェクト/"+projectName+"/Audit Log.md"))
	auditLog, err := os.ReadFile(auditLogPath)
	if err != nil {
		t.Fatalf("missing Audit Log at %s: %v", auditLogPath, err)
	}
	if !strings.Contains(string(auditLog), "TASK-001") {
		t.Fatalf("Audit Log = %s, want an entry for TASK-001", auditLog)
	}
	eventMu.Lock()
	publishedReviewEvents := len(reviewEvents)
	eventMu.Unlock()
	if publishedReviewEvents != 1 {
		t.Fatalf("review.completed Events published = %d, want exactly 1", publishedReviewEvents)
	}
}

// TestBoundedApproveAndExecuteTerminalReservationRejectsSecondOuterCommand
// is a sequential (not concurrent) regression: once a reservation has
// already reached a terminal `consumed` state, a second, later outer
// Command targeting the same {SessionID, ExpectedVersion, PlanDigest,
// Profile} must be rejected without applying again. This exercises the
// "already-terminal reservation" branch specifically -- see
// TestBoundedApproveAndExecuteConcurrentApprovalRaceOnlyOneWins below for
// the genuinely concurrent case (two goroutines actually racing).
func TestBoundedApproveAndExecuteTerminalReservationRejectsSecondOuterCommand(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BOUNDED-TERMINAL-RESERVE", "限定確認再試行アプリ"
	_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeApproveAndExecutePlan(projectName), at)

	server, calls := sequentialProviderServer(t, []string{"# 成果物\n\n完成した機能です。", reviewProviderOutput(review.VerdictApprove)})
	defer server.Close()

	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-TERMINAL-RESERVE",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute),
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}

	first := input
	first.CommandID = "CMD-BOUNDED-TERMINAL-RESERVE-A"
	firstResult, firstErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), first, provider, server.Client(), true)
	if firstErr != nil || firstResult.Apply.Status != "applied" {
		t.Fatalf("first (winner) approve_and_execute = %#v, %v", firstResult, firstErr)
	}

	second := input
	second.CommandID = "CMD-BOUNDED-TERMINAL-RESERVE-B"
	secondResult, secondErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), second, provider, server.Client(), true)
	if secondErr == nil || secondResult.Apply.Status == "applied" {
		t.Fatalf("second (later) approve_and_execute = %#v, %v, want rejected without applying again", secondResult, secondErr)
	}
	if got := calls(); got != 2 {
		t.Fatalf("Provider calls across both attempts = %d, want exactly 2 (only the first ever executed)", got)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.InspectAll(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Task Store after two sequential bounded approvals = %#v, %v, want exactly 1 Task created", tasks, err)
	}
}

// raceApproveAndExecuteMockServer is a thread-safe Provider mock with
// independent Task-execution and Review call counters, plus a deterministic
// pause on the Task execution call specifically (PB-3an.2d item 1). Because
// a bounded_acceptance approval reservation is consumed (ADR-0072) before
// Child 1 (Project/Task creation) ever runs, only the winning outer
// Command's own Task execution call can ever reach this handler -- pausing
// it here holds the winner genuinely mid-flight, already past the point its
// reservation was consumed but not yet returned, so a test can observe the
// loser reach its own terminal rejection while the winner still has not
// returned. That proves the two outer Commands actually overlapped in time
// rather than merely finishing with the right end state after running one
// after another. taskStarted closes exactly once, the instant the (unique)
// Task execution call arrives; release is closed by the caller to let that
// call's response proceed.
func raceApproveAndExecuteMockServer(t *testing.T) (server *httptest.Server, taskCalls func() int, reviewCalls func() int, taskStarted <-chan struct{}, release chan<- struct{}) {
	t.Helper()
	var mu sync.Mutex
	var startedOnce sync.Once
	tasks, reviews := 0, 0
	started := make(chan struct{})
	releaseChan := make(chan struct{})
	handler := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var decoded struct {
			OutputConfig json.RawMessage `json:"output_config"`
		}
		_ = json.Unmarshal(body, &decoded)
		structured := len(decoded.OutputConfig) > 0
		output := "# deliverable\n\n本文"
		if structured {
			mu.Lock()
			reviews++
			mu.Unlock()
			output = reviewProviderOutput(review.VerdictApprove)
		} else {
			mu.Lock()
			tasks++
			mu.Unlock()
			startedOnce.Do(func() { close(started) })
			<-releaseChan // paused here until the caller confirms the loser is terminal
		}
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
	return handler, func() int { mu.Lock(); defer mu.Unlock(); return tasks }, func() int { mu.Lock(); defer mu.Unlock(); return reviews }, started, releaseChan
}

// TestBoundedApproveAndExecuteConcurrentApprovalRaceOnlyOneWins is the
// PB-3an.2a P1 fix, strengthened by PB-3an.2d (item 1): a genuine
// concurrency test (two real goroutines, each signaling readiness before a
// shared barrier releases both at the same instant, both racing against the
// same real temporary Vault on disk) rather than two sequential calls
// dressed up as "concurrent". The winner is deterministically paused (via
// raceApproveAndExecuteMockServer) at its own Task execution Provider call
// -- necessarily after its reservation is already consumed -- and the test
// waits for the loser to independently reach a terminal Ledger state before
// releasing the winner, so the observed collision is proven to be real
// overlap, not a probable-but-unconfirmed outcome. It asserts every
// invariant the approval reservation exists to guarantee, all in one
// race-safe (go test -race clean) test.
func TestBoundedApproveAndExecuteConcurrentApprovalRaceOnlyOneWins(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BOUNDED-RACE", "限定確認実並行アプリ"
	_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeApproveAndExecutePlan(projectName), at)

	server, taskCalls, reviewCalls, taskStarted, release := raceApproveAndExecuteMockServer(t)
	defer server.Close()

	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-RACE",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute),
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	commandIDs := [2]string{"CMD-BOUNDED-RACE-A", "CMD-BOUNDED-RACE-B"}
	results := [2]InteractionApproveAndExecuteResult{}
	errs := [2]error{}
	done := [2]chan struct{}{make(chan struct{}), make(chan struct{})}

	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func(i int) {
			ready <- struct{}{} // signal this goroutine is alive and about to block on the barrier
			<-start             // every goroutine blocks here until the barrier below releases all of them at once
			attempt := input
			attempt.CommandID = commandIDs[i]
			results[i], errs[i] = ExecuteInteractionPlanApproveAndExecute(context.Background(), attempt, provider, server.Client(), true)
			close(done[i])
		}(i)
	}
	<-ready
	<-ready      // both goroutines have reached the barrier before it is released
	close(start) // releases both goroutines simultaneously -- the actual race

	const safetyTimeout = 10 * time.Second
	select {
	case <-taskStarted:
	case <-time.After(safetyTimeout):
		t.Fatal("winner never reached its Task execution Provider call -- reservation consume must run before Child 1")
	}

	// The winner is now genuinely paused mid-flight. Waiting here for the
	// loser's own outer Command to reach a terminal state -- confirmed
	// below to happen while the winner has NOT yet returned -- proves the
	// two outer Commands really overlapped, not merely that they finished
	// with the right end state after running one after another.
	loserIndex := -1
	select {
	case <-done[0]:
		loserIndex = 0
	case <-done[1]:
		loserIndex = 1
	case <-time.After(safetyTimeout):
		t.Fatal("neither outer Command reached a terminal state while the winner was paused -- want the loser rejected during that window")
	}
	winnerIndex := 1 - loserIndex
	select {
	case <-done[winnerIndex]:
		t.Fatalf("winner (index %d) returned before its blocked Task execution Provider call was released -- the two outer Commands did not genuinely overlap", winnerIndex)
	default:
	}
	if errs[loserIndex] == nil {
		t.Fatalf("loser (index %d) error = nil, want a typed non-success", loserIndex)
	}

	// Durable evidence, not just the goroutine's own return: the loser's
	// outer Command Ledger record is independently terminal right now,
	// while the winner is still paused.
	workspaceLedger, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loserRecordDuringPause, err := workspaceLedger.Get(context.Background(), commandIDs[loserIndex])
	if err != nil || !loserRecordDuringPause.State.Terminal() {
		t.Fatalf("loser outer Command %s while winner is paused = %#v, %v, want already terminal", commandIDs[loserIndex], loserRecordDuringPause, err)
	}

	close(release) // only now does the winner's blocked Task execution response proceed
	select {
	case <-done[winnerIndex]:
	case <-time.After(safetyTimeout):
		t.Fatal("winner never returned after its blocked Task execution Provider call was released")
	}

	if errs[winnerIndex] != nil || results[winnerIndex].Apply.Status != "applied" {
		t.Fatalf("winner (index %d) = %#v, %v, want a successful approve_and_execute", winnerIndex, results[winnerIndex], errs[winnerIndex])
	}
	var recorded *RecordedCommandError
	if !errors.As(errs[loserIndex], &recorded) || recorded.Code != "INTERACTION_APPROVE_AND_EXECUTE_FAILED" || recorded.Stage != "approval_reservation" {
		t.Fatalf("loser error = %#v, %v, want Code=INTERACTION_APPROVE_AND_EXECUTE_FAILED Stage=approval_reservation", recorded, errs[loserIndex])
	}

	// Both outer Commands' own Ledger records: winner succeeded, loser failed.
	winnerRecord, err := workspaceLedger.Get(context.Background(), commandIDs[winnerIndex])
	if err != nil || winnerRecord.State != commandledger.StateSucceeded {
		t.Fatalf("winner outer Command %s = %#v, %v, want State=succeeded", commandIDs[winnerIndex], winnerRecord, err)
	}
	loserRecord, err := workspaceLedger.Get(context.Background(), commandIDs[loserIndex])
	if err != nil || loserRecord.State != commandledger.StateFailed {
		t.Fatalf("loser outer Command %s = %#v, %v, want State=failed", commandIDs[loserIndex], loserRecord, err)
	}

	// The reservation Command itself: terminal, consumed=true.
	reservationID, err := commandledger.DeriveApprovalReservationID(commandledger.ApprovalReservationID{
		SessionID: sessionID, ExpectedVersion: input.ExpectedVersion, PlanDigest: digest, Profile: "bounded_acceptance",
	})
	if err != nil {
		t.Fatal(err)
	}
	reservationRecord, err := workspaceLedger.Get(context.Background(), reservationID)
	if err != nil || reservationRecord.State != commandledger.StateSucceeded {
		t.Fatalf("reservation Command %s = %#v, %v, want State=succeeded", reservationID, reservationRecord, err)
	}
	var reservationResult approvalReservationResult
	if err := json.Unmarshal(reservationRecord.Result, &reservationResult); err != nil || !reservationResult.Consumed {
		t.Fatalf("reservation Command result = %s, %v, want consumed=true", reservationRecord.Result, err)
	}

	// Project created exactly once, Task created exactly once. プロジェクト/
	// also holds a non-directory workspace lock file alongside each Project
	// directory, so only directory entries count as Projects here.
	projectEntries, err := os.ReadDir(filepath.Join(root, "プロジェクト"))
	if err != nil {
		t.Fatal(err)
	}
	projectDirs := 0
	for _, entry := range projectEntries {
		if entry.IsDir() {
			projectDirs++
		}
	}
	if projectDirs != 1 {
		t.Fatalf("プロジェクト/ has %d Project directories after the race, want exactly 1: %v", projectDirs, projectEntries)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: projectName})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.InspectAll(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Task Store after the race = %#v, %v, want exactly 1 Task created", tasks, err)
	}

	// Task Provider call 1, Review Provider call 1 -- the loser never
	// reaches the Provider at all, regardless of which one won. Together
	// with the winner's own Workflow evidence below (canonical Review,
	// nil Revision), this also proves there is no retry, fallback, or
	// extra Review of any kind: any such call would have incremented one
	// of these two counters or populated Revision.
	if got := taskCalls(); got != 1 {
		t.Fatalf("Task execution Provider calls = %d, want exactly 1", got)
	}
	if got := reviewCalls(); got != 1 {
		t.Fatalf("Review Provider calls = %d, want exactly 1", got)
	}

	// The winner's own typed Workflow evidence: canonical Review committed
	// exactly once, no Revision ever attempted -- a nil Revision result
	// means revision.Result, the Revisions/*.revision.md intent file, and
	// any Revision Task deliverable were never created (Revision Task,
	// Revision Command, and Revision intent/artifact are all zero).
	winnerTask := results[winnerIndex].Workflow.Tasks[0]
	if winnerTask.Verdict != review.VerdictApprove || winnerTask.Review == nil || winnerTask.Review.Artifact == nil || !winnerTask.Review.Artifact.CanonicalCommitted {
		t.Fatalf("winner Task result = %#v, want an Approve verdict with a canonically committed Review", winnerTask)
	}
	if winnerTask.RevisionCommandID != "" || winnerTask.Revision != nil {
		t.Fatalf("winner Task result = %#v, want no Revision (Command, Task, or intent/artifact)", winnerTask)
	}
}

func TestBoundedApproveAndExecuteOuterReplaySkipsReservationAndProvider(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID, projectName := "SESSION-BOUNDED-REPLAY", "限定確認再送アプリ"
	_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeApproveAndExecutePlan(projectName), at)

	server, calls := sequentialProviderServer(t, []string{"# 成果物\n\n完成した機能です。", reviewProviderOutput(review.VerdictApprove)})
	defer server.Close()

	input := InteractionApplyInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-REPLAY",
		PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-BOUNDED-REPLAY-001",
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	first, firstErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	replayed, replayErr := ExecuteInteractionPlanApproveAndExecute(context.Background(), input, provider, server.Client(), true)
	if replayErr != nil || replayed.Workflow.Status != first.Workflow.Status {
		t.Fatalf("replayed approve_and_execute = %#v, %v, want cached terminal result", replayed, replayErr)
	}
	if got := calls(); got != 2 {
		t.Fatalf("Provider calls after replay = %d, want still exactly 2 (replay never re-invokes)", got)
	}
}

func TestBoundedStandaloneEntryPointsRejectBeforeAnyEffect(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	unreachableProvider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("Provider must never be called for any bounded standalone entry point this test exercises")
	}))
	defer unreachableProvider.Close()
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: unreachableProvider.URL}

	t.Run("interaction.plan.apply", func(t *testing.T) {
		sessionID, projectName := "SESSION-BOUNDED-STANDALONE-APPLY", "限定確認standalone適用アプリ"
		_, digest := writeBoundedApprovalRequiredSession(t, root, sessionID, writeApproveAndExecutePlan(projectName), at)
		result, err := ExecuteInteractionPlanApply(context.Background(), InteractionApplyInput{
			VaultRoot: root, SessionID: sessionID, ExpectedVersion: 3, ProjectID: "PROJECT-BOUNDED-STANDALONE-APPLY",
			PlanDigest: digest, CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-BOUNDED-STANDALONE-APPLY",
		}, true)
		if err == nil || result.Apply.Status == "applied" {
			t.Fatalf("standalone interaction.plan.apply = %#v, %v, want rejected", result, err)
		}
	})

	t.Run("interaction.answer", func(t *testing.T) {
		sessionID := "SESSION-BOUNDED-STANDALONE-ANSWER"
		record, err := interaction.NewWithProfile(sessionID, "依頼", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := record.RecordPlanGenerationReservation("CHILD-0000000000000000000000000000aa", at.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		withPlan, err := reserved.RecordPlan(interactionTestPlanForBoundedProcess([]string{"対象端末は？"}), at.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		store, err := vault.NewInteractionStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if err := store.Update(context.Background(), reserved, record.Version); err != nil {
			t.Fatal(err)
		}
		if err := store.Update(context.Background(), withPlan, reserved.Version); err != nil {
			t.Fatal(err)
		}
		result, err := ExecuteInteractionAnswer(context.Background(), InteractionAnswerInput{
			VaultRoot: root, SessionID: sessionID, ExpectedVersion: withPlan.Version,
			Answers:     []interaction.Answer{{Question: "対象端末は？", Answer: "Web"}},
			CurrentTime: at.Add(3 * time.Minute), CommandID: "CMD-BOUNDED-STANDALONE-ANSWER",
		}, provider, unreachableProvider.Client(), true)
		if err == nil || result.Session.State == interaction.StatePlanGenerationApprovalRequired {
			t.Fatalf("interaction.answer for bounded clarification = %#v, %v, want rejected", result, err)
		}
	})

	t.Run("interaction.workflow.execute", func(t *testing.T) {
		sessionID := "SESSION-BOUNDED-STANDALONE-WORKFLOW"
		record, err := interaction.NewWithProfile(sessionID, "依頼", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := record.RecordPlanGenerationReservation("CHILD-eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", at.Add(30*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		withPlan, err := reserved.RecordPlan(interactionTestPlanForBoundedProcess([]string{}), at.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		_, digest, _ := withPlan.CurrentPlan()
		ready, err := withPlan.RecordApplied("PROJECT-BOUNDED-STANDALONE-WORKFLOW", "案件", digest, "", at.Add(2*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		store, err := vault.NewInteractionStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if err := store.Update(context.Background(), reserved, record.Version); err != nil {
			t.Fatal(err)
		}
		if err := store.Update(context.Background(), withPlan, reserved.Version); err != nil {
			t.Fatal(err)
		}
		if err := store.Update(context.Background(), ready, withPlan.Version); err != nil {
			t.Fatal(err)
		}
		result, err := ExecuteInteractionWorkflow(context.Background(), ExecuteInteractionWorkflowInput{
			InteractionWorkflowPlanInput: InteractionWorkflowPlanInput{
				VaultRoot: root, SessionID: sessionID, ExpectedVersion: ready.Version, ReviewerID: "QA-001",
				CurrentTime: at.Add(3 * time.Minute), MaxTasks: 1,
			},
			WorkflowPlanDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			CommandID:          "CMD-BOUNDED-STANDALONE-WORKFLOW",
		}, provider, unreachableProvider.Client(), true)
		if err == nil {
			t.Fatalf("standalone interaction.workflow.execute for bounded Session = %#v, %v, want rejected", result, err)
		}
	})

	t.Run("interaction.workflow.recover_revision", func(t *testing.T) {
		sessionID := "SESSION-BOUNDED-STANDALONE-RECOVER"
		record, err := interaction.NewWithProfile(sessionID, "依頼", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
		if err != nil {
			t.Fatal(err)
		}
		store, err := vault.NewInteractionStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Create(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		result, err := ExecuteInteractionRecoverRevision(context.Background(), InteractionRecoverRevisionInput{
			VaultRoot: root, SessionID: sessionID, ExpectedVersion: record.Version, TaskID: "TASK-001",
			CurrentTime: at.Add(time.Minute), CommandID: "CMD-BOUNDED-STANDALONE-RECOVER",
		}, provider, unreachableProvider.Client(), true)
		if err == nil {
			t.Fatalf("interaction.workflow.recover_revision for bounded Session = %#v, %v, want rejected", result, err)
		}
	})
}

func interactionTestPlanForBoundedProcess(questions []string) ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: "案件", Objective: "目的", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Product Manager"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "計画する", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要なため",
		}},
		Risks: []string{}, CEOQuestions: questions, PlanOnly: true,
	}
}

// TestBoundedPlanGenerationReservationBlocksRetryAfterTimeout is
// TestBoundedPlanGenerationReservationBlocksRetryAfterFailure's timeout
// variant (PB-3an.2a item 6): a Provider that never responds (hangs) forces
// a genuine client-side timeout error, distinct in shape from a Provider
// returning an HTTP error status -- the reservation must still be
// committed and durable, and a retry with a new outer Command ID must
// still reach zero further Provider attempts.
func TestBoundedPlanGenerationReservationBlocksRetryAfterTimeout(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID := "SESSION-BOUNDED-RESERVE-TIMEOUT"

	block := make(chan struct{})
	hangingServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block // never respond until the test itself tears down
	}))
	// t.Cleanup runs LIFO: registering Close() first and close(block)
	// second means close(block) fires first, unblocking the hung handler,
	// so hangingServer.Close() (which waits for active connections) can
	// then actually complete instead of deadlocking against the timed-out
	// client's still-open connection.
	t.Cleanup(hangingServer.Close)
	t.Cleanup(func() { close(block) })
	timeoutClient := hangingServer.Client()
	timeoutClient.Timeout = 50 * time.Millisecond

	candidate, err := interaction.NewWithProfile(sessionID, "アプリを完成させる", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, firstErr := ExecuteInteractionStart(context.Background(), InteractionStartInput{
		VaultRoot: root, SessionID: sessionID, Request: candidate.Request, RequestDigest: candidate.RequestDigest,
		Model: candidate.Model, CurrentTime: at, CommandID: "CMD-BOUNDED-RESERVE-TIMEOUT-1", Profile: "bounded_acceptance",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: hangingServer.URL}, timeoutClient, true)
	if firstErr == nil {
		t.Fatalf("first attempt against a hanging Provider = %#v, %v, want a client-side timeout error", firstResult, firstErr)
	}
	if firstResult.Session.State != interaction.StatePlanGenerationApprovalRequired || firstResult.Session.Version != 2 {
		t.Fatalf("Session after timed-out first attempt = %#v, want Version=2 (reservation committed, no result Turn)", firstResult.Session)
	}
	if _, reserved := firstResult.Session.PlanGenerationReservation(); !reserved {
		t.Fatal("reservation Turn must be committed even though the Provider call timed out")
	}

	unreachableServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("a retry with a new outer Command ID must never reach the Provider once a bounded Session's Plan generation is reserved, even after a timeout")
	}))
	defer unreachableServer.Close()
	secondResult, secondErr := ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: firstResult.Session.Version,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-BOUNDED-RESERVE-TIMEOUT-2",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: unreachableServer.URL}, unreachableServer.Client(), true)
	if secondErr == nil || secondResult.Session.State != interaction.StatePlanGenerationApprovalRequired {
		t.Fatalf("retry after timeout with a new outer Command ID = %#v, %v, want rejected without a Provider call", secondResult, secondErr)
	}
}

func TestBoundedPlanGenerationReservationBlocksRetryAfterFailure(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	sessionID := "SESSION-BOUNDED-RESERVE-RETRY"

	failingServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{"error":{"type":"overloaded_error","message":"unavailable"}}`))
	}))
	defer failingServer.Close()

	candidate, err := interaction.NewWithProfile(sessionID, "アプリを完成させる", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	firstResult, firstErr := ExecuteInteractionStart(context.Background(), InteractionStartInput{
		VaultRoot: root, SessionID: sessionID, Request: candidate.Request, RequestDigest: candidate.RequestDigest,
		Model: candidate.Model, CurrentTime: at, CommandID: "CMD-BOUNDED-RESERVE-RETRY-1", Profile: "bounded_acceptance",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: failingServer.URL}, failingServer.Client(), true)
	if firstErr == nil {
		t.Fatalf("first attempt = %#v, %v, want a Provider failure", firstResult, firstErr)
	}
	if firstResult.Session.State != interaction.StatePlanGenerationApprovalRequired || firstResult.Session.Version != 2 {
		t.Fatalf("Session after failed first attempt = %#v, want Version=2 (reservation committed, no result Turn)", firstResult.Session)
	}
	if _, reserved := firstResult.Session.PlanGenerationReservation(); !reserved {
		t.Fatal("reservation Turn must be committed even though the Provider call failed")
	}

	unreachableProvider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("a retry with a new outer Command ID must never reach the Provider once a bounded Session's Plan generation is reserved")
	}))
	defer unreachableProvider.Close()
	secondResult, secondErr := ExecuteInteractionPlanGeneration(context.Background(), InteractionPlanGenerationInput{
		VaultRoot: root, SessionID: sessionID, ExpectedVersion: firstResult.Session.Version,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-BOUNDED-RESERVE-RETRY-2",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: unreachableProvider.URL}, unreachableProvider.Client(), true)
	if secondErr == nil || secondResult.Session.State != interaction.StatePlanGenerationApprovalRequired {
		t.Fatalf("retry with a new outer Command ID = %#v, %v, want rejected without a Provider call", secondResult, secondErr)
	}
	if !errors.Is(secondErr, ErrInteractionBoundedPlanAlreadyReserved) {
		var recorded *RecordedCommandError
		if !errors.As(secondErr, &recorded) {
			t.Fatalf("retry error = %v, want ErrInteractionBoundedPlanAlreadyReserved (directly or wrapped)", secondErr)
		}
	}
}
