package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/action"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/recovery"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
	workspaceruntime "github.com/AkiraShimizu0/workcairn/go/internal/runtime"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

func TestProviderTimeoutUsesSharedPublicBetaDefaultAndKeepsCLIOverride(t *testing.T) {
	base := []string{"--vault", t.TempDir(), "--request", "依頼", "--model", "workcairn-auto"}
	options, err := parseOptions("ceo-plan-generate", base)
	if err != nil || options.timeout != workspaceruntime.DefaultProviderRequestTimeout {
		t.Fatalf("default options = %#v, %v", options, err)
	}
	overridden, err := parseOptions("ceo-plan-generate", append(base, "--timeout", "37s"))
	if err != nil || overridden.timeout != 37*time.Second {
		t.Fatalf("override options = %#v, %v", overridden, err)
	}
}

func TestPlanCommandIsReadOnlyAndNeedsNoProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	httpConstructed := false
	var output bytes.Buffer
	exitCode := run(context.Background(), append([]string{"plan"}, commandArgs(root)...), &output, commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("plan read Provider environment"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			httpConstructed = true
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || response.Version != outputVersion || httpConstructed {
		t.Fatalf("run(plan) exit=%d response=%#v http=%t", exitCode, response, httpConstructed)
	}
	encoded, _ := json.Marshal(response.Result)
	if !strings.Contains(string(encoded), `"executable":true`) || !strings.Contains(string(encoded), `"approval_required":true`) {
		t.Fatalf("plan result = %s", encoded)
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("plan command changed temporary Vault")
	}
}

func TestVersionDoesNotRequireVaultOrRuntimeDependencies(t *testing.T) {
	environmentRead, httpConstructed := false, false
	var output bytes.Buffer
	exitCode := run(context.Background(), []string{"version"}, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { environmentRead = true; return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { httpConstructed = true; return http.DefaultClient },
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || environmentRead || httpConstructed {
		t.Fatalf("version response = exit %d %#v env=%t http=%t", exitCode, response, environmentRead, httpConstructed)
	}
}

// TestInteractionStartPlanIsReadOnlyAndStartChainsIntoPlanGenerationWithApproval
// covers ADR-0049's request-submit chain from the operator CLI side:
// interaction-start-plan stays a read-only preview that never touches the
// Provider, an unapproved interaction-start still never touches the
// Provider (the approval check runs before any Provider config is
// resolved), but an approved interaction-start now itself performs Plan
// generation as a deterministic child Command -- interaction-next no longer
// shows approve_plan_generation afterward. The standalone
// interaction-plan-generate command is unchanged and still independently
// requires its own approval before touching the Provider (operator/Recovery
// parity, section 16).
func TestInteractionStartPlanIsReadOnlyAndStartChainsIntoPlanGenerationWithApproval(t *testing.T) {
	root := t.TempDir()
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	planArgs := []string{
		"interaction-start-plan", "--vault", root, "--session-id", "SESSION-CLI-001",
		"--request", "Webアプリを作りたい", "--model", "Claude Sonnet 5", "--at", "2026-08-09T12:00:00Z",
	}
	exitCode := run(context.Background(), planArgs, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("Interaction plan read Provider environment"); return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("Interaction plan constructed HTTP client"); return nil },
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Interaction plan exit=%d response=%#v", exitCode, response)
	}
	encoded, _ := json.Marshal(response.Result)
	var plan workspaceprocess.InteractionStartPlan
	if err := json.Unmarshal(encoded, &plan); err != nil || plan.Session.RequestDigest == "" {
		t.Fatalf("Interaction plan = %#v, %v", plan, err)
	}

	// Organization roster the chained Plan generation's Structured Output
	// schema needs (ADR-0048's Organization-scoped required_role enum) --
	// added only now, after the read-only preview's own no-mutation proof
	// above.
	employeeDirectory := filepath.Join(root, "社員")
	if err := os.MkdirAll(employeeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(employeeDirectory, "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	before = commandVaultSnapshot(t, root)

	unapprovedStartArgs := []string{
		"interaction-start", "--vault", root, "--session-id", "SESSION-CLI-001", "--request", "Webアプリを作りたい",
		"--request-sha256", plan.Session.RequestDigest, "--model", "Claude Sonnet 5", "--at", "2026-08-09T12:00:00Z",
		"--command-id", "CMD-INTERACTION-CLI-START",
	}
	output.Reset()
	if exit := run(context.Background(), unapprovedStartArgs, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("unapproved start read Provider environment"); return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("unapproved start constructed HTTP client"); return nil },
	}); exit != 1 || !bytes.Contains(output.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) ||
		!reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("unapproved start exit=%d response=%s", exit, output.String())
	}

	startArgs := append(unapprovedStartArgs, "--approved")
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "Webアプリ案件", "objective": "依頼から成果物を完成させる", "summary": "CLI E2E",
		"steps":         []map[string]any{{"kind": "write", "description": "要件をまとめる", "required_role": "Product Manager"}},
		"ceo_questions": []string{},
	})
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, _ *http.Request) {
		responseWriter.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(responseWriter).Encode(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()
	environment := map[string]string{"ANTHROPIC_API_KEY": "fake-key", "WORKCAIRN_CLAUDE_BASE_URL": server.URL}
	providerDependencies := commandDependencies{
		lookupEnv:     func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	output.Reset()
	if exit := run(context.Background(), startArgs, &output, providerDependencies); exit != 0 {
		t.Fatalf("Interaction start exit=%d response=%s", exit, output.String())
	}
	// ADR-0049: the CEO's "send request" approval is itself the approval for
	// Plan generation -- interaction-next must show the resulting Plan
	// approval state directly, never approve_plan_generation.
	output.Reset()
	if exit := run(context.Background(), []string{"interaction-next", "--vault", root, "--session-id", "SESSION-CLI-001"}, &output, commandDependencies{}); exit != 0 ||
		!bytes.Contains(output.Bytes(), []byte(`"kind":"approve_plan_apply"`)) || bytes.Contains(output.Bytes(), []byte(`"approve_plan_generation"`)) {
		t.Fatalf("Interaction next exit=%d response=%s", exit, output.String())
	}

	// The standalone interaction-plan-generate command is unchanged: it
	// still independently requires its own approval before touching the
	// Provider, regardless of the chained flow above.
	environmentRead, httpConstructed := false, false
	output.Reset()
	exitCode = run(context.Background(), []string{
		"interaction-plan-generate", "--vault", root, "--session-id", "SESSION-CLI-001", "--expected-version", "2",
		"--at", "2026-08-09T12:01:00Z", "--command-id", "CMD-INTERACTION-CLI-PLAN",
	}, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { environmentRead = true; return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { httpConstructed = true; return nil },
	})
	response = decodeCommandResponse(t, output.Bytes())
	if exitCode != 1 || response.Error == nil || response.Error.Code != "APPROVAL_REQUIRED" || environmentRead || httpConstructed {
		t.Fatalf("unapproved generation exit=%d response=%#v env=%t http=%t", exitCode, response, environmentRead, httpConstructed)
	}
}

func TestWorkflowPlanIsReadOnlyAndNeedsNoProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	exit := run(context.Background(), []string{"workflow-plan", "--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ"}, &output, commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("Workflow plan read Provider environment"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Workflow plan constructed HTTP client")
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exit != 0 || !response.OK || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Workflow plan exit=%d response=%#v", exit, response)
	}
}

func TestReviewedWorkflowPlanIsReadOnlyAndNeedsNoProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	writeCommandFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	exit := run(context.Background(), []string{
		"workflow-reviewed-plan", "--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--reviewer", "QA-001",
	}, &output, commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("reviewed Workflow plan read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("reviewed Workflow plan constructed HTTP client")
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	encoded, _ := json.Marshal(response.Result)
	if exit != 0 || !response.OK || !bytes.Contains(encoded, []byte(`"review_after_every_task":true`)) ||
		!reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("reviewed Workflow plan exit=%d response=%#v", exit, response)
	}
}

func TestInteractionWorkflowPlanIsReadOnlyAndExecutionNeedsApprovalBeforeProviderConfig(t *testing.T) {
	root := writeCommandVault(t)
	writeCommandFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ready := writeCommandReadyInteraction(t, root, at.Add(-time.Hour))
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	base := []string{
		"--vault", root, "--session-id", ready.SessionID, "--expected-version", "3",
		"--reviewer", "QA-001", "--max-tasks", "10", "--at", at.Format(time.RFC3339),
	}
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("Interaction Workflow plan or rejection read Provider environment")
			return "", false
		},
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Interaction Workflow plan or rejection constructed HTTP client")
			return nil
		},
	}
	if code := run(context.Background(), append([]string{"interaction-workflow-plan"}, base...), &output, dependencies); code != 0 {
		t.Fatalf("interaction-workflow-plan code=%d output=%s", code, output.String())
	}
	response := decodeCommandResponse(t, output.Bytes())
	encoded, _ := json.Marshal(response.Result)
	var plan workspaceprocess.InteractionWorkflowPlan
	if err := json.Unmarshal(encoded, &plan); err != nil || plan.WorkflowPlanDigest == "" || !plan.ApprovalRequired ||
		!reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Interaction Workflow plan = %#v, %v", plan, err)
	}
	output.Reset()
	executeArgs := append([]string{"interaction-workflow-execute"}, base...)
	executeArgs = append(executeArgs, "--workflow-sha256", plan.WorkflowPlanDigest, "--command-id", "CMD-INTERACTION-WORKFLOW-CLI")
	if code := run(context.Background(), executeArgs, &output, dependencies); code != 1 || !bytes.Contains(output.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) ||
		!reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("unapproved interaction-workflow-execute code=%d output=%s", code, output.String())
	}
	output.Reset()
	actionArgs := []string{
		"interaction-action-wordpress-publish", "--vault", root, "--session-id", ready.SessionID, "--expected-version", "3",
		"--task", "TASK-001", "--target", "site-main", "--command-id", "CMD-INTERACTION-ACTION-CLI",
		"--action-plan-sha256", "sha256:" + strings.Repeat("a", 64), "--at", at.Format(time.RFC3339),
	}
	if code := run(context.Background(), actionArgs, &output, dependencies); code != 1 || !bytes.Contains(output.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) ||
		!reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("unapproved interaction-action-wordpress-publish code=%d output=%s", code, output.String())
	}
}

func writeCommandReadyInteraction(t *testing.T, root string, at time.Time) interaction.Record {
	t.Helper()
	record, _ := interaction.New("SESSION-CLI-WORKFLOW", "ToDoアプリを完成させる", "Claude Sonnet 5", at)
	assignee := "PLAN-001"
	plan := ceoplan.Plan{
		ProjectName: "ToDoアプリ", Objective: "完成", Summary: "概要", RequiredDepartments: []string{"企画部"},
		RequiredRoles: []string{"Product Manager"}, AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{ProposalID: "PROPOSED-001", Title: "要件を整理する", AssigneeID: &assignee, DependencyIDs: []string{}, Rationale: "必要"}},
		Risks:         []string{}, CEOQuestions: []string{}, PlanOnly: true,
	}
	withPlan, _ := record.RecordPlan(plan, at.Add(time.Minute))
	_, digest, _ := withPlan.CurrentPlan()
	ready, _ := withPlan.RecordApplied("PROJECT-001", "ToDoアプリ", digest, "", at.Add(2*time.Minute))
	store, _ := vault.NewInteractionStore(root)
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), withPlan, record.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), ready, withPlan.Version); err != nil {
		t.Fatal(err)
	}
	return ready
}

func TestSchedulePlanApprovalCreateAndListUseOnlyTemporaryVault(t *testing.T) {
	root := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "scheduler", "one_shot_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition := string(fixture)
	dependencies := commandDependencies{
		now:       func() time.Time { return commandTestTime() },
		lookupEnv: func(string) (string, bool) { t.Fatal("Schedule read Provider configuration"); return "", false },
	}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if code := run(context.Background(), []string{"schedule-plan", "--vault", root, "--schedule-json", definition}, &planOutput, dependencies); code != 0 || !bytes.Contains(planOutput.Bytes(), []byte(`"executable":true`)) {
		t.Fatalf("schedule-plan code=%d output=%s", code, planOutput.String())
	}
	if !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatal("schedule-plan changed temporary Vault")
	}
	var rejected bytes.Buffer
	if code := run(context.Background(), []string{"schedule-create", "--vault", root, "--schedule-json", definition, "--command-id", "CMD-CREATE-SCHEDULE-001"}, &rejected, dependencies); code != 1 || !bytes.Contains(rejected.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) {
		t.Fatalf("unapproved schedule-create code=%d output=%s", code, rejected.String())
	}
	if !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatal("unapproved schedule-create changed temporary Vault")
	}
	var created bytes.Buffer
	if code := run(context.Background(), []string{"schedule-create", "--vault", root, "--schedule-json", definition, "--command-id", "CMD-CREATE-SCHEDULE-001", "--approved"}, &created, dependencies); code != 0 || !bytes.Contains(created.Bytes(), []byte(`"state":"pending"`)) {
		t.Fatalf("schedule-create code=%d output=%s", code, created.String())
	}
	var listed bytes.Buffer
	if code := run(context.Background(), []string{"schedule-list", "--vault", root}, &listed, dependencies); code != 0 || !bytes.Contains(listed.Bytes(), []byte(`"schedule_id":"SCHEDULE-001"`)) {
		t.Fatalf("schedule-list code=%d output=%s", code, listed.String())
	}
}

func TestWordPressActionPlanApprovalPublishAndReplayUseMockProvider(t *testing.T) {
	root := writeCommandVault(t)
	deliverableDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables")
	if err := os.MkdirAll(deliverableDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	deliverableContent := "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\nrunner: fake\nexecuted_at: 2026-08-09 10:00:00\n---\n\n# 公開記事\n\n本文\n"
	writeCommandFile(t, filepath.Join(deliverableDirectory, "TASK-001.md"), deliverableContent)
	args := []string{"--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--task", "TASK-001", "--target", "site-main", "--command-id", "CMD-ACTION-CLI-001"}
	before := commandVaultSnapshot(t, root)
	noProvider := commandDependencies{
		now:       commandTestTime,
		lookupEnv: func(string) (string, bool) { t.Fatal("Action plan or rejection read credentials"); return "", false },
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Action plan or rejection created HTTP client")
			return nil
		},
	}
	var planOutput bytes.Buffer
	if code := run(context.Background(), append([]string{"action-wordpress-plan"}, args...), &planOutput, noProvider); code != 0 || !bytes.Contains(planOutput.Bytes(), []byte(`"executable":true`)) || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Action plan code=%d output=%s", code, planOutput.String())
	}
	publishBaseArgs := append(append([]string(nil), args...), "--source-sha256", action.SourceDigest([]byte(deliverableContent)))
	var rejected bytes.Buffer
	if code := run(context.Background(), append([]string{"action-wordpress-publish"}, publishBaseArgs...), &rejected, noProvider); code != 1 || !bytes.Contains(rejected.Bytes(), []byte(`"code":"APPROVAL_REQUIRED"`)) || !reflect.DeepEqual(before, commandVaultSnapshot(t, root)) {
		t.Fatalf("Action rejection code=%d output=%s", code, rejected.String())
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":77,"link":"https://example.test/77","status":"publish"}`))
	}))
	defer server.Close()
	environment := map[string]string{
		"WORKCAIRN_WORDPRESS_BASE_URL": server.URL, "WORKCAIRN_WORDPRESS_USERNAME": "fake-user", "WORKCAIRN_WORDPRESS_APPLICATION_PASSWORD": "fake-password",
	}
	dependencies := commandDependencies{
		now: commandTestTime, lookupEnv: func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	publishArgs := append(append([]string{"action-wordpress-publish"}, publishBaseArgs...), "--approved")
	var first bytes.Buffer
	if code := run(context.Background(), publishArgs, &first, dependencies); code != 0 || !bytes.Contains(first.Bytes(), []byte(`"status":"published"`)) || calls != 1 {
		t.Fatalf("Action publish code=%d output=%s calls=%d", code, first.String(), calls)
	}
	var replay bytes.Buffer
	if code := run(context.Background(), publishArgs, &replay, dependencies); code != 0 || replay.String() != first.String() || calls != 1 {
		t.Fatalf("Action replay code=%d output=%s calls=%d", code, replay.String(), calls)
	}
}

func TestOrganizationCommandsNeedNoClockProviderOrEffects(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("Organization command read Provider environment")
			return "", false
		},
		now: func() time.Time { t.Fatal("Organization command read clock"); return time.Time{} },
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("Organization command constructed HTTP client")
			return nil
		},
	}
	for _, args := range [][]string{
		{"organization-inspect", "--vault", root},
		{"identity-validate", "--vault", root, "--name", "佐藤 蓮"},
	} {
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("run(%s) failed: %s", args[0], output.String())
		}
		response := decodeCommandResponse(t, output.Bytes())
		if !response.OK || response.Error != nil {
			t.Fatalf("response = %#v", response)
		}
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("Organization commands changed temporary Vault")
	}
}

func TestRecoveryCommandsInspectPlanAndApplyTemporaryVaultWithoutProvider(t *testing.T) {
	root := writeCommandVault(t)
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(context.Background(), "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	started, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), started, current.Version); err != nil {
		t.Fatal(err)
	}
	deliverablePath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(deliverablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, deliverablePath, "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\n---\n\n# immutable result\n")

	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("recovery read Provider environment"); return "", false },
		now:       func() time.Time { t.Fatal("recovery read clock"); return time.Time{} },
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("recovery constructed HTTP client")
			return nil
		},
	}
	base := []string{"--vault", root, "--project", "ToDoアプリ"}
	before := commandVaultSnapshot(t, root)
	var inspectOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"recovery-inspect"}, base...), &inspectOutput, dependencies); exit != 0 {
		t.Fatalf("recovery-inspect failed: %s", inspectOutput.String())
	}
	inspect := decodeCommandResponse(t, inspectOutput.Bytes())
	encoded, _ := json.Marshal(inspect.Result)
	if !strings.Contains(string(encoded), string(recovery.FindingTaskCompletionPending)) {
		t.Fatalf("recovery inspection = %s", encoded)
	}

	var planOutput bytes.Buffer
	planArgs := append([]string{"recovery-plan"}, base...)
	planArgs = append(planArgs, "--task", "TASK-001", "--action", string(recovery.ActionCompleteTask))
	if exit := run(context.Background(), planArgs, &planOutput, dependencies); exit != 0 {
		t.Fatalf("recovery-plan failed: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("recovery inspect/plan changed the temporary Vault")
	}
	planPath := filepath.Join(t.TempDir(), "recovery-plan.json")
	writeCommandFile(t, planPath, planOutput.String())

	var applyOutput bytes.Buffer
	applyArgs := append([]string{"recovery-apply"}, base...)
	applyArgs = append(applyArgs, "--plan-file", planPath, "--approved")
	if exit := run(context.Background(), applyArgs, &applyOutput, dependencies); exit != 0 {
		t.Fatalf("recovery-apply failed: %s", applyOutput.String())
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != started.Version+1 {
		t.Fatalf("recovered Task = %#v, %v", stored, err)
	}
}

func TestProjectBootstrapAndTaskCreationUseGoManagedPathWithoutProvider(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("creation command read Provider environment"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("creation command constructed HTTP client")
			return nil
		},
	}
	projectArgs := []string{"--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--description", "シンプルなToDoアプリ"}
	for _, operation := range []string{"project-bootstrap-plan", "project-bootstrap-execute"} {
		args := append([]string{operation}, projectArgs...)
		if operation == "project-bootstrap-execute" {
			args = append(args, "--approved")
		}
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("%s failed: %s", operation, output.String())
		}
	}
	taskArgs := []string{"--vault", root, "--project", "ToDoアプリ", "--title", "要件を整理する", "--assignee", "PLAN-001"}
	for _, operation := range []string{"task-create-plan", "task-create-execute"} {
		args := append([]string{operation}, taskArgs...)
		if operation == "task-create-execute" {
			args = append(args, "--approved")
		}
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("%s failed: %s", operation, output.String())
		}
	}
	dependencyJSON := `{"task_id":"TASK-001","proposal_id":"PROPOSED-001","depends_on":[],"rationale":"最初のTask"}`
	for _, operation := range []string{"project-dependencies-plan", "project-dependencies-create"} {
		args := []string{operation, "--vault", root, "--project", "ToDoアプリ", "--dependency-json", dependencyJSON}
		if operation == "project-dependencies-create" {
			args = append(args, "--approved")
		}
		var output bytes.Buffer
		if exit := run(context.Background(), args, &output, dependencies); exit != 0 {
			t.Fatalf("%s failed: %s", operation, output.String())
		}
	}
	content, err := os.ReadFile(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md"))
	if err != nil || !strings.Contains(string(content), "workspace-os-task-metadata:v1") || !strings.Contains(string(content), "TASK-001") {
		t.Fatalf("managed Tasks.md = %s, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Task Dependencies.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeHirePlanAndExecuteNeedNoProvider(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeCommandFile(t, filepath.Join(root, "会社", "Workspace State.md"), "---\nupdated_at: 2026-08-01 10:00\n---\n\n## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n\n## 部署\n\n| 部署 | 社員数 | 状態 |\n|---|---:|---|\n")
	dependencies := commandDependencies{lookupEnv: func(string) (string, bool) { t.Fatal("hire read Provider environment"); return "", false }, now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("hire created HTTP client"); return nil }}
	base := []string{"--vault", root, "--employee-id", "DEV-001", "--name", "佐藤 蓮", "--department", "開発部", "--role", "Engineer", "--model", "Claude Sonnet 5"}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"employee-hire-plan"}, base...), &planOutput, dependencies); exit != 0 {
		t.Fatalf("hire plan: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("hire plan changed Vault")
	}
	var executeOutput bytes.Buffer
	args := append(append([]string{"employee-hire-execute"}, base...), "--approved")
	if exit := run(context.Background(), args, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("hire execute: %s", executeOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "佐藤 蓮.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeRenamePlanAndExecuteNeedNoProvider(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\nname: 田中 美咲\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n# 田中 美咲\n\n- 氏名: 田中 美咲\n")
	writeCommandFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| PLAN-001 | 田中 美咲 | Product Manager | 待機中 | なし |\n\n## 部署\n")
	dependencies := commandDependencies{lookupEnv: func(string) (string, bool) { t.Fatal("rename read Provider environment"); return "", false }, now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("rename created HTTP client"); return nil }}
	base := []string{"--vault", root, "--employee-id", "PLAN-001", "--old-name", "田中 美咲", "--new-name", "山本 真帆", "--reason", "類似名の解消"}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"employee-rename-plan"}, base...), &planOutput, dependencies); exit != 0 {
		t.Fatalf("rename plan: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("rename plan changed Vault")
	}
	var executeOutput bytes.Buffer
	args := append(append([]string{"employee-rename-execute"}, base...), "--approved")
	if exit := run(context.Background(), args, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("rename execute: %s", executeOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "社員", "山本 真帆.md")); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeIDRepairPlanAndExecuteNeedNoProvider(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "会社"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	employee := func(id string) string {
		return "---\nid: " + id + "\ndepartment: 開発部\nrole: Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n\n- ID: " + id + "\n"
	}
	writeCommandFile(t, filepath.Join(root, "社員", "佐藤 蓮.md"), employee("DEV-002"))
	writeCommandFile(t, filepath.Join(root, "社員", "鈴木 陽菜.md"), employee("DEV-002"))
	writeCommandFile(t, filepath.Join(root, "会社", "Workspace State.md"), "## Workspace Manager\n\n| ID | 氏名 | 役割 | 状態 | 現在の作業 |\n|---|---|---|---|---|\n| DEV-002 | 佐藤 蓮 | Engineer | 待機中 | なし |\n| DEV-002 | 鈴木 陽菜 | Engineer | 待機中 | なし |\n\n## 部署\n")
	dependencies := commandDependencies{lookupEnv: func(string) (string, bool) { t.Fatal("ID repair read Provider environment"); return "", false }, now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("ID repair created HTTP client"); return nil }}
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), []string{"employee-id-repair-plan", "--vault", root}, &planOutput, dependencies); exit != 0 {
		t.Fatalf("ID repair plan: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("ID repair plan changed Vault")
	}
	repairJSON := `{"name":"鈴木 陽菜","current_id":"DEV-002","proposed_id":"DEV-003"}`
	var executeOutput bytes.Buffer
	args := []string{"employee-id-repair-execute", "--vault", root, "--repair-json", repairJSON, "--approved"}
	if exit := run(context.Background(), args, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("ID repair execute: %s", executeOutput.String())
	}
	content, _ := os.ReadFile(filepath.Join(root, "社員", "鈴木 陽菜.md"))
	if !strings.Contains(string(content), "id: DEV-003") {
		t.Fatalf("Employee ID not repaired: %s", content)
	}
}

func TestCEOPlanGenerateAndApplyUseGoOnlyProductPath(t *testing.T) {
	fixtureContent, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ceo", "plan_generation_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Request      string          `json:"request"`
		RunnerOutput json.RawMessage `json:"runner_output"`
		ExpectedPlan ceoplan.Plan    `json:"expected_plan"`
		Employees    []struct {
			ID, Name, Department, Role, Model string
		} `json:"employees"`
	}
	if err := json.Unmarshal(fixtureContent, &fixture); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, employee := range fixture.Employees {
		writeCommandFile(t, filepath.Join(root, "社員", employee.Name+".md"), "---\nid: "+employee.ID+"\ndepartment: "+employee.Department+"\nrole: "+employee.Role+"\nmodel: "+employee.Model+"\nstatus: 待機中\n---\n")
	}
	before := commandVaultSnapshot(t, root)
	var unapproved bytes.Buffer
	unapprovedDependencies := commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("unapproved CEO plan read Provider config"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("unapproved CEO plan created HTTP client"); return nil },
	}
	args := []string{"ceo-plan-generate", "--vault", root, "--request", fixture.Request, "--model", "Claude Sonnet 5"}
	if exit := run(context.Background(), args, &unapproved, unapprovedDependencies); exit != 1 {
		t.Fatalf("unapproved exit=%d response=%s", exit, unapproved.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved CEO generation changed Vault")
	}
	// Intent-shaped mock Provider output: both steps resolve uniquely against
	// fixture.Employees (Product Manager -> PLAN-001, Backend Engineer ->
	// DEV-001). NormalizeIntent now safely rejects rather than silently
	// leaving unassigned a required_role with zero matching employees, so
	// this E2E no longer reuses the historical canonical fixture's second
	// task ("UI/UX Designer", intentionally unfilled) -- that fixture's own
	// contract is still exercised directly by ceoplan's own fixture test.
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": fixture.ExpectedPlan.ProjectName, "objective": fixture.ExpectedPlan.Objective,
		"summary": fixture.ExpectedPlan.Summary,
		"steps": []map[string]any{
			{"kind": "write", "description": "MVP要件を整理する", "required_role": "Product Manager"},
			{"kind": "implement", "description": "収支登録画面を実装する", "required_role": "Backend Engineer"},
		},
		"ceo_questions": []string{},
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()
	environment := map[string]string{"ANTHROPIC_API_KEY": "fake-key", "WORKCAIRN_CLAUDE_BASE_URL": server.URL}
	dependencies := commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		now:       commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	var generatedOutput bytes.Buffer
	if exit := run(context.Background(), append(args, "--approved"), &generatedOutput, dependencies); exit != 0 {
		t.Fatalf("generate response=%s", generatedOutput.String())
	}
	generated := decodeCommandResponse(t, generatedOutput.Bytes())
	encodedResult, _ := json.Marshal(generated.Result)
	var generationResult service.CEOPlanResult
	if err := json.Unmarshal(encodedResult, &generationResult); err != nil ||
		generationResult.Plan.ProjectName != fixture.ExpectedPlan.ProjectName ||
		len(generationResult.Plan.ProposedTasks) != 2 || len(generationResult.Plan.MissingRoles) != 0 {
		t.Fatalf("generation result=%s err=%v", encodedResult, err)
	}
	planJSON, _ := json.Marshal(generationResult.Plan)
	applyArgs := []string{"--vault", root, "--project-id", "PROJECT-001", "--plan-json", string(planJSON)}
	var applyPlanOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"ceo-plan-apply-plan"}, applyArgs...), &applyPlanOutput, unapprovedDependencies); exit != 0 {
		t.Fatalf("apply plan response=%s", applyPlanOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("CEO apply plan changed Vault")
	}
	var applyOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"ceo-plan-apply"}, applyArgs...), "--approved"), &applyOutput, unapprovedDependencies); exit != 0 {
		t.Fatalf("apply response=%s", applyOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", fixture.ExpectedPlan.ProjectName, "Task Dependencies.md")); err != nil {
		t.Fatal(err)
	}
}

// TestResponsibilityPlanCLIResolvesContextAndReusesGenerateCEOPlan exercises
// the new responsibility-plan operation end to end through the real CLI
// dispatcher: goal-create -> responsibility-create wire up standing Company
// OS state exactly as an Operator would, then responsibility-plan resolves
// that Responsibility's context and hands off to the same Go-only Planning
// product path ceo-plan-generate itself uses -- confirming the CLI wiring
// (flags, required-field validation, approval gate, error mapping) added
// this Checkpoint, not just the underlying process.GenerateResponsibilityPlan
// function already covered by internal/process's own unit tests.
func TestResponsibilityPlanCLIResolvesContextAndReusesGenerateCEOPlan(t *testing.T) {
	fixtureContent, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ceo", "plan_generation_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Employees []struct{ ID, Name, Department, Role, Model string } `json:"employees"`
	}
	if err := json.Unmarshal(fixtureContent, &fixture); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, employee := range fixture.Employees {
		writeCommandFile(t, filepath.Join(root, "社員", employee.Name+".md"), "---\nid: "+employee.ID+"\ndepartment: "+employee.Department+"\nrole: "+employee.Role+"\nmodel: "+employee.Model+"\nstatus: 待機中\n---\n")
	}
	noSecretsDependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("read-only/unapproved step read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("read-only/unapproved step created HTTP client")
			return nil
		},
	}
	var goalOutput bytes.Buffer
	if exit := run(context.Background(), []string{
		"goal-create", "--vault", root, "--goal-id", "GOAL-1", "--goal-scope", "company",
		"--goal-title", "オンボーディングを継続的に改善する", "--goal-outcome", "完了率80%",
		"--command-id", "CMD-GOAL-1", "--approved",
	}, &goalOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("goal-create response=%s", goalOutput.String())
	}
	var responsibilityOutput bytes.Buffer
	if exit := run(context.Background(), []string{
		"responsibility-create", "--vault", root, "--responsibility-id", "RESP-1", "--responsibility-scope", "company",
		"--responsibility-title", "オンボーディング品質を継続的に改善する", "--goal-ref", "GOAL-1",
		"--command-id", "CMD-RESP-1", "--approved",
	}, &responsibilityOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("responsibility-create response=%s", responsibilityOutput.String())
	}
	planArgs := []string{
		"responsibility-plan", "--vault", root, "--responsibility-id", "RESP-1", "--responsibility-scope", "company",
		"--instruction", "今週改善すべき項目を調査して実装計画を作る", "--model", "Claude Sonnet 5",
	}
	before := commandVaultSnapshot(t, root)
	var unapprovedOutput bytes.Buffer
	if exit := run(context.Background(), planArgs, &unapprovedOutput, noSecretsDependencies); exit != 1 {
		t.Fatalf("unapproved responsibility-plan exit=%d response=%s", exit, unapprovedOutput.String())
	}
	unapprovedResponse := decodeCommandResponse(t, unapprovedOutput.Bytes())
	if unapprovedResponse.OK || unapprovedResponse.Error == nil || unapprovedResponse.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("unapproved responsibility-plan response=%#v", unapprovedResponse)
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved responsibility-plan changed the Vault")
	}
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "オンボーディング改善", "objective": "新規ユーザーのオンボーディング体験を改善する",
		"summary": "今週の改善項目を調査し実装計画を作る",
		"steps": []map[string]any{
			{"kind": "write", "description": "MVP要件を整理する", "required_role": "Product Manager"},
			{"kind": "implement", "description": "収支登録画面を実装する", "required_role": "Backend Engineer"},
		},
		"ceo_questions": []string{},
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()
	environment := map[string]string{"ANTHROPIC_API_KEY": "fake-key", "WORKCAIRN_CLAUDE_BASE_URL": server.URL}
	dependencies := commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		now:       commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	var approvedOutput bytes.Buffer
	if exit := run(context.Background(), append(planArgs, "--approved"), &approvedOutput, dependencies); exit != 0 {
		t.Fatalf("approved responsibility-plan response=%s", approvedOutput.String())
	}
	approvedResponse := decodeCommandResponse(t, approvedOutput.Bytes())
	var result workspaceprocess.ResponsibilityPlanningResult
	encodedResult, _ := json.Marshal(approvedResponse.Result)
	if err := json.Unmarshal(encodedResult, &result); err != nil ||
		result.ResponsibilityID != "RESP-1" || len(result.GoalRefs) != 1 || result.GoalRefs[0] != "GOAL-1" ||
		result.BoundEmployeeID != "" || result.Generation.Plan.ProjectName != "オンボーディング改善" ||
		len(result.Generation.Plan.ProposedTasks) != 2 {
		t.Fatalf("approved responsibility-plan result=%s err=%v", encodedResult, err)
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("approved responsibility-plan (Plan generation only) changed the Vault")
	}
}

// TestRoutineCLILifecycleCreatesActivatesAndRunsNow exercises the new
// routine-* operations end to end through the real CLI dispatcher: a
// Routine is created against a real Responsibility, activated (which must
// create a next-occurrence Schedule, verified via schedule-list), run
// manually via routine-run-now against a mock Provider (proving it reuses
// the exact same Go-only Planning product path responsibility-plan uses),
// then deactivated.
func TestRoutineCLILifecycleCreatesActivatesAndRunsNow(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	noSecretsDependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("read-only/unapproved step read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("read-only/unapproved step created HTTP client")
			return nil
		},
	}
	var responsibilityOutput bytes.Buffer
	if exit := run(context.Background(), []string{
		"responsibility-create", "--vault", root, "--responsibility-id", "RESP-1", "--responsibility-scope", "company",
		"--responsibility-title", "オンボーディング品質を継続的に改善する", "--command-id", "CMD-RESP-1", "--approved",
	}, &responsibilityOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("responsibility-create response=%s", responsibilityOutput.String())
	}

	var unapprovedOutput bytes.Buffer
	createArgs := []string{
		"routine-create", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
		"--responsibility-id", "RESP-1", "--instruction", "今週の改善項目を計画する", "--model", "Claude Sonnet 5",
		"--cadence", "weekly", "--weekday", "1", "--time-of-day", "09:00", "--command-id", "CMD-ROUTINE-1",
	}
	if exit := run(context.Background(), createArgs, &unapprovedOutput, noSecretsDependencies); exit != 1 {
		t.Fatalf("unapproved routine-create exit=%d response=%s", exit, unapprovedOutput.String())
	}
	unapprovedResponse := decodeCommandResponse(t, unapprovedOutput.Bytes())
	if unapprovedResponse.OK || unapprovedResponse.Error == nil || unapprovedResponse.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("unapproved routine-create response=%#v", unapprovedResponse)
	}

	var createOutput bytes.Buffer
	if exit := run(context.Background(), append(createArgs, "--approved"), &createOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-create response=%s", createOutput.String())
	}
	createResponse := decodeCommandResponse(t, createOutput.Bytes())
	encodedCreated, _ := json.Marshal(createResponse.Result)
	var createdRoutine routine.Record
	if err := json.Unmarshal(encodedCreated, &createdRoutine); err != nil || createdRoutine.Status != routine.StatusInactive || createdRoutine.Version != 1 {
		t.Fatalf("routine-create result=%s err=%v", encodedCreated, err)
	}

	var activateOutput bytes.Buffer
	activateArgs := []string{
		"routine-activate", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
		"--expected-version", "1", "--command-id", "CMD-ACTIVATE-1", "--at", "2026-08-26T12:00:00Z", "--approved",
	}
	if exit := run(context.Background(), activateArgs, &activateOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-activate response=%s", activateOutput.String())
	}
	activateResponse := decodeCommandResponse(t, activateOutput.Bytes())
	encodedActivate, _ := json.Marshal(activateResponse.Result)
	var activated workspaceprocess.RoutineActivateResult
	if err := json.Unmarshal(encodedActivate, &activated); err != nil || activated.Routine.Status != routine.StatusActive || activated.NextScheduleID == "" {
		t.Fatalf("routine-activate result=%s err=%v", encodedActivate, err)
	}

	var listOutput bytes.Buffer
	if exit := run(context.Background(), []string{"schedule-list", "--vault", root}, &listOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("schedule-list response=%s", listOutput.String())
	}
	if !strings.Contains(listOutput.String(), activated.NextScheduleID) {
		t.Fatalf("schedule-list does not contain the Routine's next occurrence %s: %s", activated.NextScheduleID, listOutput.String())
	}

	var showOutput bytes.Buffer
	if exit := run(context.Background(), []string{
		"routine-show", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
	}, &showOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-show response=%s", showOutput.String())
	}
	if !strings.Contains(showOutput.String(), `"schedule_healthy":true`) {
		t.Fatalf("routine-show does not report schedule_healthy:true after a successful activation: %s", showOutput.String())
	}

	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "オンボーディング改善", "objective": "新規ユーザーのオンボーディング体験を改善する",
		"summary": "今週の改善項目を計画する",
		"steps": []map[string]any{
			{"kind": "write", "description": "改善候補を整理する", "required_role": "Product Manager"},
		},
		"ceo_questions": []string{},
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()
	environment := map[string]string{"ANTHROPIC_API_KEY": "fake-key", "WORKCAIRN_CLAUDE_BASE_URL": server.URL}
	providerDependencies := commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, ok := environment[key]; return value, ok },
		now:       commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	var runNowOutput bytes.Buffer
	if exit := run(context.Background(), []string{
		"routine-run-now", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company", "--approved",
	}, &runNowOutput, providerDependencies); exit != 0 {
		t.Fatalf("routine-run-now response=%s", runNowOutput.String())
	}
	runNowResponse := decodeCommandResponse(t, runNowOutput.Bytes())
	encodedRunNow, _ := json.Marshal(runNowResponse.Result)
	var runNowResult workspaceprocess.ResponsibilityPlanningResult
	if err := json.Unmarshal(encodedRunNow, &runNowResult); err != nil || runNowResult.ResponsibilityID != "RESP-1" ||
		runNowResult.Generation.Plan.ProjectName != "オンボーディング改善" {
		t.Fatalf("routine-run-now result=%s err=%v", encodedRunNow, err)
	}

	var deactivateOutput bytes.Buffer
	if exit := run(context.Background(), []string{
		"routine-deactivate", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
		"--expected-version", "2", "--command-id", "CMD-DEACTIVATE-1", "--approved",
	}, &deactivateOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-deactivate response=%s", deactivateOutput.String())
	}
	deactivateResponse := decodeCommandResponse(t, deactivateOutput.Bytes())
	encodedDeactivate, _ := json.Marshal(deactivateResponse.Result)
	var deactivated routine.Record
	if err := json.Unmarshal(encodedDeactivate, &deactivated); err != nil || deactivated.Status != routine.StatusInactive {
		t.Fatalf("routine-deactivate result=%s err=%v", encodedDeactivate, err)
	}
}

// TestRoutineReconcileCLIRepairsMissingSchedule drives the PHASE U-5
// reliability primitive through the real CLI: routine-reconcile requires
// approval, reports schedule_healthy:false via routine-show while an
// Active Routine has no future occurrence, repairs it, and then reports
// healthy again -- all without ever touching the Provider.
func TestRoutineReconcileCLIRepairsMissingSchedule(t *testing.T) {
	root := t.TempDir()
	noSecretsDependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("read-only/unapproved step read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("read-only/unapproved step created HTTP client")
			return nil
		},
	}
	if exit := run(context.Background(), []string{
		"responsibility-create", "--vault", root, "--responsibility-id", "RESP-1", "--responsibility-scope", "company",
		"--responsibility-title", "オンボーディング品質を継続的に改善する", "--command-id", "CMD-RESP-1", "--approved",
	}, new(bytes.Buffer), noSecretsDependencies); exit != 0 {
		t.Fatal("responsibility-create failed")
	}
	if exit := run(context.Background(), []string{
		"routine-create", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
		"--responsibility-id", "RESP-1", "--instruction", "今週の改善項目を計画する", "--model", "Claude Sonnet 5",
		"--cadence", "weekly", "--weekday", "1", "--time-of-day", "09:00", "--command-id", "CMD-ROUTINE-1", "--approved",
	}, new(bytes.Buffer), noSecretsDependencies); exit != 0 {
		t.Fatal("routine-create failed")
	}
	// Simulate the exact durable state PHASE U-5 hardens against: the
	// Routine's own Active transition committed with no Schedule ever
	// created for it. workspaceprocess is used directly here (rather than
	// forcing a real Schedule-creation failure through the CLI) purely to
	// construct that state economically.
	store, err := vault.NewWorkspaceRoutineStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), "ROUTINE-1")
	if err != nil {
		t.Fatal(err)
	}
	active, err := record.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), active, record.Version); err != nil {
		t.Fatal(err)
	}

	var showBefore bytes.Buffer
	if exit := run(context.Background(), []string{
		"routine-show", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
	}, &showBefore, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-show response=%s", showBefore.String())
	}
	if !strings.Contains(showBefore.String(), `"schedule_healthy":false`) {
		t.Fatalf("routine-show does not report schedule_healthy:false for an Active Routine missing its occurrence: %s", showBefore.String())
	}

	var unapprovedReconcile bytes.Buffer
	reconcileArgs := []string{
		"routine-reconcile", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
		"--command-id", "CMD-RECONCILE-1", "--at", "2026-08-26T12:00:00Z",
	}
	if exit := run(context.Background(), reconcileArgs, &unapprovedReconcile, noSecretsDependencies); exit != 1 {
		t.Fatalf("unapproved routine-reconcile exit=%d response=%s", exit, unapprovedReconcile.String())
	}
	unapprovedResponse := decodeCommandResponse(t, unapprovedReconcile.Bytes())
	if unapprovedResponse.OK || unapprovedResponse.Error == nil || unapprovedResponse.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("unapproved routine-reconcile response=%#v", unapprovedResponse)
	}

	var reconcileOutput bytes.Buffer
	if exit := run(context.Background(), append(reconcileArgs, "--approved"), &reconcileOutput, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-reconcile response=%s", reconcileOutput.String())
	}
	reconcileResponse := decodeCommandResponse(t, reconcileOutput.Bytes())
	encodedReconcile, _ := json.Marshal(reconcileResponse.Result)
	var reconciled workspaceprocess.RoutineReconcileResult
	if err := json.Unmarshal(encodedReconcile, &reconciled); err != nil || reconciled.AlreadyHealthy || reconciled.ScheduleID == "" {
		t.Fatalf("routine-reconcile result=%s err=%v", encodedReconcile, err)
	}

	var showAfter bytes.Buffer
	if exit := run(context.Background(), []string{
		"routine-show", "--vault", root, "--routine-id", "ROUTINE-1", "--routine-scope", "company",
	}, &showAfter, noSecretsDependencies); exit != 0 {
		t.Fatalf("routine-show response=%s", showAfter.String())
	}
	if !strings.Contains(showAfter.String(), `"schedule_healthy":true`) {
		t.Fatalf("routine-show does not report schedule_healthy:true after reconciliation: %s", showAfter.String())
	}
}

func TestExecuteCommandRequiresFlagBeforeSecretsOrEffects(t *testing.T) {
	root := writeCommandVault(t)
	before := commandVaultSnapshot(t, root)
	var output bytes.Buffer
	exitCode := run(context.Background(), append([]string{"execute"}, commandArgs(root)...), &output, commandDependencies{
		lookupEnv: func(string) (string, bool) { t.Fatal("unapproved command read a secret"); return "", false },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("unapproved command constructed HTTP client")
			return nil
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 1 || response.OK || response.Error == nil || response.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("run(execute) exit=%d response=%#v", exitCode, response)
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved command changed temporary Vault")
	}
}

func TestEveryMutatingCommandRequiresApprovalBeforeIO(t *testing.T) {
	missingVault := filepath.Join(t.TempDir(), "missing-vault")
	missingPlan := filepath.Join(t.TempDir(), "missing-plan.json")
	tests := []struct {
		name, expectedCode string
		args               []string
	}{
		{"migration", "MIGRATION_APPROVAL_REQUIRED", []string{"migrate-apply", "--vault", missingVault, "--project", "P", "--plan-file", missingPlan}},
		{"recovery", "RECOVERY_APPROVAL_REQUIRED", []string{"recovery-apply", "--vault", missingVault, "--project", "P", "--plan-file", missingPlan}},
		{"task execution", "APPROVAL_REQUIRED", []string{"execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--task", "TASK-001", "--command-id", "CMD-UNAPPROVED-001"}},
		{"review", "APPROVAL_REQUIRED", []string{"review-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--task", "TASK-001", "--reviewer", "REV-001"}},
		{"revision", "APPROVAL_REQUIRED", []string{"revision-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--task", "TASK-001"}},
		{"workflow", "APPROVAL_REQUIRED", []string{"workflow-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--command-id", "CMD-WORKFLOW-001"}},
		{"reviewed workflow", "APPROVAL_REQUIRED", []string{"workflow-reviewed-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P", "--reviewer", "QA-001", "--command-id", "CMD-REVIEWED-WORKFLOW-001"}},
		{"organization sync", "APPROVAL_REQUIRED", []string{"organization-sync-execute", "--vault", missingVault}},
		{"employee hire", "APPROVAL_REQUIRED", []string{"employee-hire-execute", "--vault", missingVault, "--employee-id", "DEV-001", "--name", "佐藤 蓮", "--department", "開発部", "--role", "Engineer", "--model", "Claude Sonnet 5"}},
		{"employee rename", "APPROVAL_REQUIRED", []string{"employee-rename-execute", "--vault", missingVault, "--employee-id", "DEV-001", "--old-name", "佐藤 蓮", "--new-name", "鈴木 陽菜", "--reason", "類似名の解消"}},
		{"employee ID repair", "APPROVAL_REQUIRED", []string{"employee-id-repair-execute", "--vault", missingVault, "--repair-json", `{"name":"佐藤 蓮","current_id":"DEV-001","proposed_id":"DEV-002"}`}},
		{"project bootstrap", "APPROVAL_REQUIRED", []string{"project-bootstrap-execute", "--vault", missingVault, "--project-id", "PROJECT-001", "--project", "P"}},
		{"task create", "APPROVAL_REQUIRED", []string{"task-create-execute", "--vault", missingVault, "--project", "P", "--title", "Task"}},
		{"task dependencies", "APPROVAL_REQUIRED", []string{"project-dependencies-create", "--vault", missingVault, "--project", "P", "--dependency-json", `{"task_id":"TASK-001","proposal_id":"PROPOSED-001","depends_on":[],"rationale":"first"}`}},
		{"CEO plan generation", "APPROVAL_REQUIRED", []string{"ceo-plan-generate", "--vault", missingVault, "--request", "Projectを作る", "--model", "Claude Sonnet 5"}},
		{"CEO plan apply", "APPROVAL_REQUIRED", []string{"ceo-plan-apply", "--vault", missingVault, "--project-id", "PROJECT-001", "--plan-json", `{}`}},
	}
	dependencies := commandDependencies{
		lookupEnv: func(string) (string, bool) {
			t.Fatal("unapproved command read Provider environment")
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			t.Fatal("unapproved command constructed HTTP client")
			return nil
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			exit := run(context.Background(), test.args, &output, dependencies)
			response := decodeCommandResponse(t, output.Bytes())
			if exit != 1 || response.OK || response.Error == nil || response.Error.Code != test.expectedCode {
				t.Fatalf("exit=%d response=%#v", exit, response)
			}
			if _, err := os.Stat(missingVault); !os.IsNotExist(err) {
				t.Fatalf("unapproved command touched Vault path: %v", err)
			}
		})
	}
}

func TestExecuteCommandUsesMockProviderAndTemporaryVault(t *testing.T) {
	root := writeCommandVault(t)
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls++
		if request.Header.Get("x-api-key") != "fake-api-key" || request.URL.Path != "/v1/messages" {
			t.Error("unexpected mock Provider request")
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
            "model":"claude-sonnet-5",
            "content":[{"type":"text","text":"# 成果物\n\n本文"}],
            "usage":{"input_tokens":10,"output_tokens":5}
        }`))
	}))
	defer server.Close()
	environment := map[string]string{
		"ANTHROPIC_API_KEY":         "fake-api-key",
		"WORKCAIRN_CLAUDE_BASE_URL": server.URL,
	}
	args := append([]string{"execute"}, commandArgs(root)...)
	args = append(args, "--approved", "--approval-reference", "human-approval-001", "--execution-id", "EXEC-001", "--command-id", "CMD-CLI-001")
	var output bytes.Buffer
	exitCode := run(context.Background(), args, &output, commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, found := environment[key]; return value, found },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			return server.Client()
		},
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exitCode != 0 || !response.OK || response.Error != nil {
		t.Fatalf("run(execute) exit=%d response=%#v output=%s", exitCode, response, output.String())
	}
	var replayOutput bytes.Buffer
	if replayExit := run(context.Background(), args, &replayOutput, commandDependencies{
		lookupEnv: func(key string) (string, bool) { value, found := environment[key]; return value, found },
		now:       commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			return server.Client()
		},
	}); replayExit != 0 || providerCalls != 1 {
		t.Fatalf("idempotent CLI replay exit=%d providerCalls=%d output=%s", replayExit, providerCalls, replayOutput.String())
	}
	for _, path := range []string{
		filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md"),
		filepath.Join(root, "プロジェクト", "ToDoアプリ", "Audit Log.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing execution output %s: %v", path, err)
		}
	}
}

func TestExecuteCommandNeverWritesSecretToFailureResponse(t *testing.T) {
	root := writeCommandVault(t)
	const secret = "secret-must-not-appear"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	var output bytes.Buffer
	exitCode := run(context.Background(), append(append([]string{"execute"}, commandArgs(root)...), "--approved"), &output, commandDependencies{
		lookupEnv: func(key string) (string, bool) {
			if key == "ANTHROPIC_API_KEY" {
				return secret, true
			}
			if key == "WORKCAIRN_CLAUDE_BASE_URL" {
				return server.URL, true
			}
			return "", false
		},
		now: commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer {
			return server.Client()
		},
	})
	if exitCode != 1 || strings.Contains(output.String(), secret) {
		t.Fatalf("unsafe failure response exit=%d output=%s", exitCode, output.String())
	}
	response := decodeCommandResponse(t, output.Bytes())
	if response.Error == nil || response.Error.Code != "WORKER_FAILED" || response.Error.Stage != "worker" {
		t.Fatalf("response = %#v", response)
	}
}

func TestReviewCommandsPlanWithoutSecretsAndExecuteWithMockProvider(t *testing.T) {
	root := writeCommandVault(t)
	writeCommandFile(t, filepath.Join(root, "社員", "伊藤 健太.md"), "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		text := `# 成果物\n\n本文`
		if strings.Contains(string(content), "レビュー方針") {
			encoded, err := json.Marshal(map[string]any{
				"verdict": "Approve", "issues": []any{}, "summary": "問題ありません。",
			})
			if err != nil {
				t.Fatal(err)
			}
			text = string(encoded)
		}
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-sonnet-5", "content": []map[string]string{{"type": "text", "text": text}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 5},
		})
	}))
	defer server.Close()
	environment := map[string]string{
		"ANTHROPIC_API_KEY": "fake-api-key", "WORKCAIRN_CLAUDE_BASE_URL": server.URL,
	}
	dependencies := commandDependencies{
		lookupEnv:     func(key string) (string, bool) { value, found := environment[key]; return value, found },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}
	var executeOutput bytes.Buffer
	executeArgs := append(append([]string{"execute"}, commandArgs(root)...), "--approved")
	if exit := run(context.Background(), executeArgs, &executeOutput, dependencies); exit != 0 {
		t.Fatalf("Task execute failed: %s", executeOutput.String())
	}

	reviewArgs := append(commandArgs(root), "--reviewer", "QA-001")
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	planDependencies := commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("review-plan read Provider environment"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("review-plan constructed HTTP client"); return nil },
	}
	if exit := run(context.Background(), append([]string{"review-plan"}, reviewArgs...), &planOutput, planDependencies); exit != 0 {
		t.Fatalf("review-plan failed: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("review-plan changed temporary Vault")
	}

	var reviewOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"review-execute"}, reviewArgs...), "--approved"), &reviewOutput, dependencies); exit != 0 {
		t.Fatalf("review-execute failed: %s", reviewOutput.String())
	}
	response := decodeCommandResponse(t, reviewOutput.Bytes())
	if !response.OK || response.Error != nil {
		t.Fatalf("review response = %#v", response)
	}
	for _, name := range []string{"TASK-001.review.json", "TASK-001.review.md"} {
		if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews", name)); err != nil {
			t.Fatalf("Review artifact missing: %v", err)
		}
	}
}

func TestReviewExecuteRequiresApprovalBeforeSecrets(t *testing.T) {
	root := writeCommandVault(t)
	args := append(append([]string{"review-execute"}, commandArgs(root)...), "--reviewer", "QA-001")
	var output bytes.Buffer
	exit := run(context.Background(), args, &output, commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("unapproved Review read a secret"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("unapproved Review constructed HTTP client"); return nil },
	})
	response := decodeCommandResponse(t, output.Bytes())
	if exit != 1 || response.Error == nil || response.Error.Code != "APPROVAL_REQUIRED" {
		t.Fatalf("review-execute exit=%d response=%#v", exit, response)
	}
}

func TestRevisionCommandsNeedNeitherProviderSecretsNorHTTP(t *testing.T) {
	root := writeCommandVault(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-sonnet-5","content":[{"type":"text","text":"# 成果物\n\n本文"}],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer server.Close()
	var executeOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"execute"}, commandArgs(root)...), "--approved"), &executeOutput, commandDependencies{
		lookupEnv: func(key string) (string, bool) {
			values := map[string]string{"ANTHROPIC_API_KEY": "fake", "WORKCAIRN_CLAUDE_BASE_URL": server.URL}
			value, found := values[key]
			return value, found
		},
		now: commandTestTime, newHTTPClient: func(time.Duration) claude.HTTPDoer { return server.Client() },
	}); exit != 0 {
		t.Fatalf("Task execute failed: %s", executeOutput.String())
	}
	reviews := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews")
	if err := os.MkdirAll(reviews, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(reviews, "TASK-001.review.json"), `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件不足","suggested_action":"追記する"}]}`+"\n")
	writeCommandFile(t, filepath.Join(reviews, "TASK-001.review.md"), "---\ntype: review\nproject: ToDoアプリ\ntask_id: TASK-001\n---\n")
	dependencies := commandDependencies{
		lookupEnv:     func(string) (string, bool) { t.Fatal("Revision read Provider environment"); return "", false },
		now:           commandTestTime,
		newHTTPClient: func(time.Duration) claude.HTTPDoer { t.Fatal("Revision constructed HTTP client"); return nil },
	}
	args := commandArgs(root)
	before := commandVaultSnapshot(t, root)
	var planOutput bytes.Buffer
	if exit := run(context.Background(), append([]string{"revision-plan"}, args...), &planOutput, dependencies); exit != 0 {
		t.Fatalf("revision-plan failed: %s", planOutput.String())
	}
	if after := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("revision-plan changed temporary Vault")
	}
	var revisionOutput bytes.Buffer
	if exit := run(context.Background(), append(append([]string{"revision-execute"}, args...), "--approved"), &revisionOutput, dependencies); exit != 0 {
		t.Fatalf("revision-execute failed: %s", revisionOutput.String())
	}
	response := decodeCommandResponse(t, revisionOutput.Bytes())
	if !response.OK || response.Error != nil || !strings.Contains(revisionOutput.String(), `"revision_task_id":"TASK-002"`) {
		t.Fatalf("Revision response = %s", revisionOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Revisions", "TASK-002.revision.md")); err != nil {
		t.Fatalf("Revision intent missing: %v", err)
	}
}

func TestReviewPublicationFailureResponsePreservesCommitState(t *testing.T) {
	result := workspaceprocess.ReviewExecutionResult{
		Status: "partial_failure",
		Artifact: &review.Record{
			TaskID: "TASK-001", CanonicalCommitted: true, ProjectionCommitted: true,
		},
		EventID: "event-001",
	}
	response := reviewFailureResponse(&service.ReviewOrchestrationError{
		PublicationErr: errors.New("audit unavailable"),
	}, result)
	if response.Error == nil || response.Error.Code != "REVIEW_EVENT_PUBLISH_FAILED" ||
		!response.Error.CanonicalCommitted || !response.Error.ProjectionCommitted {
		t.Fatalf("response = %#v", response)
	}
}

func TestMigrationCommandsRequireSavedPlanAndExplicitApproval(t *testing.T) {
	root := writeCommandVault(t)
	tasksPath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
	managed, err := os.ReadFile(tasksPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Split(string(managed), "<!-- workspace-os-task-metadata:v1")[0]
	if err := os.WriteFile(tasksPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	before := commandVaultSnapshot(t, root)

	var planOutput bytes.Buffer
	exitCode := run(context.Background(), []string{
		"migrate-plan", "--vault", root, "--project", "ToDoアプリ",
	}, &planOutput, commandDependencies{})
	planResponse := decodeCommandResponse(t, planOutput.Bytes())
	if exitCode != 0 || !planResponse.OK || !strings.Contains(planOutput.String(), `"source_revision":"sha256:`) {
		t.Fatalf("migrate-plan exit=%d response=%s", exitCode, planOutput.String())
	}
	if afterPlan := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, afterPlan) {
		t.Fatal("migrate-plan changed temporary Vault")
	}
	planPath := filepath.Join(t.TempDir(), "migration-plan.json")
	if err := os.WriteFile(planPath, planOutput.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var unapprovedOutput bytes.Buffer
	exitCode = run(context.Background(), []string{
		"migrate-apply", "--vault", root, "--project", "ToDoアプリ", "--plan-file", planPath,
	}, &unapprovedOutput, commandDependencies{})
	unapproved := decodeCommandResponse(t, unapprovedOutput.Bytes())
	if exitCode != 1 || unapproved.Error == nil || unapproved.Error.Code != "MIGRATION_APPROVAL_REQUIRED" {
		t.Fatalf("unapproved migrate-apply exit=%d response=%#v", exitCode, unapproved)
	}
	if afterUnapproved := commandVaultSnapshot(t, root); !reflect.DeepEqual(before, afterUnapproved) {
		t.Fatal("unapproved migrate-apply changed temporary Vault")
	}

	var applyOutput bytes.Buffer
	exitCode = run(context.Background(), []string{
		"migrate-apply", "--vault", root, "--project", "ToDoアプリ", "--plan-file", planPath, "--approved",
	}, &applyOutput, commandDependencies{})
	applied := decodeCommandResponse(t, applyOutput.Bytes())
	if exitCode != 0 || !applied.OK {
		t.Fatalf("migrate-apply exit=%d response=%s", exitCode, applyOutput.String())
	}
	updated, err := os.ReadFile(tasksPath)
	if err != nil || !strings.Contains(string(updated), "<!-- workspace-os-task-metadata:v1") {
		t.Fatalf("migrated Tasks.md error=%v content=%s", err, updated)
	}
}

func commandArgs(root string) []string {
	return []string{"--vault", root, "--project-id", "PROJECT-001", "--project", "ToDoアプリ", "--task", "TASK-001"}
}

func commandTestTime() time.Time {
	return time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
}

func decodeCommandResponse(t *testing.T, content []byte) commandResponse {
	t.Helper()
	var response commandResponse
	if err := json.Unmarshal(content, &response); err != nil {
		t.Fatalf("invalid command response: %v: %s", err, content)
	}
	return response
}

func writeCommandVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	employeeDirectory := filepath.Join(root, "社員")
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(employeeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(employeeDirectory, "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeCommandFile(t, filepath.Join(projectDirectory, "Project.md"), "---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	writeCommandFile(t, filepath.Join(projectDirectory, "Tasks.md"), string(fixture))
	writeCommandFile(t, filepath.Join(projectDirectory, "Task Dependencies.md"), "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n| Task ID | Proposed ID | Depends On | Rationale |\n|---|---|---|---|\n| TASK-001 | PROPOSED-001 | なし | 初期Task |\n")
	return root
}

func writeCommandFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commandVaultSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
